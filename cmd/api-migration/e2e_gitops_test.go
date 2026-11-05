//go:build apimigration_e2e

package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// The GitOps cases run the procedure under live GitOps controllers that manage
// both the operator and the OpenBao resources through a parent object, all
// syncing automatically with pruning. They qualify what the GitOps checks
// assume: the tracking metadata the controllers write, that an active parent
// re-applies its children, that a manifest installation goes through its
// checkpoints with manual syncs that keep the openbao.org CRDs, that resuming
// sync mid-procedure stops commit, and that resuming after commit and a Git
// update keeps every migrated Cluster, its storage, and its data.
//
// Git is an OCI registry inside the Kind cluster: both Argo CD and Flux read
// charts and manifests from it, and the test pushes new revisions to it.

type gitopsController struct {
	kind    string // gitopsArgoCD, gitopsFlux, or gitopsFluxHelm
	version string
}

const (
	gitopsArgoCD = "argocd"
	gitopsFlux   = "flux"
	// gitopsFluxHelm installs the operator with a Flux HelmRelease.
	gitopsFluxHelm = "flux-helmrelease"
	// argoEnabled is how an Argo CD repository Secret spells a true flag.
	argoEnabled = "true"
	// fluxSuspended is how kubectl prints a suspended Flux object's spec.suspend.
	fluxSuspended = "true"
)

// e2eGitOpsControllers are the supported controller versions.
var e2eGitOpsControllers = map[string]gitopsController{
	"argocd-3.4": {gitopsArgoCD, "v3.4.13"},
	"argocd-3.5": {gitopsArgoCD, "v3.5.7"},
	"argocd-3.6": {gitopsArgoCD, "v3.6.1"},
	"flux-2.9":   {gitopsFlux, "v2.9.6"},
	"flux-2.10":  {gitopsFlux, "v2.10.0"},
	// The 0.6.1 chart survives helm-controller's post-rendering (#934).
	"flux-2.9-helmrelease":  {gitopsFluxHelm, "v2.9.6"},
	"flux-2.10-helmrelease": {gitopsFluxHelm, "v2.10.0"},
}

const (
	e2eRegistryNS       = "gitops-registry"
	e2eRegistryImage    = "registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
	e2eRegistryHost     = "registry." + e2eRegistryNS + ".svc:5000"
	e2eDestinationChart = "0.7.0-e2e"
	e2eManifestLayer    = "application/vnd.oci.image.layer.v1.tar+gzip"
	e2eHelmChartLayer   = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
	e2eArgoNS           = "argocd"
	e2eFluxNS           = "flux-system"
	e2eOperatorApp      = "openbao-operator"
	e2eResourcesApp     = "openbao-resources"
	e2ePlatformApp      = "platform"
)

var registryForwarding = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+) -> 5000`)

// installGitOps replaces the source installation: the GitOps controller
// installs the source chart and creates the source Cluster.
func (f *e2eFixture) installGitOps() {
	f.t.Helper()
	f.kubectl([]byte(fmt.Sprintf(e2eIdentities, e2eOperatorNS, e2eDataNS, e2eAdmin)), "apply", "-f", "-")
	f.deployRegistry()
	f.pushChart(f.releaseChart(e2eSourceRelease), e2eSourceRelease)
	root := strings.TrimSpace(f.cmd("git", nil, "rev-parse", "--show-toplevel"))
	f.pushChart(filepath.Join(root, "charts/openbao-operator"), e2eDestinationChart)
	resources := f.gitopsResources()
	f.pushManifests("resources", "source", resources)
	converted, changed := convertManifest(resources)
	require.Positive(f.t, changed)
	f.pushManifests("resources", e2eDestination, converted)

	switch f.shape.gitops.kind {
	case gitopsArgoCD:
		f.installArgoCD()
	case gitopsFlux, gitopsFluxHelm:
		f.installFlux()
	}
	f.eventually("the GitOps controller to install the source operator", 10*time.Minute, func() bool {
		_, err := f.kubectlOutput("-n", e2eOperatorNS, "wait", "--for=condition=Available", "deployment", "--all",
			"--timeout=5s")
		return err == nil
	})
	f.eventually("the GitOps controller to create the source Cluster", 10*time.Minute, func() bool {
		return clusterReady(f.cluster(primitivesSourceGroup))
	})
	f.waitSettled()
}

// gitopsResources are the OpenBao resources Git holds: in multi-tenant mode
// the Tenants and the self-service namespace, then the Cluster.
func (f *e2eFixture) gitopsResources() string {
	objects := []map[string]any{}
	if f.shape.tenants {
		objects = append(objects, map[string]any{"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]any{"name": e2eSelfNS}})
		for _, tenant := range e2eTenants {
			objects = append(objects, map[string]any{"apiVersion": primitivesSourceGroup + "/v1alpha1",
				"kind": "OpenBaoTenant", "metadata": map[string]any{"name": tenant.name, "namespace": tenant.namespace},
				"spec": map[string]any{"targetNamespace": tenant.target}})
		}
	}
	objects = append(objects, f.sourceCluster())
	if f.hardenedSource != nil {
		objects = append(objects, f.hardenedSource)
	}
	return joinManifests(objects...)
}

func (f *e2eFixture) deployRegistry() {
	f.t.Helper()
	f.kubectl([]byte(fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata: {name: %[1]s}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: registry, namespace: %[1]s}
spec:
  selector: {matchLabels: {app: registry}}
  template:
    metadata: {labels: {app: registry}}
    spec:
      containers:
        - name: registry
          image: %[2]s
          imagePullPolicy: IfNotPresent
          ports: [{containerPort: 5000}]
---
apiVersion: v1
kind: Service
metadata: {name: registry, namespace: %[1]s}
spec:
  selector: {app: registry}
  ports: [{port: 5000, targetPort: 5000}]
`, e2eRegistryNS, f.images["registry"])), "apply", "-f", "-")
	f.kubectl(nil, "-n", e2eRegistryNS, "rollout", "status", "deployment/registry", "--timeout=180s")
}

