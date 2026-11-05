//go:build apimigration_e2e

package main

// End-to-end qualification on a disposable Kind cluster. It installs the
// released 0.6.1 chart, creates an initialized Target-mode Cluster, and runs
// the procedure in-process against a destination operator image:
//
//	make test-e2e-api-migration API_MIGRATION_E2E_IMAGE=api-migration.local/operator:dev API_MIGRATION_E2E_CASE=commit
//
// It reads the released charts and Kustomize bases from the release tags.
// Cases: commit, rollback, and interrupt (stop after every migrate step, roll
// back, then resume a stopped migration and commit), then one per supported
// shape (see e2eShape). Only the cluster the test creates is deleted; -keep
// retains it after a failure for inspection.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
	"sigs.k8s.io/yaml"
)

var (
	e2eNodeImageFlag          = flag.String("node-image", e2eNodeImage, "Kind node image for migration qualification")
	e2eDestinationBackupImage = flag.String("destination-backup-image", "", "destination backup/restore helper image")
	e2eDestinationImage       = flag.String("destination-image", "", "locally built destination operator image")
	e2eArtifacts              = flag.String("artifacts-dir", "", "directory for availability observations; excludes credentials and migration state")
	e2eKeep                   = flag.Bool("keep", false, "keep the Kind cluster after a failure")
)

const (
	e2eNodeImage          = "kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5"
	e2eSourceImage        = "ghcr.io/kubebao/openbao-operator@sha256:8a73e5a7c0f13d6786e7ca6d65af20cb36620d4464dc07a6e8210e59d17bcb97"
	e2eInitImage          = "ghcr.io/kubebao/openbao-init@sha256:190fde3ba6c4b8e48ed39f82fde61273953f30c4fd79784e0f59802f06314ed9"
	e2eOpenBaoImage       = "openbao/openbao:2.6.5"
	e2eOpenBaoVer         = "2.6.5"
	e2eSourceRelease      = "0.6.1"
	e2eDestinationRelease = "0.7.0"
	// The Kustomize base pins its policies to the default operator namespace.
	e2eOperatorNS  = "openbao-operator-system"
	e2eDataNS      = "api-migration-data"
	e2eSelfNS      = "api-migration-self"
	e2eRelease     = "openbao-operator"
	e2eCluster     = "bao"
	e2eIdle        = "idle"
	e2eAdmin       = "fixture-administrator"
	e2eSourceValue = "source"
	e2eProofKey    = "migration.kubebao.org/reconcile-proof"
)

// e2eShape selects the source installation a case starts from.
type e2eShape struct {
	replicas       int
	readReplicas   int
	legacyRestore  bool
	backups        bool
	openBaoVersion string
	blueGreen      bool
	// restores runs an in-place restore, resumed by the administrator, before
	// the migration, so a finished restore record and its Job must move.
	restores         bool
	restoreLifecycle bool
	// tenants installs in multi-tenant mode, with a central Tenant for the
	// Cluster's namespace and a self-service Tenant without a Cluster.
	tenants bool
	// install selects a manifest installation: "chart" renders the chart and
	// applies it without a Helm release, as Argo CD does; "kustomize" builds
	// config/default. The procedure then stops at checkpoints.
	install string
	// legacy creates the Cluster on the previous stable release (implicit Shared JWT).
	legacy bool
	// paused pauses the running Cluster and adds one paused before it
	// initialized. Both move paused and are resumed after commit.
	paused bool
	// hardened adds a Hardened Cluster with ACME TLS and Transit unseal.
	hardened bool
	// recovery adds a second Cluster and interrupts commit and rollback.
	recovery bool
	// gitops installs the operator and the Cluster through a live GitOps
	// controller (see e2e_gitops_test.go).
	gitops gitopsController
	// sharedGitOps puts the operator and OpenBao resources in one child.
	sharedGitOps bool
	// explicitSuspend keeps spec.suspend:false in the parent's HelmRelease.
	explicitSuspend bool
	// auditMode selects managed or externally owned shared audit storage.
	auditMode string
	// seal selects the plugin-backed unseal fixture.
	seal string
	// rehearse supplies additional assertions for storage and seal fixtures.
	rehearse func(*e2eFixture)
}

const (
	// e2eDestination names the destination side: its image key, Git tag, and data marker.
	e2eDestination     = "destination"
	e2eConditionTrue   = "True"
	e2eLegacyRelease   = "0.5.3"
	e2eLegacyImage     = "ghcr.io/kubebao/openbao-operator:0.5.3"
	e2eLegacyInitImage = "ghcr.io/kubebao/openbao-init@sha256:9298ef4eceaec7ae79de2d7429261b5d60c1488319033f76bbdb48995bdf10c7"
	e2eBackupImage     = "ghcr.io/kubebao/openbao-backup@sha256:2f7bae8d727e8049f0aa3ed923174aaa8a43294f5db80bab1198c0cc2a73e906"
	e2eStorageNS       = "api-migration-storage"
	e2eStorageBucket   = "openbao-backups"
	e2eStorageKey      = "rustfsadmin"
)

type e2eFixture struct {
	t          *testing.T
	shape      e2eShape
	name       string
	dir        string
	kubeconfig string
	context    string
	images     map[string]string
	port       int
	bao        *OpenBao
	cancel     context.CancelFunc
	forwarding sync.WaitGroup
	written    []string
	// steps, while set, records when each procedure step starts.
	steps *stepClock
	// registryAddr forwards to the GitOps cases' registry.
	registryAddr       string
	voterSet           string
	hardenedSource     map[string]any
	destinationChart   string
	destinationVersion string
}

