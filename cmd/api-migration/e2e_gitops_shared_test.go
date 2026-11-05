//go:build apimigration_e2e

package main

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

// pushFluxOperatorBundle keeps the existing operator-only artifacts for split
// installations. A shared Kustomization instead reads one operator-and-CR bundle.
func (f *e2eFixture) pushFluxOperatorBundle(tag, release, image string) {
	f.t.Helper()
	bundle := f.kustomizeBuild(release, image, nil)
	if f.shape.sharedGitOps {
		resources := f.gitopsResources()
		if tag == e2eDestination {
			resources, _ = convertManifest(resources)
		}
		bundle += "\n---\n" + resources
	}
	f.pushManifests(e2eOperatorApp, tag, bundle)
}

func (f *e2eFixture) fluxObjects() []string {
	if f.shape.sharedGitOps {
		return []string{e2ePlatformApp, e2eOperatorApp}
	}
	return []string{e2ePlatformApp, e2eOperatorApp, e2eResourcesApp}
}

// fluxStageHelmResume advances the chart source while the parent's desired
// HelmRelease remains suspended. Only a matching artifact permits Git to resume
// the child. This matters when Git owns spec.suspend:false: a manual pause alone
// does not survive the parent's next apply.
func (f *e2eFixture) fluxStageHelmResume() {
	f.t.Helper()
	staged := f.fluxHelmPlatformObjects(e2eDestinationChart, f.images[e2eDestination])
	for _, object := range staged {
		if object["kind"] == "HelmRelease" {
			object["spec"].(map[string]any)["suspend"] = true
		}
	}
	f.pushManifests(e2ePlatformApp, "v2-suspended", joinManifests(staged...))
	f.kubectl(nil, "-n", e2eFluxNS, "patch", "ocirepository", e2ePlatformApp, "--type", "merge",
		"-p", `{"spec":{"ref":{"tag":"v2-suspended"}}}`)
	f.fluxReconcile("ocirepository", e2ePlatformApp)
	parentRevision := f.fluxSourceRevision(e2ePlatformApp, "v2-suspended")
	f.fluxSuspend(e2ePlatformApp, false)
	f.fluxReconcile("kustomization", e2ePlatformApp)
	f.eventually("the parent to apply the staged suspended HelmRelease", 10*time.Minute, func() bool {
		return f.fluxApplied("kustomization", e2ePlatformApp, parentRevision)
	})
	f.waitFor("the staged HelmRelease to remain suspended", time.Minute, "true",
		"-n", e2eFluxNS, "get", "helmrelease", e2eOperatorApp, "-o", "jsonpath={.spec.suspend}")
	f.fluxReconcile("ocirepository", e2eOperatorApp)
	f.fluxSourceRevision(e2eOperatorApp, e2eDestinationChart)
	f.waitFor("the HelmRelease to stay suspended until its destination chart is available", time.Minute, "true",
		"-n", e2eFluxNS, "get", "helmrelease", e2eOperatorApp, "-o", "jsonpath={.spec.suspend}")

	// Git can now declare suspend:false again. Reapplying this parent cannot
	// expose the destination Helm release to the stale source chart artifact.
	f.pushManifests(e2ePlatformApp, "v3", f.fluxHelmPlatform(e2eDestinationChart, f.images[e2eDestination]))
	f.kubectl(nil, "-n", e2eFluxNS, "patch", "ocirepository", e2ePlatformApp, "--type", "merge",
		"-p", `{"spec":{"ref":{"tag":"v3"}}}`)
	f.fluxResume()
	f.waitFor("Git to resume the destination HelmRelease", time.Minute, "false",
		"-n", e2eFluxNS, "get", "helmrelease", e2eOperatorApp, "-o", "jsonpath={.spec.suspend}")
}