// registry returns a local address that forwards to the in-cluster registry.
func (f *e2eFixture) registry() string {
	f.t.Helper()
	if f.registryAddr != "" {
		return f.registryAddr
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", f.kubeconfig, "--context", f.context,
		"-n", e2eRegistryNS, "port-forward", "svc/registry", ":5000", "--address", "127.0.0.1")
	stdout, err := cmd.StdoutPipe()
	require.NoError(f.t, err)
	require.NoError(f.t, cmd.Start())
	f.t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	ports := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if match := registryForwarding.FindStringSubmatch(scanner.Text()); match != nil {
				ports <- match[1]
			}
		}
	}()
	select {
	case port := <-ports:
		f.registryAddr = "127.0.0.1:" + port
	case <-time.After(30 * time.Second):
		f.t.Fatal("the registry port-forward did not start")
	}
	return f.registryAddr
}

// pushChart publishes a chart directory as version.
func (f *e2eFixture) pushChart(dir, version string) {
	f.t.Helper()
	work, err := os.MkdirTemp(f.dir, "chart-")
	require.NoError(f.t, err)
	f.cmd("cp", nil, "-R", dir, filepath.Join(work, "openbao-operator"))
	chart := filepath.Join(work, "openbao-operator", "Chart.yaml")
	data, err := os.ReadFile(chart)
	require.NoError(f.t, err)
	data = regexp.MustCompile(`(?m)^version:.*$`).ReplaceAll(data, []byte("version: "+version))
	require.NoError(f.t, os.WriteFile(chart, data, 0o600))
	f.cmd("helm", nil, "package", filepath.Join(work, "openbao-operator"), "-d", work)
	f.cmd("helm", nil, "push", filepath.Join(work, "openbao-operator-"+version+".tgz"),
		"oci://"+f.registry()+"/charts", "--plain-http")
}

// pushManifests publishes manifests as a single-layer OCI artifact, the
// format both Argo CD and Flux read.
func (f *e2eFixture) pushManifests(repository, tag, manifests string) {
	f.t.Helper()
	var archive bytes.Buffer
	zipped := gzip.NewWriter(&archive)
	writer := tar.NewWriter(zipped)
	require.NoError(f.t, writer.WriteHeader(&tar.Header{Name: "manifests.yaml", Mode: 0o644, Size: int64(len(manifests)),
		ModTime: time.Unix(0, 0)}))
	_, err := writer.Write([]byte(manifests))
	require.NoError(f.t, err)
	require.NoError(f.t, writer.Close())
	require.NoError(f.t, zipped.Close())
	image := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	image = mutate.ConfigMediaType(image, types.OCIConfigJSON)
	image, err = mutate.AppendLayers(image, static.NewLayer(archive.Bytes(), e2eManifestLayer))
	require.NoError(f.t, err)
	ref, err := name.ParseReference(f.registry()+"/"+repository+":"+tag, name.Insecure)
	require.NoError(f.t, err)
	require.NoError(f.t, remote.Write(ref, image))
}

func joinManifests(objects ...map[string]any) string {
	var out strings.Builder
	for _, object := range objects {
		data, _ := yaml.Marshal(object)
		out.WriteString("---\n")
		out.Write(data)
	}
	return out.String()
}

// waitFor polls a kubectl jsonpath until it prints want.
func (f *e2eFixture) waitFor(what string, timeout time.Duration, want string, args ...string) {
	f.t.Helper()
	f.eventually(what, timeout, func() bool {
		out, err := f.kubectlOutput(args...)
		return err == nil && strings.TrimSpace(out) == want
	})
}