func TestAPIMigrationE2E(t *testing.T) {
	if *e2eDestinationImage == "" {
		t.Skip("-destination-image is required")
	}
	shapes := map[string]e2eShape{
		"ha":             {replicas: 3, readReplicas: 1, legacyRestore: true},
		"backups":        {replicas: 1, backups: true},
		"restores":       {replicas: 1, backups: true, restores: true},
		"tenants":        {replicas: 1, tenants: true},
		"chart":          {replicas: 1, install: flavourChart},
		"kustomize":      {replicas: 1, install: flavourKustomize, tenants: true},
		"legacy":         {replicas: 1, legacy: true},
		"paused":         {replicas: 1, paused: true},
		"hardened":       {replicas: 1, hardened: true},
		"recovery":       {replicas: 1, recovery: true},
		"cli":            {replicas: 1, recovery: true},
		"operator-ha":    {replicas: 1},
		"helm-csa":       {replicas: 1},
		"bluegreen":      {replicas: 3, blueGreen: true},
		"partitioned":    {replicas: 3, blueGreen: true},
		"openbao-2.7":    {replicas: 3, readReplicas: 1, openBaoVersion: "2.7.2"},
		"audit-managed":  {replicas: 3, readReplicas: 1, auditMode: "ManagedPVC", rehearse: (*e2eFixture).migrateAudit},
		"audit-existing": {replicas: 3, readReplicas: 1, auditMode: "ExistingPVC", rehearse: (*e2eFixture).migrateAudit},
		e2eSealPKCS11:    {replicas: 1, seal: e2eSealPKCS11, rehearse: (*e2eFixture).migrateUnseal},
		e2eSealKMIP:      {replicas: 1, seal: e2eSealKMIP, rehearse: (*e2eFixture).migrateUnseal},
	}
	names := make([]string, 0, 13+len(e2eGitOpsControllers))
	names = append(names, "commit", "rollback", "interrupt", "ha", "backups", "restores", "tenants", "chart",
		"kustomize", "legacy", "paused", "hardened", "recovery", "bluegreen", "partitioned", "openbao-2.7", "cli", "operator-ha", "helm-csa")
	names = append(names, "audit-managed", "audit-existing", e2eSealPKCS11, e2eSealKMIP)
	shapes["cli-manifests"] = e2eShape{replicas: 1, install: flavourChart}
	names = append(names, "cli-manifests")
	shapes["restore-lifecycle"] = e2eShape{replicas: 1, backups: true, openBaoVersion: "2.7.2", restoreLifecycle: true}
	names = append(names, "restore-lifecycle")
	shapes["flux-2.10-hardened"] = e2eShape{replicas: 1, tenants: true, hardened: true,
		install: flavourKustomize, gitops: e2eGitOpsControllers["flux-2.10"]}
	names = append(names, "flux-2.10-hardened")
	shapes["flux-2.10-helmrelease-hardened"] = e2eShape{replicas: 1, hardened: true,
		gitops: e2eGitOpsControllers["flux-2.10-helmrelease"]}
	shapes["argocd-3.6-hardened"] = e2eShape{replicas: 1, hardened: true,
		install: flavourChart, gitops: e2eGitOpsControllers["argocd-3.6"]}
	names = append(names, "flux-2.10-helmrelease-hardened", "argocd-3.6-hardened")
	shapes["argocd-3.6-shared"] = e2eShape{replicas: 1, install: flavourChart, sharedGitOps: true,
		gitops: e2eGitOpsControllers["argocd-3.6"]}
	shapes["flux-2.10-shared"] = e2eShape{replicas: 1, install: flavourKustomize, tenants: true, sharedGitOps: true,
		gitops: e2eGitOpsControllers["flux-2.10"]}
	shapes["flux-2.10-helmrelease-explicit-suspend"] = e2eShape{replicas: 1, explicitSuspend: true,
		gitops: e2eGitOpsControllers["flux-2.10-helmrelease"]}
	names = append(names, "argocd-3.6-shared", "flux-2.10-shared", "flux-2.10-helmrelease-explicit-suspend")
	for _, name := range slices.Sorted(maps.Keys(e2eGitOpsControllers)) {
		controller := e2eGitOpsControllers[name]
		shape := e2eShape{replicas: 1, gitops: controller}
		switch controller.kind {
		case gitopsArgoCD:
			// Argo CD renders the chart without a Helm release.
			shape.install = flavourChart
		case gitopsFlux:
			// A Kustomization applies the Kustomize build, multi-tenant.
			shape.install, shape.tenants = flavourKustomize, true
		case gitopsFluxHelm:
			// A HelmRelease installs the chart as a Helm release, single-tenant.
		}
		shapes[name] = shape
		names = append(names, name)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if caught := recover(); caught != nil {
					t.Error("migration scenario panicked; preserve failure diagnostics")
					panic(caught)
				}
			}()
			shape, ok := shapes[name]
			if !ok {
				shape = e2eShape{replicas: 1}
			}
			f := newE2EFixture(t, name, shape)
			switch name {
			case "commit", "ha", "legacy":
				f.migrateAndCommit(f.newState("migrate"))
			case "chart", "kustomize", "openbao-2.7":
				// Every operator state is a checkpoint: stopped, held, and normal
				// forward, and the source state on rollback.
				state := f.newState("rollback")
				f.plan(state)
				f.migrate(state)
				f.procedure("rollback", state)
				f.assertSurvivor(primitivesSourceGroup, "rollback")
				f.migrateAndCommit(f.newState("migrate"))
			case "backups":
				before := f.backup(primitivesSourceGroup, 0)
				f.migrateAndCommit(f.newState("migrate"))
				f.backup("kubebao.org", before)
			case "restores":
				before := f.backup(primitivesSourceGroup, 0)
				record := f.restoreInPlace()
				state := f.newState("rollback")
				f.plan(state)
				f.migrate(state)
				f.assertRestoreRecord("kubebao.org", record, false)
				f.procedure("rollback", state)
				f.assertRestoreRecord(primitivesSourceGroup, record, true)
				f.migrateAndCommit(f.newState("migrate"))
				f.assertRestoreRecord("kubebao.org", record, true)
				f.retireRestoreRecord(record)
				// An unreleased restore blocks backups; the retired record no longer does.
				f.backup("kubebao.org", before)
			case "tenants":
				state := f.newState("rollback")
				f.plan(state)
				f.migrate(state)
				f.assertTenants("kubebao.org", false)
				f.procedure("rollback", state)
				f.assertTenants(primitivesSourceGroup, true)
				f.migrateAndCommit(f.newState("migrate"))
				f.assertTenants("kubebao.org", true)
			case "recovery":
				f.recover()
			case "cli":
				f.rehearseReleaseCLI()
			case "cli-manifests":
				f.rehearseManifestCLI()
			case "operator-ha":
				f.migrateControllerHA()
			case "helm-csa":
				f.migrateClientSideHelm()
			case "restore-lifecycle":
				f.migrateRestoreLifecycle()
			case "audit-managed", "audit-existing", e2eSealPKCS11, e2eSealKMIP:
				shape.rehearse(f)
			case "bluegreen", "partitioned":
				f.migrateBlueGreen(name == "partitioned")
			case "flux-2.10-hardened", "flux-2.10-helmrelease-hardened", "argocd-3.6-hardened":
				f.createHardenedCluster()
				f.gitopsProcedure()
				f.assertHardened("kubebao.org", true)
				if f.shape.tenants {
					f.assertTenants("kubebao.org", true)
				}
			case "argocd-3.4", "argocd-3.5", "argocd-3.6", "flux-2.9", "flux-2.10", "flux-2.9-helmrelease",
				"flux-2.10-helmrelease", "argocd-3.6-shared", "flux-2.10-shared", "flux-2.10-helmrelease-explicit-suspend":
				f.gitopsProcedure()
			case "hardened":
				// Two Clusters in one installation: the Development one and a
				// Hardened one with ACME TLS and Transit unseal.
				f.createHardenedCluster()
				state := f.newState("rollback")
				f.plan(state)
				f.migrate(state)
				f.assertHardened("kubebao.org", false)
				f.procedure("rollback", state)
				f.assertHardened(primitivesSourceGroup, true)
				f.assertSurvivor(primitivesSourceGroup, "rollback")
				f.migrateAndCommit(f.newState("migrate"))
				f.assertHardened("kubebao.org", true)
				f.assertReconciles("kubebao.org", e2eHardened, "commit")
			case "paused":
				pod := f.pauseSource()
				state := f.newState("rollback")
				f.plan(state)
				f.migrate(state)
				f.assertPaused("kubebao.org", pod, false)
				f.procedure("rollback", state)
				f.assertPaused(primitivesSourceGroup, pod, true)
				state = f.newState("migrate")
				f.plan(state)
				f.migrate(state)
				f.procedure("commit", state)
				f.assertPaused("kubebao.org", pod, true)
				f.resume("kubebao.org")
				f.assertSurvivor("kubebao.org", "commit")
			case "rollback":
				state := f.newState("migrate")
				f.plan(state)
				f.migrate(state)
				f.assertData("before")
				f.writeData("during", e2eDestination)
				f.procedure("rollback", state)
				f.assertSurvivor(primitivesSourceGroup, "rollback")
			case "interrupt":
				for _, step := range []string{"preflight", "migrator", "stop-operators", "recheck", "apply-crds", "install-destination",
					"open-handoff", "import", "transfer", "statefulsets", "close-handoff", "start-destination"} {
					state := f.newState("stop-" + step)
					f.plan(state)
					require.ErrorIs(t, f.run("migrate", state, "--stop-after", step), errStop)
					f.procedure("rollback", state)
					f.assertSurvivor(primitivesSourceGroup, "rollback-"+step)
				}
				// Interrupt between a StatefulSet's orphan delete and its recreation,
				// where its Pods have no controller, then roll back.
				state := f.newState("stop-orphaned")
				f.plan(state)
				f.migrateUntilOrphaned(state)
				f.procedure("rollback", state)
				f.assertSurvivor(primitivesSourceGroup, "rollback-orphaned")
				// Interrupt there again, then resume and commit.
				state = f.newState("resume")
				f.plan(state)
				f.migrateUntilOrphaned(state)
				f.migrateAndCommit(state)
			}
		})
	}
}

const (
	primitivesSourceGroup = "openbao.org"
	e2eDestinationGroup   = "kubebao.org"
)

func (f *e2eFixture) migrateAndCommit(state string) {
	if _, err := os.Stat(filepath.Join(state, planFile)); err != nil {
		// A rollback's checks change the source, which can roll it: plan
		// requires it ready again.
		f.waitSettled()
		f.plan(state)
	}
	f.migrate(state)
	f.assertData("before")
	f.writeData("during", e2eDestination)
	f.procedure("commit", state)
	f.assertSurvivor("kubebao.org", "commit")
}