func (f *e2eFixture) fluxSourceRevision(object, tag string) string {
	f.t.Helper()
	revision := ""
	f.eventually("OCIRepository "+object+" to serve "+tag, 10*time.Minute, func() bool {
		source := f.getJSON("-n", e2eFluxNS, "get", "ocirepository", object)
		desired, _, _ := unstructured.NestedString(source, "spec", "ref", "tag")
		revision, _, _ = unstructured.NestedString(source, "status", "artifact", "revision")
		generation, _, _ := unstructured.NestedInt64(source, "metadata", "generation")
		observed, _, _ := unstructured.NestedInt64(source, "status", "observedGeneration")
		conditions, _, _ := unstructured.NestedSlice(source, "status", "conditions")
		ready := false
		for _, condition := range conditions {
			if c, ok := condition.(map[string]any); ok && c["type"] == "Ready" && c["status"] == e2eConditionTrue {
				ready = true
			}
		}
		return ready && desired == tag && observed == generation && strings.HasPrefix(revision, tag+"@sha256:")
	})
	return revision
}

// argoSharedPlatform creates one multi-source child for the chart and CRs.
// Keeping the chart as a Helm source preserves the generated checkpoint values.
func (f *e2eFixture) argoSharedPlatform(chartVersion string, values map[string]any, resources string) string {
	chart := map[string]any{"repoURL": e2eRegistryHost + "/charts", "chart": "openbao-operator",
		"targetRevision": chartVersion, "helm": map[string]any{"releaseName": e2eRelease, "valuesObject": values}}
	app := argoApplicationObject(e2eOperatorApp, chart, e2eOperatorNS)
	spec := app["spec"].(map[string]any)
	delete(spec, "source")
	spec["sources"] = []any{chart, map[string]any{"repoURL": "oci://" + e2eRegistryHost + "/resources",
		"targetRevision": resources, "path": "."}}
	return joinManifests(app)
}

// argoSharedSync never prunes. The normal checkpoint selects only operator
// Deployments because its resource source still contains the retired old CRs.
// A whole-app sync is used once to rehearse and detect that operator error.
func (f *e2eFixture) argoSharedSync(state string, wholeApp bool) {
	f.t.Helper()
	version, values := e2eDestinationChart, f.chartValues(f.images[e2eDestination])
	if state == stateSource.name {
		version, values = e2eSourceRelease, f.chartValues(f.images["source"])
	}
	if overrides := f.checkpointValues(state); overrides != nil {
		values = mergeValues(values, overrides)
	}
	patch, err := json.Marshal([]map[string]any{
		{"op": "replace", "path": "/spec/sources/0/targetRevision", "value": version},
		{"op": "replace", "path": "/spec/sources/0/helm/valuesObject", "value": values}})
	require.NoError(f.t, err)
	f.kubectl(nil, "-n", e2eArgoNS, "patch", "application", e2eOperatorApp, "--type", "json", "-p", string(patch))
	sync := map[string]any{"revisions": []any{version, "source"}, "prune": false,
		"syncOptions": []any{"ServerSideApply=true"}}
	if !wholeApp {
		sync["resources"] = f.argoChartResources(version, values, state == stateNormal.name)
	}
	started := time.Now().UTC().Format(time.RFC3339)
	operation, err := json.Marshal(map[string]any{"operation": map[string]any{
		"initiatedBy": map[string]any{"username": "api-migration-e2e"}, "sync": sync}})
	require.NoError(f.t, err)
	f.kubectl(nil, "-n", e2eArgoNS, "patch", "application", e2eOperatorApp, "--type", "merge", "-p", string(operation))
	f.eventually("the shared Application's manual sync of "+state, 10*time.Minute, func() bool {
		app := f.getJSON("-n", e2eArgoNS, "get", "application", e2eOperatorApp)
		phase, _, _ := unstructured.NestedString(app, "status", "operationState", "phase")
		finished, _, _ := unstructured.NestedString(app, "status", "operationState", "finishedAt")
		revisions, _, _ := unstructured.NestedStringSlice(app, "status", "operationState", "syncResult", "revisions")
		return phase == "Succeeded" && finished >= started && len(revisions) == 2 && revisions[0] == version
	})
	if state != stateStopped.name {
		f.kubectl(nil, "-n", e2eOperatorNS, "wait", "--for=condition=Available", "deployment", "--all", "--timeout=300s")
	}
	if !wholeApp {
		app := f.getJSON("-n", e2eArgoNS, "get", "application", e2eOperatorApp)
		results, _, err := unstructured.NestedSlice(app, "status", "operationState", "syncResult", "resources")
		require.NoError(f.t, err)
		require.NotEmpty(f.t, results)
		for _, result := range results {
			resource := result.(map[string]any)
			require.NotEqual(f.t, primitivesSourceGroup, resource["group"], "checkpoints must not apply old-group CRs")
			require.NotEqual(f.t, "kubebao.org", resource["group"], "checkpoints must not apply new-group CRs")
			if state == stateNormal.name {
				require.Equal(f.t, "apps", resource["group"])
				require.Equal(f.t, "Deployment", resource["kind"], "normal must apply only operator Deployments")
			}
		}
	}
}