// planRefused requires plan to refuse with the given reason.
func (f *e2eFixture) planRefused(state, reason string) {
	f.t.Helper()
	err := f.run("plan", f.newState(state), "--config", f.configFile())
	require.ErrorContains(f.t, err, reason)
}

// gitopsProcedure runs the procedure under the controller.
func (f *e2eFixture) gitopsProcedure() {
	f.t.Helper()
	switch f.shape.gitops.kind {
	case gitopsArgoCD:
		if f.shape.sharedGitOps {
			f.argoSharedProcedure()
			return
		}
		f.argoProcedure()
	case gitopsFlux, gitopsFluxHelm:
		f.fluxProcedure()
	}
}

// assertResumed checks that resuming sync after commit kept the migrated
// Cluster: same object, same claim, its data, and ready.
func (f *e2eFixture) assertResumed(clusterUID, claimUID string) {
	f.t.Helper()
	f.eventually("the migrated Cluster to stay ready under GitOps", 5*time.Minute, func() bool {
		return clusterReady(f.cluster("kubebao.org"))
	})
	require.Equal(f.t, clusterUID, string(f.cluster("kubebao.org").GetUID()), "GitOps must not replace the Cluster")
	require.Nil(f.t, f.cluster(primitivesSourceGroup), "GitOps must not recreate the openbao.org Cluster")
	claim := f.getJSON("get", "pvc", "data-"+f.voterSet+"-0", "-n", e2eDataNS)
	require.Equal(f.t, claimUID, claim["metadata"].(map[string]any)["uid"], "the data claim must survive")
	f.assertData(f.written...)
}

func (f *e2eFixture) migratedIdentity() (string, string) {
	f.t.Helper()
	claim := f.getJSON("get", "pvc", "data-"+f.voterSet+"-0", "-n", e2eDataNS)
	return string(f.cluster("kubebao.org").GetUID()), claim["metadata"].(map[string]any)["uid"].(string)
}

// ---------------------------------------------------------------------------
// Flux
//
// A Flux installation applies the operator either with a Kustomization of the
// Kustomize build (install.yaml) or with a HelmRelease of the chart. The 0.6.0
// chart's admission policies did not survive helm-controller's post-rendering;
// 0.6.1 includes the fix (#934). Either way, a parent Kustomization applies the
// operator object and a Kustomization of the OpenBao resources.

func (f *e2eFixture) installFlux() {
	f.t.Helper()
	f.kubectl(nil, "apply", "--server-side", "-f",
		"https://github.com/fluxcd/flux2/releases/download/"+f.shape.gitops.version+"/install.yaml")
	f.kubectl(nil, "-n", e2eFluxNS, "wait", "--for=condition=Available", "deployment", "--all", "--timeout=300s")
	if f.shape.gitops.kind == gitopsFluxHelm {
		f.pushManifests(e2ePlatformApp, "v1", f.fluxHelmPlatform(e2eSourceRelease, f.images["source"]))
	} else {
		f.pushFluxOperatorBundle("source", e2eSourceRelease, f.images["source"])
		f.pushFluxOperatorBundle(e2eDestination, e2eDestination, f.images[e2eDestination])
		f.pushManifests(e2ePlatformApp, "v1", f.fluxPlatform("source"))
	}
	f.kubectl([]byte(joinManifests(
		fluxOCIRepositoryObject(e2ePlatformApp, "oci://"+e2eRegistryHost+"/"+e2ePlatformApp, "v1"),
		fluxKustomizationObject(e2ePlatformApp, ""))), "apply", "-f", "-")
}

func fluxOCIRepositoryObject(object, url, tag string) map[string]any {
	return map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "OCIRepository",
		"metadata": map[string]any{"name": object, "namespace": e2eFluxNS},
		"spec": map[string]any{"interval": "30s", "url": url, "ref": map[string]any{"tag": tag}, "insecure": true,
			"layerSelector": map[string]any{"mediaType": e2eManifestLayer, "operation": "extract"}}}
}

// fluxKustomizationObject applies an OCIRepository of the same name, with pruning.
func fluxKustomizationObject(object, dependsOn string) map[string]any {
	spec := map[string]any{"interval": "30s", "retryInterval": "15s", "path": "./", "prune": true,
		"sourceRef": map[string]any{"kind": "OCIRepository", "name": object}}
	if dependsOn != "" {
		spec["dependsOn"] = []any{map[string]any{"name": dependsOn}}
	}
	return map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": object, "namespace": e2eFluxNS}, "spec": spec}
}

// fluxPlatform is the parent's revision: the operator manifests and the
// OpenBao resources at one tag, each applied by its own Kustomization.
func fluxPlatform(tag string) string {
	return joinManifests(
		fluxOCIRepositoryObject(e2eOperatorApp, "oci://"+e2eRegistryHost+"/"+e2eOperatorApp, tag),
		fluxKustomizationObject(e2eOperatorApp, ""),
		fluxOCIRepositoryObject(e2eResourcesApp, "oci://"+e2eRegistryHost+"/resources", tag),
		fluxKustomizationObject(e2eResourcesApp, e2eOperatorApp))
}