func newE2EFixture(t *testing.T, label string, shape e2eShape) *e2eFixture {
	t.Helper()
	// Kind appends -control-plane to the cluster name for the node hostname.
	label = label[:min(len(label), 24)]
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	// A kept cluster needs its evidence after the test, so the directory outlives t.TempDir.
	dir, err := os.MkdirTemp("", "api-migration-e2e-")
	require.NoError(t, err)
	f := &e2eFixture{t: t, shape: shape, name: "api-migration-" + label + "-" + hex.EncodeToString(suffix), dir: dir,
		images: map[string]string{}}
	require.NoError(t, os.Chmod(f.dir, 0o700))
	f.kubeconfig = filepath.Join(f.dir, "kubeconfig")
	f.context = "kind-" + f.name
	t.Cleanup(f.cleanup)
	f.createCluster()
	f.loadImages()
	if f.shape.gitops.kind != "" {
		f.installGitOps()
	} else {
		f.installSource()
		if f.shape.tenants {
			f.createTenants()
		}
		if f.shape.backups {
			f.deployStorage()
		}
		f.prepareAudit()
		f.prepareUnseal()
		f.createSourceCluster()
	}
	if f.shape.legacy {
		f.upgradeSource()
	}
	f.voterSet = e2eCluster
	if revision, _, _ := unstructured.NestedString(f.cluster(primitivesSourceGroup).Object, "status", "blueGreen", "blueRevision"); revision != "" {
		f.voterSet += "-" + revision
	}
	f.startOpenBaoAccess()
	f.writeData("before", e2eSourceValue)
	return f
}

func (f *e2eFixture) cleanup() {
	if f.cancel != nil {
		f.cancel()
		f.forwarding.Wait()
	}
	if f.t.Failed() && *e2eKeep {
		f.t.Logf("kept Kind cluster %s and evidence in %s", f.name, f.dir)
		return
	}
	defer func() { _ = os.RemoveAll(f.dir) }()
	// Delete only the cluster this test created.
	if out, err := exec.Command("kind", "delete", "cluster", "--name", f.name, "--kubeconfig", f.kubeconfig).CombinedOutput(); err != nil {
		f.t.Logf("delete Kind cluster %s: %v: %s", f.name, err, out)
	}
}

func (f *e2eFixture) cmd(name string, input []byte, args ...string) string {
	f.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Stdin = bytes.NewReader(input)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(f.t, cmd.Run(), "%s %s\n%s", name, strings.Join(args, " "), out.String())
	return out.String()
}

func (f *e2eFixture) kubectl(input []byte, args ...string) string {
	f.t.Helper()
	return f.cmd("kubectl", input, append([]string{"--kubeconfig", f.kubeconfig, "--context", f.context}, args...)...)
}

func (f *e2eFixture) getJSON(args ...string) map[string]any {
	f.t.Helper()
	value := map[string]any{}
	require.NoError(f.t, json.Unmarshal([]byte(f.kubectl(nil, append(args, "-o", "json")...)), &value))
	return value
}

func (f *e2eFixture) eventually(what string, timeout time.Duration, check func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(timeout)
	for !check() {
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Second)
	}
}

func (f *e2eFixture) createCluster() {
	f.cmd("kind", nil, "create", "cluster", "--name", f.name, "--image", *e2eNodeImageFlag,
		"--kubeconfig", f.kubeconfig, "--wait", "120s")
}

// loadImages tags each pinned image locally; kind cannot load digest-only references.
func (f *e2eFixture) loadImages() {
	images := map[string]string{"source": e2eSourceImage, "init": e2eInitImage,
		"openbao": e2eOpenBaoImage, e2eDestination: *e2eDestinationImage}
	if f.shape.openBaoVersion != "" {
		images["openbao"] = "openbao/openbao:" + f.shape.openBaoVersion
	}
	switch f.shape.seal {
	case e2eSealPKCS11:
		require.NotEmpty(f.t, *e2eSoftHSMImage, "-softhsm-image is required for pkcs11")
		images["openbao"] = *e2eSoftHSMImage
	case e2eSealKMIP:
		require.NotEmpty(f.t, *e2eKMIPImage, "-kmip-image is required for kmip")
		images["kmip"] = *e2eKMIPImage
	}
	if f.shape.backups {
		images["backup"], images["rustfs"] = e2eBackupImage, e2ehelpers.DefaultRustFSImage
		require.NotEmpty(f.t, *e2eDestinationBackupImage, "-destination-backup-image is required for backup and restore cases")
		images["destination-backup"] = *e2eDestinationBackupImage
	}
	if f.shape.legacy {
		images["legacy-source"], images["legacy-init"] = e2eLegacyImage, e2eLegacyInitImage
	}
	if f.shape.hardened {
		// The Hardened Cluster has the kubelet pull the published OpenBao and
		// init images. A kubelet that recorded such a pull no longer treats a
		// local alias of the same image as preloaded: it tries to pull the
		// alias when a Pod starts. So every Pod uses the published references.
		delete(images, "openbao")
		delete(images, "init")
		f.images["openbao"], f.images["init"] = e2eHardenedImage, e2eInitImage
	}
	if f.shape.gitops.kind != "" {
		images["registry"] = e2eRegistryImage
	}
	for key, image := range images {
		// A fresh runner has none of the images yet.
		if exec.Command("docker", "image", "inspect", image).Run() != nil {
			f.cmd("docker", nil, "pull", "--quiet", image)
		}
		local := "api-migration-e2e.local/" + key + ":" + f.name
		f.cmd("docker", nil, "tag", image, local)
		// A containerd image store can retain index entries for platforms whose
		// blobs were not pulled. Export only the native Linux node's platform.
		archive := filepath.Join(f.dir, key+"-image.tar")
		f.cmd("docker", nil, "image", "save", "--platform", "linux/"+runtime.GOARCH, "--output", archive, local)
		f.cmd("kind", nil, "load", "image-archive", archive, "--name", f.name)
		require.NoError(f.t, os.Remove(archive))
		f.images[key] = local
	}
}

// installSource installs the nominated source release. A legacy shape starts
// on the previous stable release, so its Cluster keeps the implicit Shared JWT
// mode, and upgrades it in place to the source release after creation.
func (f *e2eFixture) installSource() {
	f.kubectl([]byte(fmt.Sprintf(e2eIdentities, e2eOperatorNS, e2eDataNS, e2eAdmin)), "apply", "-f", "-")
	release, operator, init := e2eSourceRelease, "source", "init"
	if f.shape.legacy {
		release, operator, init = e2eLegacyRelease, "legacy-source", "legacy-init"
	}
	switch f.shape.install {
	case flavourChart, flavourKustomize:
		f.applyOperatorState(stateSource.name)
	default:
		f.helmRelease("install", release, operator, init)
	}
}

// applyOperatorState applies a manifest installation's state, as an
// administrator does at a checkpoint: the source release, or the destination
// with the overrides or Kustomize components the checkpoint names.
func (f *e2eFixture) applyOperatorState(name string) {
	f.t.Helper()
	if f.shape.gitops.kind == gitopsArgoCD {
		f.argoSync(name)
		return
	}
	source := name == stateSource.name
	release, image := e2eDestination, f.images[e2eDestination]
	if source {
		release, image = e2eSourceRelease, f.images["source"]
	}
	var rendered string
	switch f.shape.install {
	case flavourChart:
		chart := filepath.Join(strings.TrimSpace(f.cmd("git", nil, "rev-parse", "--show-toplevel")), "charts/openbao-operator")
		if source {
			chart = f.releaseChart(e2eSourceRelease)
		} else if f.destinationChart != "" {
			chart = f.destinationChart
		}
		values := f.chartValues(image)
		if !source && f.destinationVersion != "" {
			values["operatorVersion"] = f.destinationVersion
		}
		if overrides := f.checkpointValues(name); overrides != nil {
			values = mergeValues(values, overrides)
		}
		path := filepath.Join(f.dir, "values-"+name+".json")
		require.NoError(f.t, writePrivateJSON(path, values))
		rendered = f.cmd("helm", nil, "template", e2eRelease, chart, "--namespace", e2eOperatorNS, "--include-crds",
			"--values", path)
	case flavourKustomize:
		rendered = f.kustomizeBuild(release, image, map[string][]string{
			stateStopped.name: {"api-migration-stopped"}, stateHeld.name: {"api-migration-hold"}}[name])
	}
	f.kubectl([]byte(rendered), "apply", "--server-side", "--force-conflicts", "-f", "-")
	if name != stateStopped.name {
		f.kubectl(nil, "-n", e2eOperatorNS, "wait", "--for=condition=Available", "deployment", "--all", "--timeout=300s")
	}
}