// argoChartResources selects identities from the same chart and values Argo CD
// renders. Earlier checkpoints need operator RBAC and CRDs as well as Deployments;
// the final checkpoint only removes migration flags from the Deployments.
func (f *e2eFixture) argoChartResources(version string, values map[string]any, deploymentsOnly bool) []any {
	f.t.Helper()
	root := strings.TrimSpace(f.cmd("git", nil, "rev-parse", "--show-toplevel"))
	chart := filepath.Join(root, "charts/openbao-operator")
	if version == e2eSourceRelease {
		chart = f.releaseChart(e2eSourceRelease)
	}
	path := filepath.Join(f.dir, "shared-argo-values.json")
	require.NoError(f.t, writePrivateJSON(path, values))
	rendered := f.cmd("helm", nil, "template", e2eRelease, chart, "--namespace", e2eOperatorNS,
		"--include-crds", "--values", path)
	decoder := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(rendered), 4096)
	selected := []any{}
	for {
		object := &unstructured.Unstructured{}
		err := decoder.Decode(&object.Object)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(f.t, err)
		if object.GetKind() == "" || (deploymentsOnly && object.GetKind() != "Deployment") {
			continue
		}
		group := ""
		if prefix, _, found := strings.Cut(object.GetAPIVersion(), "/"); found {
			group = prefix
		}
		selected = append(selected, map[string]any{"group": group, "kind": object.GetKind(),
			"namespace": object.GetNamespace(), "name": object.GetName()})
	}
	require.NotEmpty(f.t, selected)
	return selected
}