func (f *e2eFixture) fluxPlatform(tag string) string {
	if f.shape.sharedGitOps {
		return joinManifests(
			fluxOCIRepositoryObject(e2eOperatorApp, "oci://"+e2eRegistryHost+"/"+e2eOperatorApp, tag),
			fluxKustomizationObject(e2eOperatorApp, ""))
	}
	return fluxPlatform(tag)
}

// fluxHelmPlatform is the parent's revision for a HelmRelease installation:
// the chart at version with the e2e values, and the OpenBao resources.
func (f *e2eFixture) fluxHelmPlatform(version, image string) string {
	return joinManifests(f.fluxHelmPlatformObjects(version, image)...)
}

func (f *e2eFixture) fluxHelmPlatformObjects(version, image string) []map[string]any {
	tag := e2eDestination
	if version == e2eSourceRelease {
		tag = "source"
	}
	chart := map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "OCIRepository",
		"metadata": map[string]any{"name": e2eOperatorApp, "namespace": e2eFluxNS},
		"spec": map[string]any{"interval": "30s", "url": "oci://" + e2eRegistryHost + "/charts/openbao-operator",
			"ref": map[string]any{"tag": version}, "insecure": true,
			"layerSelector": map[string]any{"mediaType": e2eHelmChartLayer, "operation": "copy"}}}
	release := map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": e2eOperatorApp, "namespace": e2eFluxNS},
		"spec": map[string]any{"interval": "30s", "releaseName": e2eRelease, "targetNamespace": e2eOperatorNS,
			"storageNamespace": e2eOperatorNS, "chartRef": map[string]any{"kind": "OCIRepository", "name": e2eOperatorApp},
			"install": map[string]any{"crds": "CreateReplace"}, "upgrade": map[string]any{"crds": "CreateReplace"},
			"values": f.chartValues(image)}}
	if f.shape.explicitSuspend {
		spec := release["spec"].(map[string]any)
		spec["suspend"] = false
		// Flux defaults Helm storage to the HelmRelease namespace while
		// the operator's resources remain in targetNamespace.
		delete(spec, "storageNamespace")
	}
	// The resources wait for the chart's CRDs by retrying.
	return []map[string]any{chart, release,
		fluxOCIRepositoryObject(e2eResourcesApp, "oci://"+e2eRegistryHost+"/resources", tag),
		fluxKustomizationObject(e2eResourcesApp, "")}
}

// fluxKind is the kind of a Flux object: the operator is a HelmRelease in a
// HelmRelease installation, and everything else is a Kustomization.
func (f *e2eFixture) fluxKind(object string) string {
	if object == e2eOperatorApp && f.shape.gitops.kind == gitopsFluxHelm {
		return "helmrelease"
	}
	return "kustomization"
}

func (f *e2eFixture) fluxSuspend(object string, suspend bool) {
	f.t.Helper()
	f.kubectl(nil, "-n", e2eFluxNS, "patch", f.fluxKind(object), object, "--type", "merge",
		"-p", fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend))
}

// fluxReconcile requests a reconciliation. A parent that re-applies the object
// may drop the request annotation, so callers wait for a state, not for it.
func (f *e2eFixture) fluxReconcile(kind, object string) {
	f.t.Helper()
	f.kubectl(nil, "-n", e2eFluxNS, "annotate", "--overwrite", kind, object,
		"reconcile.fluxcd.io/requestedAt="+time.Now().Format(time.RFC3339Nano))
}

// fluxResume resumes the parent and then its children, as after-commit.md
// says. Each object resumes only after its OCIRepository serves the tag Git
// names, because a resumed HelmRelease installs whatever chart its source
// serves. It counts as resumed once it reports that revision and is ready; a
// Ready condition alone can predate the suspension.
func (f *e2eFixture) fluxResume() {
	f.t.Helper()
	for _, object := range f.fluxObjects() {
		kind := f.fluxKind(object)
		f.fluxReconcile("ocirepository", object)
		revision := ""
		f.eventually("OCIRepository "+object+" to serve the tag Git names", 10*time.Minute, func() bool {
			source := f.getJSON("-n", e2eFluxNS, "get", "ocirepository", object)
			tag, _, _ := unstructured.NestedString(source, "spec", "ref", "tag")
			revision, _, _ = unstructured.NestedString(source, "status", "artifact", "revision")
			return strings.HasPrefix(revision, tag+"@")
		})
		f.fluxSuspend(object, false)
		f.fluxReconcile(kind, object)
		f.eventually(kind+" "+object+" to apply "+revision, 10*time.Minute, func() bool {
			return f.fluxApplied(kind, object, revision)
		})
	}
}