// checkpointValues reads the overrides the last checkpoint wrote, if any.
func (f *e2eFixture) checkpointValues(name string) map[string]any {
	matches, _ := filepath.Glob(filepath.Join(f.dir, "state-*", "checkpoints", name+"-values.yaml"))
	if len(matches) == 0 {
		return nil
	}
	slices.SortFunc(matches, func(a, b string) int {
		ai, _ := os.Stat(a)
		bi, _ := os.Stat(b)
		return ai.ModTime().Compare(bi.ModTime())
	})
	data, err := os.ReadFile(matches[len(matches)-1])
	require.NoError(f.t, err)
	values := map[string]any{}
	require.NoError(f.t, yaml.Unmarshal(data, &values))
	return values
}

// kustomizeBuild builds config/default of the source release, or of this
// tree for the destination, with the e2e images and the given components.
func (f *e2eFixture) kustomizeBuild(release, image string, components []string) string {
	f.t.Helper()
	root := strings.TrimSpace(f.cmd("git", nil, "rev-parse", "--show-toplevel"))
	config := filepath.Join(root, "config")
	if release == e2eSourceRelease {
		dir := filepath.Join(f.dir, "config-"+release)
		if _, err := os.Stat(dir); err != nil {
			require.NoError(f.t, os.Mkdir(dir, 0o700))
			archive, err := exec.Command("git", "-C", root, "archive", release, "config").Output()
			require.NoError(f.t, err)
			f.cmd("tar", archive, "-x", "-C", dir)
		}
		config = filepath.Join(dir, "config")
	}
	build, err := os.MkdirTemp(f.dir, "kustomize-")
	require.NoError(f.t, err)
	relative := func(path string) string {
		rel, err := relativeResolved(build, path)
		require.NoError(f.t, err)
		return rel
	}
	repository, tag, _ := strings.Cut(image, ":")
	content := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - " +
		relative(filepath.Join(config, "default")) + "\n"
	if len(components) > 0 {
		content += "components:\n"
		for _, component := range components {
			content += "  - " + relative(filepath.Join(root, "config", "components", component)) + "\n"
		}
	}
	// config/manager names the image; a release overlay may already have renamed it.
	content += "images:\n"
	for _, name := range []string{"controller", "example.com/openbao-operator"} {
		content += "  - name: " + name + "\n    newName: " + repository + "\n    newTag: " + tag + "\n"
	}
	operatorVersion := e2eDestinationRelease
	if release == e2eSourceRelease {
		operatorVersion = e2eSourceRelease
	}
	// The published install.yaml carries the release version; config/default does not.
	content += "patches:\n"
	for _, component := range []string{"controller", "provisioner"} {
		content += "  - patch: |-\n      apiVersion: apps/v1\n      kind: Deployment\n      metadata: {name: openbao-operator-" +
			component + ", namespace: " + e2eOperatorNS + "}\n      spec: {template: {spec: {containers: [{name: manager, env: [{name: OPERATOR_VERSION, value: \"" +
			operatorVersion + "\"}]}]}}}\n"
	}
	require.NoError(f.t, os.WriteFile(filepath.Join(build, "kustomization.yaml"), []byte(content), 0o600))
	return f.cmd(filepath.Join(root, "bin", "kustomize"), nil, "build", build)
}

// upgradeSource moves a legacy installation to the nominated source release:
// CRDs first, because Helm does not upgrade them, then the chart.
func (f *e2eFixture) upgradeSource() {
	chart := f.releaseChart(e2eSourceRelease)
	f.kubectl(nil, "apply", "--server-side", "--force-conflicts", "-f", filepath.Join(chart, "crds"))
	f.helmRelease("upgrade", e2eSourceRelease, "source", "init")
	f.waitSettled()
}

func (f *e2eFixture) releaseChart(release string) string {
	dir := filepath.Join(f.dir, "chart-"+release)
	if _, err := os.Stat(dir); err == nil {
		return filepath.Join(dir, "charts/openbao-operator")
	}
	require.NoError(f.t, os.Mkdir(dir, 0o700))
	root := strings.TrimSpace(f.cmd("git", nil, "rev-parse", "--show-toplevel"))
	archive, err := exec.Command("git", "-C", root, "archive", release, "charts/openbao-operator").Output()
	require.NoError(f.t, err, "archive the %s chart", release)
	f.cmd("tar", archive, "-x", "-C", dir)
	return filepath.Join(dir, "charts/openbao-operator")
}

// chartValues are the values every e2e chart installation uses.
func (f *e2eFixture) chartValues(image string) map[string]any {
	repository, tag, _ := strings.Cut(image, ":")
	init := f.images["init"]
	if f.shape.legacy && image == f.images["legacy-source"] {
		init = f.images["legacy-init"]
	}
	operatorVersion := e2eSourceRelease
	if image == f.images[e2eDestination] {
		operatorVersion = e2eDestinationRelease
	}
	values := map[string]any{
		"image":           map[string]any{"repository": repository, "tag": tag, "digest": "", "pullPolicy": "IfNotPresent"},
		"operatorVersion": operatorVersion, "helperImages": map[string]any{"init": init},
		"tenancy":    map[string]any{"mode": "single", "targetNamespace": e2eDataNS},
		"controller": map[string]any{"replicas": 1}, "metrics": map[string]any{"enabled": false},
	}
	if f.shape.backups {
		helper := "backup"
		if image == f.images[e2eDestination] {
			helper = "destination-backup"
		}
		values["helperImages"].(map[string]any)["backup"] = f.images[helper]
	}
	if f.shape.tenants {
		values["tenancy"] = map[string]any{"mode": "multi"}
	}
	return values
}

func (f *e2eFixture) helmRelease(action, release, operator, init string) {
	values := f.chartValues(f.images[operator])
	values["operatorVersion"] = release
	values["helperImages"].(map[string]any)["init"] = f.images[init]
	path := filepath.Join(f.dir, "values-"+release+".json")
	require.NoError(f.t, writePrivateJSON(path, values))
	f.cmd("helm", nil, action, e2eRelease, f.releaseChart(release),
		"--kubeconfig", f.kubeconfig, "--kube-context", f.context, "--namespace", e2eOperatorNS,
		"--values", path, "--wait", "--timeout", "300s")
}

// e2eIdentities creates the namespaces, the migrator ServiceAccount with the
// handoff grants the procedure checks, and an OpenBao administrator identity.
const e2eIdentities = `
apiVersion: v1
kind: Namespace
metadata: {name: %[1]s}
---
apiVersion: v1
kind: Namespace
metadata: {name: %[2]s}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: %[3]s, namespace: %[2]s}
`

// apiServerAddresses are the Kubernetes API endpoints a Cluster's network
// policy must allow.
func (f *e2eFixture) apiServerAddresses() []string {
	f.t.Helper()
	endpointSlices := f.getJSON("get", "endpointslices", "-n", "default", "-l", "kubernetes.io/service-name=kubernetes")
	var addresses []string
	for _, item := range endpointSlices["items"].([]any) {
		for _, endpoint := range item.(map[string]any)["endpoints"].([]any) {
			for _, address := range endpoint.(map[string]any)["addresses"].([]any) {
				addresses = append(addresses, address.(string))
			}
		}
	}
	require.NotEmpty(f.t, addresses)
	return addresses
}