func (f *e2eFixture) argoSharedProcedure() {
	f.t.Helper()
	for _, app := range []string{e2ePlatformApp, e2eOperatorApp} {
		f.argoHealthy(app)
	}
	f.assertSharedTracking(primitivesSourceGroup)
	f.planRefused("refused-active", "syncs automatically")
	f.argoAutomated(e2eOperatorApp, false)
	f.eventually("the parent to restore its shared child's automatic sync", 5*time.Minute, func() bool {
		return f.argoHasAutomated(e2eOperatorApp)
	})
	f.planRefused("refused-parent", "syncs automatically")
	f.argoAutomated(e2ePlatformApp, false)
	f.argoAutomated(e2eOperatorApp, false)

	state := f.newState("rollback")
	f.plan(state)
	f.migrate(state)
	f.procedure("rollback", state)
	f.assertSurvivor(primitivesSourceGroup, "rollback")
	// Restore Git's source specification before the next plan: assertSurvivor
	// changed it to prove the source reconciles after rollback.
	f.argoSharedSync(stateSource.name, true)
	f.waitSettled()
	f.assertData(f.written...)

	state = f.newState("migrate")
	sourceUID := f.cluster(primitivesSourceGroup).GetUID()
	f.plan(state)
	f.migrate(state)
	clusterUID, claimUID := f.migratedIdentity()
	err := f.run("commit", state)
	var checkpoint *checkpointError
	require.True(f.t, errors.As(err, &checkpoint), "commit must stop at its normal operator checkpoint: %v", err)
	require.Equal(f.t, stateNormal.name, checkpoint.State)
	require.Nil(f.t, f.cluster(primitivesSourceGroup), "the planned source must already be retired")

	// A no-prune whole-app sync still creates missing old-group CRs. Final
	// verification must reject the new UID even though the name was planned.
	f.argoSharedSync(stateNormal.name, true)
	replacement := f.cluster(primitivesSourceGroup)
	require.NotNil(f.t, replacement, "the negative rehearsal must recreate the old-group CR")
	require.NotEqual(f.t, sourceUID, replacement.GetUID())
	require.Empty(f.t, replacement.GetFinalizers(), "the replacement must remain unmanaged by the source operator")
	err = f.run("commit", state)
	require.ErrorContains(f.t, err, "final-verify")
	require.ErrorContains(f.t, err, "was replaced since planning")
	require.Equal(f.t, clusterUID, string(f.cluster("kubebao.org").GetUID()))
	f.assertData(f.written...)

	// Remove only the test-created replacement, with Orphan propagation.
	// Keep the destination and persisted plan/progress throughout recovery.
	require.Equal(f.t, replacement.GetUID(), f.cluster(primitivesSourceGroup).GetUID())
	f.kubectl(nil, "-n", e2eDataNS, "delete", "openbaoclusters."+primitivesSourceGroup, e2eCluster,
		"--cascade=orphan", "--wait=true", "--timeout=30s")
	f.argoSharedSync(stateNormal.name, false)
	f.procedure("commit", state)
	f.assertSurvivor("kubebao.org", "commit")

	f.pushManifests(e2ePlatformApp, "v2", f.argoPlatform(e2eDestinationChart,
		f.chartValues(f.images[e2eDestination]), e2eDestination))
	f.kubectl(nil, "-n", e2eArgoNS, "patch", "application", e2ePlatformApp, "--type", "json",
		"-p", `[{"op":"replace","path":"/spec/source/targetRevision","value":"v2"},`+
			`{"op":"add","path":"/spec/syncPolicy/automated","value":{"prune":true,"selfHeal":true}}]`)
	f.waitFor("the parent to advance the shared resource source", 10*time.Minute, e2eDestination,
		"-n", e2eArgoNS, "get", "application", e2eOperatorApp, "-o", "jsonpath={.spec.sources[1].targetRevision}")
	for _, app := range []string{e2ePlatformApp, e2eOperatorApp} {
		f.argoHealthy(app)
	}
	require.NotEmpty(f.t, f.kubectl(nil, "get", "crd", "openbaoclusters."+primitivesSourceGroup, "-o", "name"),
		"Argo CD must keep the chart's CRDs")
	f.assertResumed(clusterUID, claimUID)
	f.assertSharedTracking("kubebao.org")
}

func (f *e2eFixture) assertSharedTracking(group string) {
	f.t.Helper()
	cluster := f.cluster(group)
	require.NotNil(f.t, cluster)
	if f.shape.gitops.kind == gitopsArgoCD {
		tracking := cluster.GetAnnotations()[argoTrackingID]
		if tracking != "" {
			require.Equal(f.t, e2eOperatorApp+":"+group+"/OpenBaoCluster:"+e2eDataNS+"/"+e2eCluster, tracking)
		} else {
			require.Equal(f.t, e2eOperatorApp, cluster.GetLabels()["app.kubernetes.io/instance"])
		}
		return
	}
	require.Equal(f.t, e2eOperatorApp, cluster.GetLabels()["kustomize.toolkit.fluxcd.io/name"])
	require.Equal(f.t, e2eFluxNS, cluster.GetLabels()["kustomize.toolkit.fluxcd.io/namespace"])
}