// fluxApplied reports whether a Flux object is ready at its current generation
// and has applied the source revision: a Kustomization records the artifact
// revision, a HelmRelease the chart version with the digest's first 12 hex digits.
func (f *e2eFixture) fluxApplied(kind, object, revision string) bool {
	status := f.getJSON("-n", e2eFluxNS, "get", kind, object)
	generation, _, _ := unstructured.NestedInt64(status, "metadata", "generation")
	observed, _, _ := unstructured.NestedInt64(status, "status", "observedGeneration")
	conditions, _, _ := unstructured.NestedSlice(status, "status", "conditions")
	ready := false
	for _, condition := range conditions {
		if c, ok := condition.(map[string]any); ok && c["type"] == "Ready" && c["status"] == e2eConditionTrue {
			ready = true
		}
	}
	if !ready || observed != generation {
		return false
	}
	if kind == "helmrelease" {
		tag, digest, _ := strings.Cut(revision, "@sha256:")
		history, _, _ := unstructured.NestedSlice(status, "status", "history")
		if len(history) == 0 || len(digest) < 12 {
			return false
		}
		latest, _ := history[0].(map[string]any)
		return latest["chartVersion"] == tag+"+"+digest[:12] && latest["status"] == "deployed"
	}
	applied, _, _ := unstructured.NestedString(status, "status", "lastAppliedRevision")
	return applied == revision
}

// fluxSuspendAll suspends the parent and then its children. A parent
// reconciliation that started before the parent's suspension can still
// re-apply a child afterwards and drop its pause, so this re-suspends any child
// that resumes until every object has stayed suspended for 30 seconds.
func (f *e2eFixture) fluxSuspendAll() {
	f.t.Helper()
	for _, object := range f.fluxObjects() {
		f.fluxSuspend(object, true)
	}
	stableSince := time.Now()
	f.eventually("every Flux object to stay suspended", 5*time.Minute, func() bool {
		for _, object := range f.fluxObjects() {
			out, err := f.kubectlOutput("-n", e2eFluxNS, "get", f.fluxKind(object), object, "-o", "jsonpath={.spec.suspend}")
			if err != nil || strings.TrimSpace(out) != fluxSuspended {
				f.fluxSuspend(object, true)
				stableSince = time.Now()
			}
		}
		return time.Since(stableSince) >= 30*time.Second
	})
}