// sourceCluster is the source Cluster this shape starts from, as a manifest.
func (f *e2eFixture) sourceCluster() map[string]any {
	addresses := f.apiServerAddresses()
	cluster := map[string]any{"apiVersion": "openbao.org/v1alpha1", "kind": "OpenBaoCluster",
		"metadata": map[string]any{"name": e2eCluster, "namespace": e2eDataNS},
		"spec": map[string]any{
			"version": e2eOpenBaoVer, "image": f.images["openbao"], "replicas": f.shape.replicas, "profile": "Development",
			"deletionPolicy": "Retain",
			"initContainer":  map[string]any{"enabled": true, "image": f.images["init"]},
			"selfInit": map[string]any{"enabled": true, "oidc": map[string]any{"enabled": true}, "requests": []any{
				map[string]any{"name": "fixture-kv", "operation": "update", "path": "sys/mounts/migration-data",
					"secretEngine": map[string]any{"type": "kv", "options": map[string]any{"version": "2"}}},
				map[string]any{"name": "fixture-policy", "operation": "update", "path": "sys/policies/acl/" + e2eAdmin,
					"policy": map[string]any{"policy": `path "*" { capabilities = ["create", "read", "update", "delete", "list", "sudo"] }`}},
				map[string]any{"name": "fixture-role", "operation": "update", "path": "auth/jwt-operator/role/" + e2eAdmin,
					"data": map[string]any{"role_type": "jwt", "user_claim": "sub", "bound_audiences": []any{"openbao-internal"},
						"bound_subject":  "system:serviceaccount:" + e2eDataNS + ":" + e2eAdmin,
						"token_policies": []any{e2eAdmin}, "ttl": "4h"}},
			}},
			"tls":            map[string]any{"enabled": true, "mode": "OperatorManaged", "rotationPeriod": "720h"},
			"storage":        map[string]any{"size": "1Gi"},
			"serviceAccount": map[string]any{"annotations": map[string]any{e2eProofKey: "baseline"}},
			"network": map[string]any{"apiServerCIDR": "10.96.0.0/12", "apiServerEndpointIPs": addresses,
				// The availability probe calls the Cluster's Service during each migration.
				"trustedIngressPeers": []any{map[string]any{"podSelector": map[string]any{
					"matchLabels": map[string]any{"role": e2eProbeRole}}}}},
		}}
	if f.shape.openBaoVersion != "" {
		cluster["spec"].(map[string]any)["version"] = f.shape.openBaoVersion
	}
	if f.shape.blueGreen {
		cluster["spec"].(map[string]any)["upgrade"] = map[string]any{"strategy": "BlueGreen"}
	}
	if !f.shape.legacy {
		cluster["spec"].(map[string]any)["controllerJWTMode"] = "Target"
	}
	if f.shape.readReplicas > 0 {
		cluster["spec"].(map[string]any)["readReplicas"] = map[string]any{"replicas": f.shape.readReplicas}
	}
	if f.shape.backups {
		// A schedule far in the future: the test takes manual backups only.
		later := time.Now().UTC().Add(12 * time.Hour)
		target := e2eStorageTarget()
		target["pathPrefix"] = "clusters"
		cluster["spec"].(map[string]any)["backup"] = map[string]any{
			"schedule": fmt.Sprintf("%d %d * * *", later.Minute(), later.Hour()),
			"target":   target, "retention": map[string]any{"maxCount": 7},
		}
	}
	if f.shape.restores {
		// Self-initialization creates the restore executor's JWT role.
		cluster["spec"].(map[string]any)["restore"] = map[string]any{"jwtAuthRole": "restore"}
	}
	f.configureAudit(cluster["spec"].(map[string]any))
	f.configureUnseal(cluster["spec"].(map[string]any))
	return cluster
}

func (f *e2eFixture) createSourceCluster() {
	cluster := f.sourceCluster()
	data, _ := json.Marshal(cluster)
	f.kubectl(data, "apply", "-f", "-")
	f.eventually("the source Cluster to initialize", 10*time.Minute, func() bool {
		return clusterReady(f.cluster(primitivesSourceGroup))
	})
	if f.shape.legacyRestore {
		// Releases before 0.6.0 recorded restores on the Cluster; 0.6.0 keeps the field.
		f.kubectl(nil, "patch", "openbaoclusters."+primitivesSourceGroup, e2eCluster, "-n", e2eDataNS,
			"--subresource", "status", "--type", "merge",
			"-p", `{"status":{"restore":{"name":"restore-before-0-6","uid":"6f1c1f0e-legacy-restore"}}}`)
	}
	f.waitSettled()
}

func e2eStorageTarget() map[string]any {
	return map[string]any{"provider": "s3", "endpoint": "http://rustfs-svc." + e2eStorageNS + ".svc:9000",
		"bucket": e2eStorageBucket, "s3": map[string]any{"region": "us-east-1", "usePathStyle": true},
		"credentialsSecretRef": map[string]any{"name": "backup-credentials"}}
}

const e2eRestoreRecord = "restore-before-migration"

// restoreInPlace restores the Cluster from its last backup and resumes it as
// the administrator. The record stays Unknown with its Resume decision and
// keeps its executor Job: the common shape of restore history.
func (f *e2eFixture) restoreInPlace() string {
	f.t.Helper()
	key, _, _ := unstructured.NestedString(f.cluster(primitivesSourceGroup).Object, "status", "backup", "lastBackupName")
	require.NotEmpty(f.t, key)
	restore := map[string]any{"apiVersion": "openbao.org/v1alpha1", "kind": "OpenBaoRestore",
		"metadata": map[string]any{"name": e2eRestoreRecord, "namespace": e2eDataNS},
		"spec": map[string]any{"cluster": e2eCluster, "jwtAuthRole": "restore", "image": f.images["backup"],
			"source": map[string]any{"key": key, "target": e2eStorageTarget()}}}
	data, _ := json.Marshal(restore)
	f.kubectl(data, "create", "-f", "-")
	record := func() map[string]any {
		return f.getJSON("get", "openbaorestores."+primitivesSourceGroup, e2eRestoreRecord, "-n", e2eDataNS)
	}
	uid := record()["metadata"].(map[string]any)["uid"].(string)
	hold := func() string { return f.cluster(primitivesSourceGroup).GetAnnotations()["openbao.org/restore-hold"] }
	f.eventually("the restore to submit its snapshot and hold the Cluster", 15*time.Minute, func() bool {
		status, _ := record()["status"].(map[string]any)
		return status["phase"] == "Unknown" && status["submissionClaim"] != nil && hold() == uid
	})
	f.kubectl(nil, "annotate", "openbaorestores."+primitivesSourceGroup, e2eRestoreRecord, "-n", e2eDataNS,
		"openbao.org/restore-acknowledge="+uid+"/Resume")
	f.eventually("the administrator Resume to finish", 15*time.Minute, func() bool {
		status, _ := record()["status"].(map[string]any)
		restart, _ := status["restart"].(map[string]any)
		return status["administratorDisposition"] == "Resume" && restart["completedAt"] != nil && hold() == ""
	})
	f.waitSettled()
	f.assertData(f.written...)
	return e2eRestoreRecord
}

// assertRestoreRecord checks the record in group, with its recorded decision,
// and that it controls its executor Job. Once the other side is retired, no
// record may remain there.
func (f *e2eFixture) assertRestoreRecord(group, name string, retired bool) {
	f.t.Helper()
	if retired {
		other := map[string]string{"openbao.org": "kubebao.org", "kubebao.org": "openbao.org"}[group]
		out := f.kubectl(nil, "get", "openbaorestores."+other, "-n", e2eDataNS, "-o", "name")
		require.Empty(f.t, strings.TrimSpace(out), "no %s restore record may remain", other)
	}
	record := f.getJSON("get", "openbaorestores."+group, name, "-n", e2eDataNS)
	metadata := record["metadata"].(map[string]any)
	status := record["status"].(map[string]any)
	require.Equal(f.t, "Unknown", status["phase"])
	require.Equal(f.t, "Resume", status["administratorDisposition"])
	require.NotNil(f.t, status["restart"].(map[string]any)["completedAt"])
	if group == e2eDestinationGroup {
		annotations := metadata["annotations"].(map[string]any)
		require.NotEmpty(f.t, annotations["migration.kubebao.org/source-uid"])
		require.NotContains(f.t, annotations, "openbao.org/restore-acknowledge")
	}
	job := f.getJSON("get", "job", status["execution"].(map[string]any)["jobName"].(string), "-n", e2eDataNS)
	owners := job["metadata"].(map[string]any)["ownerReferences"].([]any)
	require.Len(f.t, owners, 1)
	owner := owners[0].(map[string]any)
	require.Equal(f.t, group+"/v1alpha1", owner["apiVersion"])
	require.Equal(f.t, metadata["uid"], owner["uid"])
}

// retireRestoreRecord shows that the destination controller manages the
// imported record: it adopts the record with its finalizer, and releases it on
// deletion, after which the executor Job is garbage-collected.
func (f *e2eFixture) retireRestoreRecord(name string) {
	f.t.Helper()
	get := func() string {
		out, _ := exec.Command("kubectl", "--kubeconfig", f.kubeconfig, "--context", f.context, "get",
			"openbaorestores.kubebao.org", name, "-n", e2eDataNS, "--ignore-not-found",
			"-o", "jsonpath={.metadata.finalizers}").CombinedOutput()
		return string(out)
	}
	f.eventually("the destination controller to adopt the restore record", 3*time.Minute, func() bool {
		return strings.Contains(get(), "openbao.org/openbaorestore-finalizer")
	})
	job := f.getJSON("get", "openbaorestores.kubebao.org", name, "-n", e2eDataNS)["status"].(map[string]any)["execution"].(map[string]any)["jobName"].(string)
	f.kubectl(nil, "delete", "openbaorestores.kubebao.org", name, "-n", e2eDataNS, "--wait=false")
	f.eventually("the restore record and its Job to be deleted", 3*time.Minute, func() bool {
		out, err := exec.Command("kubectl", "--kubeconfig", f.kubeconfig, "--context", f.context, "get", "job", job,
			"-n", e2eDataNS, "--ignore-not-found", "-o", "name").Output()
		return get() == "" && err == nil && strings.TrimSpace(string(out)) == ""
	})
}

// e2eTenants are a central Tenant, onboarding the Cluster's namespace from the
// operator namespace, and a self-service Tenant for a namespace without a Cluster.
var e2eTenants = []struct{ namespace, name, target string }{
	{e2eOperatorNS, "data", e2eDataNS}, {e2eSelfNS, "self", e2eSelfNS},
}

func (f *e2eFixture) createTenants() {
	f.t.Helper()
	f.kubectl([]byte("apiVersion: v1\nkind: Namespace\nmetadata: {name: "+e2eSelfNS+"}\n"), "apply", "-f", "-")
	for _, tenant := range e2eTenants {
		data, _ := json.Marshal(map[string]any{"apiVersion": "openbao.org/v1alpha1", "kind": "OpenBaoTenant",
			"metadata": map[string]any{"name": tenant.name, "namespace": tenant.namespace},
			"spec":     map[string]any{"targetNamespace": tenant.target}})
		f.kubectl(data, "apply", "-f", "-")
	}
	f.eventually("the Tenants to be provisioned", 3*time.Minute, func() bool {
		for _, tenant := range e2eTenants {
			out, err := exec.Command("kubectl", "--kubeconfig", f.kubeconfig, "--context", f.context, "get",
				"openbaotenants.openbao.org", tenant.name, "-n", tenant.namespace,
				"-o", "jsonpath={.status.provisioned}").Output()
			if err != nil || string(out) != "true" {
				return false
			}
		}
		return true
	})
}

// assertTenants checks each Tenant in group and that its namespace's RBAC was
// rendered for group. Once the other side is retired, no Tenant may remain there.
func (f *e2eFixture) assertTenants(group string, retired bool) {
	f.t.Helper()
	if retired {
		other := map[string]string{"openbao.org": "kubebao.org", "kubebao.org": "openbao.org"}[group]
		// Flux can prune the empty source CRDs after commit and GitOps resume.
		if crd := f.kubectl(nil, "get", "crd", "openbaotenants."+other, "--ignore-not-found", "-o", "name"); strings.TrimSpace(crd) != "" {
			out := f.kubectl(nil, "get", "openbaotenants."+other, "--all-namespaces", "-o", "name")
			require.Empty(f.t, strings.TrimSpace(out), "no %s Tenant may remain", other)
		}
	}
	for _, tenant := range e2eTenants {
		record := f.getJSON("get", "openbaotenants."+group, tenant.name, "-n", tenant.namespace)
		if group == e2eDestinationGroup {
			annotations, _ := record["metadata"].(map[string]any)["annotations"].(map[string]any)
			require.NotEmpty(f.t, annotations["migration.kubebao.org/source-uid"])
			f.eventually("the destination provisioner to provision "+tenant.name, time.Minute, func() bool {
				out, err := exec.Command("kubectl", "--kubeconfig", f.kubeconfig, "--context", f.context, "get",
					"openbaotenants.kubebao.org", tenant.name, "-n", tenant.namespace,
					"-o", "jsonpath={.status.provisioned}").Output()
				return err == nil && string(out) == "true"
			})
		}
		role := f.getJSON("get", "role", "openbao-operator-tenant-role", "-n", tenant.target)
		groups := []string{}
		for _, rule := range role["rules"].([]any) {
			for _, apiGroup := range rule.(map[string]any)["apiGroups"].([]any) {
				groups = append(groups, apiGroup.(string))
			}
		}
		require.Contains(f.t, groups, group, "the tenant Role in %s grants the active group", tenant.target)
		other := map[string]string{"openbao.org": "kubebao.org", "kubebao.org": "openbao.org"}[group]
		require.NotContains(f.t, groups, other)
	}
}

// deployStorage runs the repository's RustFS fixture as the S3 backup target.
func (f *e2eFixture) deployStorage() {
	f.t.Helper()
	config, err := clientcmd.BuildConfigFromFlags("", f.kubeconfig)
	require.NoError(f.t, err)
	c, err := client.New(config, client.Options{})
	require.NoError(f.t, err)
	f.kubectl([]byte("apiVersion: v1\nkind: Namespace\nmetadata: {name: "+e2eStorageNS+"}\n"), "apply", "-f", "-")
	storage := e2ehelpers.DefaultRustFSConfig()
	storage.Namespace, storage.Image = e2eStorageNS, f.images["rustfs"]
	storage.AccessKey, storage.SecretKey, storage.StorageSize = e2eStorageKey, e2eStorageKey, "1Gi"
	storage.Buckets = []string{e2eStorageBucket}
	require.NoError(f.t, e2ehelpers.EnsureRustFS(context.Background(), c, config, storage))
	// The object store is private in-cluster infrastructure. The operator's
	// default jobs policy allows public HTTPS, so the fixture must allow this
	// namespace and port before starting a backup or restore executor.
	policy, err := json.Marshal(map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "backup-storage-fixture", "namespace": e2eDataNS},
		"spec": map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"openbao.org/cluster": e2eCluster}},
			"policyTypes": []any{"Egress"}, "egress": []any{map[string]any{
				"to":    []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": e2eStorageNS}}}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": 9000}},
			}}}})
	require.NoError(f.t, err)
	f.kubectl(policy, "apply", "-f", "-")
	f.kubectl(nil, "create", "secret", "generic", "backup-credentials", "-n", e2eDataNS,
		"--from-literal=accessKeyId="+e2eStorageKey, "--from-literal=secretAccessKey="+e2eStorageKey)
}

// backup takes a manual backup on the Cluster in group and waits until its
// successful-backup count exceeds after. It returns the new count.
func (f *e2eFixture) backup(group string, after int64) int64 {
	f.t.Helper()
	f.kubectl(nil, "annotate", "openbaoclusters."+group, e2eCluster, "-n", e2eDataNS, "--overwrite",
		"openbao.org/trigger-backup="+time.Now().UTC().Format(time.RFC3339Nano))
	var count int64
	f.eventually("a successful backup through "+group, 8*time.Minute, func() bool {
		cluster := f.cluster(group)
		if cluster == nil {
			return false
		}
		count, _, _ = unstructured.NestedInt64(cluster.Object, "status", "backup", "successfulBackups")
		_, pending := cluster.GetAnnotations()["openbao.org/trigger-backup"]
		return count > after && !pending
	})
	// The finished Job must leave no running work behind for the migration preflight.
	f.eventually("backup Jobs to finish", 3*time.Minute, func() bool {
		out, err := exec.Command("kubectl", "--kubeconfig", f.kubeconfig, "--context", f.context, "get", "jobs",
			"-n", e2eDataNS, "-o", "jsonpath={range .items[*]}{.status.active}{end}").Output()
		return err == nil && strings.TrimSpace(string(out)) == ""
	})
	f.t.Logf("%s Cluster has %d successful backup(s)", group, count)
	return count
}

// waitSettled waits until the Cluster is available and its data Pods stop changing.
func (f *e2eFixture) waitSettled() {
	group := primitivesSourceGroup
	f.t.Helper()
	previous := ""
	stable := 0
	f.eventually("the source workload to settle", 10*time.Minute, func() bool {
		if !clusterReady(f.cluster(group)) {
			stable = 0
			return false
		}
		out, err := exec.Command("kubectl", "--kubeconfig", f.kubeconfig, "--context", f.context, "get", "pods",
			"-n", e2eDataNS, "-l", "openbao.org/cluster="+e2eCluster,
			"-o", "jsonpath={range .items[*]}{.metadata.uid}{.status.phase}{end}").Output()
		if err != nil || string(out) != previous {
			previous, stable = string(out), 0
			return false
		}
		stable++
		return stable >= 10
	})
}