func (f *e2eFixture) fluxProcedure() {
	f.t.Helper()
	if f.shape.sharedGitOps {
		f.assertSharedTracking(primitivesSourceGroup)
	}
	if f.shape.explicitSuspend {
		f.waitFor("Git to declare the source HelmRelease unsuspended", time.Minute, "false",
			"-n", e2eFluxNS, "get", "helmrelease", e2eOperatorApp, "-o", "jsonpath={.spec.suspend}")
	}
	f.planRefused("refused-active", "is not suspended")
	for _, object := range f.fluxObjects()[1:] {
		f.fluxSuspend(object, true)
	}
	// The parent would apply new child revisions from Git. With Git owning
	// suspend:false it may also overwrite the child's manual pause first.
	parentReason := "Kustomization flux-system/" + e2ePlatformApp + " is not suspended"
	if f.shape.explicitSuspend {
		parentReason = "is not suspended"
	}
	f.planRefused("refused-parent", parentReason)
	// The active parent may overwrite the first child pauses while the
	// refusal is checked. Freeze the parent, then restore every child pause.
	f.fluxSuspendAll()

	// Migrate and roll back through the checkpoints, then resume: Flux takes
	// the source back over.
	state := f.newState("rollback")
	f.plan(state)
	f.migrate(state)
	f.procedure("rollback", state)
	f.assertSurvivor(primitivesSourceGroup, "rollback")
	if f.shape.sharedGitOps {
		f.assertTenants(primitivesSourceGroup, true)
	}
	if f.shape.hardened {
		f.assertHardened(primitivesSourceGroup, true)
		if f.shape.tenants {
			f.assertTenants(primitivesSourceGroup, true)
		}
	}
	f.fluxResume()
	require.NotNil(f.t, f.cluster(primitivesSourceGroup))
	// Flux reverts the test's own changes to what Git holds, and the source
	// operator may roll the Cluster for them: plan requires it ready again.
	f.waitSettled()
	f.assertData(f.written...)

	// Suspend again, migrate, and commit; resuming one child mid-procedure stops commit.
	f.fluxSuspendAll()
	state = f.newState("migrate")
	f.plan(state)
	f.migrate(state)
	resumedChild := e2eResourcesApp
	stagedParentRevision := ""
	if f.shape.sharedGitOps {
		// Keep the shared child suspended in Git before resuming its parent.
		// Otherwise the parent can overwrite its manual pause and reapply
		// the source operator and CRs during this negative rehearsal.
		child := fluxKustomizationObject(e2eOperatorApp, "")
		child["spec"].(map[string]any)["suspend"] = true
		f.pushManifests(e2ePlatformApp, "v2-suspended", joinManifests(
			fluxOCIRepositoryObject(e2eOperatorApp, "oci://"+e2eRegistryHost+"/"+e2eOperatorApp, e2eDestination), child))
		f.kubectl(nil, "-n", e2eFluxNS, "patch", "ocirepository", e2ePlatformApp, "--type", "merge",
			"-p", `{"spec":{"ref":{"tag":"v2-suspended"}}}`)
		f.fluxReconcile("ocirepository", e2ePlatformApp)
		stagedParentRevision = f.fluxSourceRevision(e2ePlatformApp, "v2-suspended")
		resumedChild = e2ePlatformApp
	}
	f.fluxSuspend(resumedChild, false)
	if f.shape.sharedGitOps {
		f.fluxReconcile("kustomization", e2ePlatformApp)
		f.eventually("the parent to apply the suspended shared child", 10*time.Minute, func() bool {
			return f.fluxApplied("kustomization", e2ePlatformApp, stagedParentRevision)
		})
	}
	require.ErrorContains(f.t, f.run("commit", state), "is not suspended")
	f.fluxSuspend(resumedChild, true)
	if f.shape.sharedGitOps {
		f.waitFor("the shared child to remain suspended during the parent refusal", time.Minute, "true",
			"-n", e2eFluxNS, "get", "kustomization", e2eOperatorApp, "-o", "jsonpath={.spec.suspend}")
	}
	f.procedure("commit", state)
	f.assertSurvivor("kubebao.org", "commit")
	if f.shape.sharedGitOps {
		f.assertTenants("kubebao.org", true)
	}
	clusterUID, claimUID := f.migratedIdentity()

	// Update Git as after-commit.md says, then resume.
	platform := f.fluxPlatform(e2eDestination)
	if f.shape.gitops.kind == gitopsFluxHelm {
		platform = f.fluxHelmPlatform(e2eDestinationChart, f.images[e2eDestination])
	}
	if f.shape.explicitSuspend {
		f.fluxStageHelmResume()
	} else {
		f.pushManifests(e2ePlatformApp, "v2", platform)
		f.kubectl(nil, "-n", e2eFluxNS, "patch", "ocirepository", e2ePlatformApp, "--type", "merge",
			"-p", `{"spec":{"ref":{"tag":"v2"}}}`)
		f.fluxResume()
	}
	if f.shape.gitops.kind == gitopsFluxHelm {
		// Helm never deletes the CRDs in a chart's crds directory, so the
		// openbao.org CRDs stay for the administrator's clean-up.
		require.NotEmpty(f.t, f.kubectl(nil, "get", "crd", "openbaoclusters."+primitivesSourceGroup, "-o", "name"),
			"helm-controller must keep the source chart's CRDs")
	} else {
		// Pruning removes what only the source release had, including the
		// empty openbao.org CRDs.
		f.eventually("Flux to prune the openbao.org CRDs", 5*time.Minute, func() bool {
			_, err := f.kubectlOutput("get", "crd", "openbaoclusters."+primitivesSourceGroup)
			return err != nil
		})
	}
	f.assertResumed(clusterUID, claimUID)
	if f.shape.sharedGitOps {
		f.assertSharedTracking("kubebao.org")
	}
}

// ---------------------------------------------------------------------------
// Argo CD

func (f *e2eFixture) installArgoCD() {
	f.t.Helper()
	f.kubectl(nil, "create", "namespace", e2eArgoNS)
	f.kubectl(nil, "apply", "-n", e2eArgoNS, "--server-side", "--force-conflicts", "-f",
		"https://raw.githubusercontent.com/argoproj/argo-cd/"+f.shape.gitops.version+"/manifests/core-install.yaml")
	for _, workload := range []string{"deployment/argocd-repo-server", "deployment/argocd-applicationset-controller",
		"statefulset/argocd-application-controller"} {
		f.kubectl(nil, "-n", e2eArgoNS, "rollout", "status", workload, "--timeout=300s")
	}
	repositories := make([]map[string]any, 0, 4)
	for _, repository := range []struct{ name, kind, url string }{
		{"e2e-charts", "helm", e2eRegistryHost + "/charts"},
		{"e2e-resources", "oci", "oci://" + e2eRegistryHost + "/resources"},
		{"e2e-platform", "oci", "oci://" + e2eRegistryHost + "/" + e2ePlatformApp},
	} {
		data := map[string]any{"name": repository.name, "type": repository.kind, "url": repository.url,
			"insecureOCIForceHttp": argoEnabled}
		if repository.kind == "helm" {
			data["enableOCI"] = argoEnabled
		}
		repositories = append(repositories, map[string]any{"apiVersion": "v1", "kind": "Secret",
			"metadata": map[string]any{"name": repository.name, "namespace": e2eArgoNS,
				"labels": map[string]any{"argocd.argoproj.io/secret-type": "repository"}},
			"stringData": data})
	}
	// The core installation has no API server, which would create the default project.
	repositories = append(repositories, map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "AppProject",
		"metadata": map[string]any{"name": "default", "namespace": e2eArgoNS},
		"spec": map[string]any{"sourceRepos": []any{"*"},
			"destinations":             []any{map[string]any{"namespace": "*", "server": "*"}},
			"clusterResourceWhitelist": []any{map[string]any{"group": "*", "kind": "*"}}}})
	f.kubectl([]byte(joinManifests(repositories...)), "apply", "-f", "-")
	f.pushManifests(e2ePlatformApp, "v1", f.argoPlatform(e2eSourceRelease, f.chartValues(f.images["source"]), "source"))
	f.kubectl([]byte(joinManifests(argoApplicationObject(e2ePlatformApp, map[string]any{
		"repoURL": "oci://" + e2eRegistryHost + "/" + e2ePlatformApp, "targetRevision": "v1", "path": "."},
		e2eArgoNS))), "apply", "-f", "-")
}