func (f *e2eFixture) cluster(group string) *unstructured.Unstructured {
	return f.clusterNamed(group, e2eCluster)
}

func (f *e2eFixture) clusterNamed(group, name string) *unstructured.Unstructured {
	out, err := exec.Command("kubectl", "--kubeconfig", f.kubeconfig, "--context", f.context, "get",
		"openbaoclusters."+group, name, "-n", e2eDataNS, "-o", "json", "--ignore-not-found").Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return nil
	}
	// Unstructured decoding keeps integers as int64, as the procedure reads them.
	object := &unstructured.Unstructured{}
	if object.UnmarshalJSON(out) != nil {
		return nil
	}
	return object
}

// startOpenBaoAccess keeps a port-forward to bao-0 on one local port,
// restarting it if the Pod is replaced, and logs in as the administrator.
func (f *e2eFixture) startOpenBaoAccess() {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(f.t, err)
	f.port = listener.Addr().(*net.TCPAddr).Port
	require.NoError(f.t, listener.Close())
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.forwarding.Add(1)
	go func() {
		defer f.forwarding.Done()
		for ctx.Err() == nil {
			// kubectl can keep running after its Pod is replaced, so restart the
			// forward whenever the Pod UID changes.
			pod := f.podUID()
			forward, stop := context.WithCancel(ctx)
			cmd := exec.CommandContext(forward, "kubectl", "--kubeconfig", f.kubeconfig, "--context", f.context,
				"-n", e2eDataNS, "port-forward", "pod/"+f.voterSet+"-0", strconv.Itoa(f.port)+":8200", "--address", "127.0.0.1")
			go func() {
				for forward.Err() == nil {
					time.Sleep(2 * time.Second)
					if current := f.podUID(); current != "" && current != pod {
						stop()
					}
				}
			}()
			_ = cmd.Run()
			stop()
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
	secret := f.getJSON("get", "secret", e2eCluster+"-tls-ca", "-n", e2eDataNS)
	ca, err := base64.StdEncoding.DecodeString(secret["data"].(map[string]any)["ca.crt"].(string))
	require.NoError(f.t, err)
	endpoint := openBaoEndpoint{URL: "https://" + e2eCluster + "." + e2eDataNS + ".svc:8200",
		ServerName: e2eCluster + "." + e2eDataNS + ".svc", CA: ca,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(f.port))
		}}
	login, err := newOpenBao(endpoint)
	require.NoError(f.t, err)
	jwt := strings.TrimSpace(f.kubectl(nil, "create", "token", e2eAdmin, "-n", e2eDataNS,
		"--audience", "openbao-internal", "--duration", "4h"))
	var token string
	f.eventually("OpenBao administrator login", 2*time.Minute, func() bool {
		data, err := login.request(context.Background(), http.MethodPost, "auth/jwt-operator/login",
			map[string]string{"role": e2eAdmin, "jwt": jwt}, false)
		if err != nil {
			return false
		}
		var response struct {
			Auth struct {
				ClientToken string `json:"client_token"`
			} `json:"auth"`
		}
		_ = json.Unmarshal(data, &response)
		token = response.Auth.ClientToken
		return token != ""
	})
	// The tool reads its administrator token from a private file.
	require.NoError(f.t, os.WriteFile(f.tokenFile(), []byte(token+"\n"), 0o600))
	endpoint.Token = token
	f.bao, err = newOpenBao(endpoint)
	require.NoError(f.t, err)
}

func (f *e2eFixture) tokenFile() string { return filepath.Join(f.dir, "openbao-token") }

func (f *e2eFixture) podUID() string {
	out, err := exec.Command("kubectl", "--kubeconfig", f.kubeconfig, "--context", f.context, "get", "pod",
		f.voterSet+"-0", "-n", e2eDataNS, "-o", "jsonpath={.metadata.uid}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (f *e2eFixture) baoRequest(method, path string, payload any) ([]byte, error) {
	var data []byte
	var err error
	// The port-forward reconnects after Pod replacement; tolerate a short gap.
	for attempt := 0; attempt < 30; attempt++ {
		if data, err = f.bao.request(context.Background(), method, path, payload, true); err == nil {
			return data, nil
		}
		var httpErr *openBaoHTTPError
		if errors.As(err, &httpErr) {
			return nil, err
		}
		time.Sleep(2 * time.Second)
	}
	return nil, err
}

func (f *e2eFixture) writeData(key, value string) {
	_, err := f.baoRequest(http.MethodPost, "migration-data/data/"+key, map[string]any{"data": map[string]any{"value": value}})
	require.NoError(f.t, err, "write %s", key)
	f.written = append(f.written, key)
}

func (f *e2eFixture) assertData(keys ...string) {
	for _, key := range keys {
		data, err := f.baoRequest(http.MethodGet, "migration-data/data/"+key, nil)
		require.NoError(f.t, err, "read %s", key)
		require.Contains(f.t, string(data), `"value"`, "data %s", key)
	}
}

func (f *e2eFixture) newState(name string) string {
	return filepath.Join(f.dir, "state-"+name)
}

func (f *e2eFixture) configFile() string {
	path := filepath.Join(f.dir, "config.json")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	repository, tag, _ := strings.Cut(f.images[e2eDestination], ":")
	overlay := filepath.Join(f.dir, "destination-values.json")
	// The source values pin the operator version and init image, so the
	// destination values must name them; these are the values the cases qualify.
	values := map[string]any{
		"image":           map[string]any{"repository": repository, "tag": tag, "digest": "", "pullPolicy": "IfNotPresent"},
		"operatorVersion": e2eDestinationRelease, "helperImages": map[string]any{"init": f.images["init"]}}
	if f.shape.backups {
		values["helperImages"].(map[string]any)["backup"] = f.images["destination-backup"]
	}
	require.NoError(f.t, writePrivateJSON(overlay, values))
	root := strings.TrimSpace(f.cmd("git", nil, "rev-parse", "--show-toplevel"))
	// The tool manages the migrator and reaches OpenBao itself. The legacy case
	// uses the minimal config; the others add administrator evidence.
	config := Config{Kubeconfig: f.kubeconfig, Context: f.context, Release: e2eRelease, OperatorNamespace: e2eOperatorNS,
		SourceVersion: e2eSourceRelease, DestinationChart: filepath.Join(root, "charts/openbao-operator"),
		DestinationValues: overlay}
	if f.shape.install != "" {
		config.Release, config.DestinationChart, config.DestinationValues = "", "", ""
	}
	if !f.shape.legacy {
		config.Clusters = []ClusterConfig{{Namespace: e2eDataNS, Name: e2eCluster,
			OpenBao: &OpenBaoConfig{TokenFile: f.tokenFile()}}}
		if f.shape.restoreLifecycle {
			config.Clusters = append(config.Clusters, ClusterConfig{Namespace: e2eDataNS, Name: e2eRetainedTarget,
				OpenBao: &OpenBaoConfig{TokenFile: f.tokenFile()}})
		}
		if f.shape.paused {
			config.Clusters = append(config.Clusters, ClusterConfig{Namespace: e2eDataNS, Name: e2eIdle})
		}
		if f.shape.recovery {
			config.Clusters = append(config.Clusters, ClusterConfig{Namespace: e2eDataNS, Name: e2eSecond})
		}
		if f.shape.hardened {
			// The ACME certificate chains to infra-bao's PKI and names the ACME Service.
			config.Clusters = append(config.Clusters, ClusterConfig{Namespace: e2eDataNS, Name: e2eHardened,
				OpenBao: &OpenBaoConfig{CAFile: filepath.Join(f.dir, e2eACMECAFile), TLSServerName: e2eACMEDomain}})
		}
	}
	require.NoError(f.t, writePrivateJSON(path, config))
	return path
}

func (f *e2eFixture) run(command, state string, extra ...string) error {
	args := append([]string{command, "--state", state, "--timeout", "8m"}, extra...)
	var stdout, stderr bytes.Buffer
	writers := []io.Writer{&stderr, testWriter{f.t}}
	if f.steps != nil {
		writers = append(writers, f.steps)
	}
	return run(context.Background(), args, &stdout, io.MultiWriter(writers...))
}

func (f *e2eFixture) plan(state string) {
	f.t.Helper()
	require.NoError(f.t, f.run("plan", state, "--config", f.configFile()))
}

func (f *e2eFixture) migrate(state string) {
	f.t.Helper()
	before := f.dataPod()
	probes := f.startAvailabilityProbes()
	f.steps = &stepClock{}
	start := time.Now()
	f.procedure("migrate", state)
	for _, probe := range probes {
		f.assertAvailability(probe, start, f.steps.started("start-destination"), time.Now())
	}
	f.steps = nil
	if after := f.dataPod(); after != before {
		f.t.Logf("data Pod was replaced during migration (%s -> %s)", before, after)
	} else {
		f.t.Logf("data Pod %s survived the migration without a restart", before)
	}
}

// procedure runs a command to completion. At a checkpoint it applies the
// requested operator state, as an administrator does, and runs it again.
func (f *e2eFixture) procedure(command, state string) {
	f.t.Helper()
	for range 6 {
		err := f.run(command, state)
		var checkpoint *checkpointError
		if !errors.As(err, &checkpoint) {
			require.NoError(f.t, err, "%s", command)
			return
		}
		f.t.Logf("checkpoint: applying the %s operator state", checkpoint.State)
		f.applyOperatorState(checkpoint.State)
	}
	f.t.Fatalf("%s did not finish within six checkpoints", command)
}

// migrateUntilOrphaned stops migrate right after it orphans a StatefulSet and
// before it recreates it, and checks that the Pods have lost their controller.
func (f *e2eFixture) migrateUntilOrphaned(state string) {
	f.t.Helper()
	afterStatefulSetOrphaned = func() error { return errStop }
	defer func() { afterStatefulSetOrphaned = func() error { return nil } }()
	require.ErrorIs(f.t, f.run("migrate", state), errStop)
	f.eventually("the orphaned data Pod to lose its controller", 2*time.Minute, func() bool {
		pod := f.getJSON("get", "pod", f.voterSet+"-0", "-n", e2eDataNS)
		owners, _, _ := unstructured.NestedSlice(pod, "metadata", "ownerReferences")
		return len(owners) == 0
	})
}

func (f *e2eFixture) dataPod() string {
	pod := f.getJSON("get", "pod", f.voterSet+"-0", "-n", e2eDataNS)
	return pod["metadata"].(map[string]any)["uid"].(string)
}

// assertSurvivor checks that exactly one group remains, that it reconciles a
// new specification change, and that data and storage survived.
func (f *e2eFixture) assertSurvivor(group, proof string) {
	f.t.Helper()
	other := map[string]string{"openbao.org": "kubebao.org", "kubebao.org": "openbao.org"}[group]
	require.Nil(f.t, f.cluster(other), "the %s Cluster must be retired", other)
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"serviceAccount": map[string]any{
		"annotations": map[string]any{e2eProofKey: proof}}}})
	f.kubectl(nil, "patch", "openbaoclusters."+group, e2eCluster, "-n", e2eDataNS, "--type", "merge", "-p", string(patch))
	f.eventually("the surviving Cluster to reconcile a new change", 5*time.Minute, func() bool {
		out, err := exec.Command("kubectl", "--kubeconfig", f.kubeconfig, "--context", f.context, "get",
			"serviceaccounts", "-n", e2eDataNS, "-l", "openbao.org/cluster="+e2eCluster, "-o", "json").Output()
		return err == nil && regexp.MustCompile(`"`+regexp.QuoteMeta(e2eProofKey)+`":\s*"`+regexp.QuoteMeta(proof)+`"`).Match(out) &&
			clusterReady(f.cluster(group))
	})
	f.assertControllerLogin(group, proof)
	f.assertData(f.written...)
	f.writeData("after-"+proof, group)
	claim := f.getJSON("get", "pvc", "data-"+f.voterSet+"-0", "-n", e2eDataNS)
	require.Equal(f.t, "Bound", claim["status"].(map[string]any)["phase"])
}

// assertControllerLogin changes an autopilot setting and waits until OpenBao
// reports it. The controller applies autopilot settings through the OpenBao
// API with its own JWT, so this proves the surviving operator authenticates.
// The released source operator sends the CRD default cleanupDeadServers=true
// without a minimum quorum, which OpenBao rejects below three voters, so the
// source patch disables cleanup. The destination derives the quorum itself.
func (f *e2eFixture) assertControllerLogin(group, proof string) {
	f.t.Helper()
	trailing := 1000 + 100*len(f.written)
	autopilot := map[string]any{"maxTrailingLogs": trailing}
	if group == primitivesSourceGroup {
		autopilot["cleanupDeadServers"] = false
	}
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"configuration": map[string]any{
		"raft": map[string]any{"autopilot": autopilot}}}})
	f.kubectl(nil, "patch", "openbaoclusters."+group, e2eCluster, "-n", e2eDataNS, "--type", "merge", "-p", string(patch))
	f.eventually("the "+group+" controller to apply autopilot settings ("+proof+")", 5*time.Minute, func() bool {
		data, err := f.baoRequest(http.MethodGet, "sys/storage/raft/autopilot/configuration", nil)
		var response struct {
			Data struct {
				MaxTrailingLogs int `json:"max_trailing_logs"`
			} `json:"data"`
		}
		return err == nil && json.Unmarshal(data, &response) == nil && response.Data.MaxTrailingLogs == trailing
	})
}

// pauseSource pauses the running Cluster and creates a second one that is
// paused before it initializes, then waits until the source operator has
// observed both. It returns the running Cluster's data Pod UID.
func (f *e2eFixture) pauseSource() string {
	f.t.Helper()
	f.kubectl(nil, "patch", "openbaoclusters."+primitivesSourceGroup, e2eCluster, "-n", e2eDataNS, "--type", "merge",
		"-p", `{"spec":{"paused":true}}`)
	spec := f.cluster(primitivesSourceGroup).Object["spec"]
	idle, _ := json.Marshal(map[string]any{"apiVersion": primitivesSourceGroup + "/v1alpha1", "kind": "OpenBaoCluster",
		"metadata": map[string]any{"name": e2eIdle, "namespace": e2eDataNS}, "spec": spec})
	f.kubectl(idle, "apply", "-f", "-")
	f.waitPaused(primitivesSourceGroup)
	return f.dataPod()
}

func (f *e2eFixture) waitPaused(group string) {
	f.t.Helper()
	for _, name := range []string{e2eCluster, e2eIdle} {
		f.eventually("the "+group+" operator to observe "+name+" paused", 3*time.Minute, func() bool {
			object := f.clusterNamed(group, name)
			if object == nil {
				return false
			}
			paused, _, _ := unstructured.NestedBool(object.Object, "spec", "paused")
			return paused && clusterReady(object)
		})
	}
}

// assertPaused checks that both Clusters stay paused in the active group: the
// running one keeps serving from the same Pod, and the idle one has no workload.
// After commit or rollback the other group's Clusters must be retired.
func (f *e2eFixture) assertPaused(group, pod string, retired bool) {
	f.t.Helper()
	other := map[string]string{"openbao.org": "kubebao.org", "kubebao.org": "openbao.org"}[group]
	f.waitPaused(group)
	for _, name := range []string{e2eCluster, e2eIdle} {
		if retired {
			require.Nil(f.t, f.clusterNamed(other, name), "the %s Cluster %s must be retired", other, name)
		}
	}
	require.Equal(f.t, pod, f.dataPod(), "a paused Cluster's data Pod must not restart")
	f.assertData(f.written...)
	workload := f.getJSON("get", "statefulsets,pods,persistentvolumeclaims", "-n", e2eDataNS,
		"-l", "openbao.org/cluster="+e2eIdle)
	require.Empty(f.t, workload["items"], "the idle Cluster must not get a workload while paused")
}

// resume unpauses both Clusters: the idle one initializes for the first time
// under the active operator.
func (f *e2eFixture) resume(group string) {
	f.t.Helper()
	for _, name := range []string{e2eCluster, e2eIdle} {
		f.kubectl(nil, "patch", "openbaoclusters."+group, name, "-n", e2eDataNS, "--type", "merge",
			"-p", `{"spec":{"paused":false}}`)
	}
	f.eventually("the idle Cluster to initialize under "+group, 10*time.Minute, func() bool {
		return clusterReady(f.clusterNamed(group, e2eIdle))
	})
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