func argoApplicationObject(app string, source map[string]any, namespace string) map[string]any {
	return map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": app, "namespace": e2eArgoNS},
		"spec": map[string]any{"project": "default", "source": source,
			"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": namespace},
			"syncPolicy": map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true},
				// The OpenBao CRDs exceed the client-side apply annotation limit.
				"syncOptions": []any{"ServerSideApply=true"}}}}
}

// argoPlatform is the parent's revision: app-of-apps for the operator chart
// and the OpenBao resources.
func (f *e2eFixture) argoPlatform(chartVersion string, values map[string]any, resources string) string {
	if f.shape.sharedGitOps {
		return f.argoSharedPlatform(chartVersion, values, resources)
	}
	return joinManifests(
		argoApplicationObject(e2eOperatorApp, map[string]any{"repoURL": e2eRegistryHost + "/charts",
			"chart": "openbao-operator", "targetRevision": chartVersion,
			"helm": map[string]any{"releaseName": e2eRelease, "valuesObject": values}}, e2eOperatorNS),
		argoApplicationObject(e2eResourcesApp, map[string]any{"repoURL": "oci://" + e2eRegistryHost + "/resources",
			"targetRevision": resources, "path": "."}, e2eDataNS))
}

// argoAutomated turns an Application's automatic sync on or off.
func (f *e2eFixture) argoAutomated(app string, on bool) {
	f.t.Helper()
	patch := `[{"op":"remove","path":"/spec/syncPolicy/automated"}]`
	if on {
		patch = `[{"op":"add","path":"/spec/syncPolicy/automated","value":{"prune":true,"selfHeal":true}}]`
	}
	f.kubectl(nil, "-n", e2eArgoNS, "patch", "application", app, "--type", "json", "-p", patch)
}

func (f *e2eFixture) argoHasAutomated(app string) bool {
	out, err := f.kubectlOutput("-n", e2eArgoNS, "get", "application", app, "-o", "jsonpath={.spec.syncPolicy.automated}")
	return err == nil && strings.TrimSpace(out) != ""
}

func (f *e2eFixture) argoHealthy(app string) {
	f.t.Helper()
	f.waitFor("Application "+app+" to be synced and healthy", 10*time.Minute, "Synced/Healthy",
		"-n", e2eArgoNS, "get", "application", app, "-o", "jsonpath={.status.sync.status}/{.status.health.status}")
}

// argoSync applies a checkpoint's operator state as an administrator does with
// Argo CD: set the chart version and values, then sync manually without
// pruning, so the source's CRDs and their resources stay.
func (f *e2eFixture) argoSync(state string) {
	f.t.Helper()
	if f.shape.sharedGitOps {
		f.argoSharedSync(state, false)
		return
	}
	version, values := e2eDestinationChart, f.chartValues(f.images[e2eDestination])
	if state == stateSource.name {
		version, values = e2eSourceRelease, f.chartValues(f.images["source"])
	}
	if overrides := f.checkpointValues(state); overrides != nil {
		values = mergeValues(values, overrides)
	}
	patch, _ := json.Marshal([]map[string]any{
		{"op": "replace", "path": "/spec/source/targetRevision", "value": version},
		{"op": "replace", "path": "/spec/source/helm/valuesObject", "value": values}})
	f.kubectl(nil, "-n", e2eArgoNS, "patch", "application", e2eOperatorApp, "--type", "json", "-p", string(patch))
	started := time.Now().UTC().Format(time.RFC3339)
	operation, _ := json.Marshal(map[string]any{"operation": map[string]any{
		"initiatedBy": map[string]any{"username": "api-migration-e2e"},
		"sync":        map[string]any{"revision": version, "prune": false, "syncOptions": []any{"ServerSideApply=true"}}}})
	f.kubectl(nil, "-n", e2eArgoNS, "patch", "application", e2eOperatorApp, "--type", "merge", "-p", string(operation))
	f.eventually("the manual sync of the "+state+" operator state", 10*time.Minute, func() bool {
		app := f.getJSON("-n", e2eArgoNS, "get", "application", e2eOperatorApp)
		phase, _, _ := unstructured.NestedString(app, "status", "operationState", "phase")
		finished, _, _ := unstructured.NestedString(app, "status", "operationState", "finishedAt")
		revision, _, _ := unstructured.NestedString(app, "status", "operationState", "syncResult", "revision")
		return phase == "Succeeded" && finished >= started && revision == version
	})
	if state != stateStopped.name {
		f.kubectl(nil, "-n", e2eOperatorNS, "wait", "--for=condition=Available", "deployment", "--all", "--timeout=300s")
	}
}

func (f *e2eFixture) argoProcedure() {
	f.t.Helper()
	for _, app := range []string{e2ePlatformApp, e2eOperatorApp, e2eResourcesApp} {
		f.argoHealthy(app)
	}
	f.planRefused("refused-active", "syncs automatically")
	// Suspending only the children does not hold: the parent heals them.
	f.argoAutomated(e2eOperatorApp, false)
	f.argoAutomated(e2eResourcesApp, false)
	f.eventually("the parent to restore automatic sync on its children", 5*time.Minute, func() bool {
		return f.argoHasAutomated(e2eOperatorApp) && f.argoHasAutomated(e2eResourcesApp)
	})
	f.planRefused("refused-parent", "syncs automatically")
	f.argoAutomated(e2ePlatformApp, false)
	f.argoAutomated(e2eOperatorApp, false)
	f.argoAutomated(e2eResourcesApp, false)

	// Migrate and roll back through the checkpoints with manual syncs.
	state := f.newState("rollback")
	f.plan(state)
	f.migrate(state)
	f.procedure("rollback", state)
	f.assertSurvivor(primitivesSourceGroup, "rollback")
	if f.shape.hardened {
		f.assertHardened(primitivesSourceGroup, true)
	}

	// Migrate and commit; resuming one Application mid-procedure stops commit.
	f.waitSettled()
	state = f.newState("migrate")
	f.plan(state)
	f.migrate(state)
	require.NotEmpty(f.t, f.kubectl(nil, "get", "crd", "openbaoclusters."+primitivesSourceGroup, "-o", "name"),
		"manual syncs must not prune the source CRDs")
	f.argoAutomated(e2eResourcesApp, true)
	require.ErrorContains(f.t, f.run("commit", state), "syncs automatically")
	// Meanwhile Argo CD syncs the source back to Git, undoing what the test
	// changed outside Git after the rollback. It must leave the migrated
	// Cluster alone: the import copies no tracking ID.
	f.argoHealthy(e2eResourcesApp)
	require.NotNil(f.t, f.cluster("kubebao.org"), "Argo CD must not prune the migrated Cluster")
	f.argoAutomated(e2eResourcesApp, false)
	require.ErrorContains(f.t, f.run("commit", state), "specification changed since planning")
	// The destination holds the planned specification, so roll back and migrate again.
	f.procedure("rollback", state)
	f.waitSettled()
	state = f.newState("migrate-again")
	f.plan(state)
	f.migrate(state)
	f.procedure("commit", state)
	f.assertSurvivor("kubebao.org", "commit")
	clusterUID, claimUID := f.migratedIdentity()

	// Update Git as after-commit.md says, then resume the parent: it restores
	// automatic sync with pruning on its children.
	f.pushManifests(e2ePlatformApp, "v2", f.argoPlatform(e2eDestinationChart, f.chartValues(f.images[e2eDestination]),
		e2eDestination))
	f.kubectl(nil, "-n", e2eArgoNS, "patch", "application", e2ePlatformApp, "--type", "json",
		"-p", `[{"op":"replace","path":"/spec/source/targetRevision","value":"v2"},`+
			`{"op":"add","path":"/spec/syncPolicy/automated","value":{"prune":true,"selfHeal":true}}]`)
	for _, app := range []string{e2ePlatformApp, e2eOperatorApp, e2eResourcesApp} {
		f.argoHealthy(app)
	}
	// The chart marks its CRDs helm.sh/resource-policy: keep, which Argo CD
	// treats as Delete=false: even automatic pruning keeps the openbao.org CRDs
	// for the administrator's clean-up.
	require.NotEmpty(f.t, f.kubectl(nil, "get", "crd", "openbaoclusters."+primitivesSourceGroup, "-o", "name"),
		"Argo CD must keep the chart's CRDs")
	f.assertResumed(clusterUID, claimUID)
}
