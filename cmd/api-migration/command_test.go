package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func validConfig() Config {
	return Config{Kubeconfig: "/kubeconfig", Context: "kind", Release: "openbao-operator",
		OperatorNamespace: "openbao-operator-system", SourceVersion: "0.6.1", DestinationChart: "./chart",
		Migrator: "system:serviceaccount:openbao-operator-system:api-migrator",
		Clusters: []ClusterConfig{{Namespace: "openbao", Name: "bao", OpenBao: &OpenBaoConfig{TokenFile: "token"}}}}
}

func TestVersionPrintsTheBuildRelease(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.NoError(t, run(t.Context(), []string{"version"}, &stdout, &stderr))
	require.Equal(t, version+"\n", stdout.String())
}

func TestSourceChartIgnoresBuildMetadata(t *testing.T) {
	want := "openbao-operator-0.6.1"
	require.True(t, isSourceChart(want, want))
	require.True(t, isSourceChart("openbao-operator-0.6.1+96dc4cba8574", want), "Flux OCIRepository charts carry a digest")
	require.False(t, isSourceChart("openbao-operator-0.6.0", want))
	require.False(t, isSourceChart("openbao-operator-0.6.10", want))
	require.False(t, isSourceChart("other-chart-0.6.1", want))
}

func TestConfigValidation(t *testing.T) {
	require.NoError(t, validConfig().validate())
	minimal := validConfig()
	minimal.Migrator, minimal.Clusters = "", nil
	require.NoError(t, minimal.validate(), "the release and the destination chart are enough")
	require.Equal(t, "system:serviceaccount:openbao-operator-system:api-migrator-openbao-operator",
		minimal.migratorPrincipal(), "the tool manages a migrator unless one is configured")
	manifests := minimal
	manifests.Release, manifests.DestinationChart = "", ""
	require.NoError(t, manifests.validate(), "a manifest installation names no Helm release")
	require.Equal(t, "system:serviceaccount:openbao-operator-system:api-migrator", manifests.migratorPrincipal())
	acme := validConfig()
	acme.Clusters[0].OpenBao = &OpenBaoConfig{CAFile: "./acme-ca.crt", TLSServerName: "bao-acme.openbao.svc"}
	require.NoError(t, acme.validate(), "an ACME Cluster needs its CA and server name, not a token")
	for name, edit := range map[string]func(*Config){
		"group principal":      func(c *Config) { c.Migrator = "system:serviceaccounts:openbao-operator-system" },
		"user principal":       func(c *Config) { c.Migrator = "admin" },
		"duplicate cluster":    func(c *Config) { c.Clusters = append(c.Clusters, c.Clusters[0]) },
		"empty openbao block":  func(c *Config) { c.Clusters[0].OpenBao = &OpenBaoConfig{} },
		"no source version":    func(c *Config) { c.SourceVersion = "" },
		"older source version": func(c *Config) { c.SourceVersion = "0.6.0" },
		"helm without chart":   func(c *Config) { c.DestinationChart = "" },
		"manifests with chart": func(c *Config) { c.Release = "" },
		"helm with manifests":  func(c *Config) { c.DestinationManifests = "./install.yaml" },
	} {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			edit(&config)
			require.Error(t, config.validate())
		})
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"kubeconfig":"k","unexpected":true}`), 0o600))
	_, err := loadConfig(path)
	require.ErrorContains(t, err, "unknown field")
}

func TestChartValuesLayerOverlayAndProcedureSettings(t *testing.T) {
	source := map[string]any{"tenancy": map[string]any{"mode": "single", "targetNamespace": "openbao"},
		"image":      map[string]any{"repository": "ghcr.io/dc-tec/openbao-operator", "tag": "0.6.0"},
		"controller": map[string]any{"replicas": 1, "resources": map[string]any{"limits": map[string]any{"cpu": "1"}}}}
	overlay := map[string]any{"image": map[string]any{"repository": "ghcr.io/kubebao/openbao-operator", "tag": "0.7.0"}}
	values := chartValues(source, overlay, map[string]int32{"controller": 0, "provisioner": 0}, true)
	require.Equal(t, map[string]any{"repository": "ghcr.io/kubebao/openbao-operator", "tag": "0.7.0"}, values["image"])
	controller := values["controller"].(map[string]any)
	require.EqualValues(t, 0, controller["replicas"])
	require.Equal(t, true, controller["apiMigrationHold"])
	require.Equal(t, map[string]any{"limits": map[string]any{"cpu": "1"}}, controller["resources"])
	require.NotContains(t, values, "apiMigration", "the tool opens the handoff in the live policy, not through the chart")
	require.Equal(t, "0.6.0", source["image"].(map[string]any)["tag"], "source values must not be modified")
	require.True(t, sameValues(values, deepCopyMap(values)))
	require.False(t, sameValues(values, source))
}

func statefulSetFixture(owner primitives.TransferIdentity, templateOwner string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: "openbao", UID: "set-uid", ResourceVersion: "42",
			Generation: 3, Labels: map[string]string{constants.LabelOpenBaoCluster: "bao"},
			Annotations:     map[string]string{constants.AnnotationOpenBaoOwnerUID: string(owner.UID)},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: owner.APIVersion, Kind: owner.Kind, Name: owner.Name, UID: owner.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}},
			ManagedFields:   []metav1.ManagedFieldsEntry{{Manager: "openbao-operator"}},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(3)),
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"config-hash": "abc"}}},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data",
				Annotations: map[string]string{constants.AnnotationOpenBaoOwnerUID: templateOwner, "keep": "me"}}}},
		},
		Status: appsv1.StatefulSetStatus{ReadyReplicas: 3},
	}
}

func TestRecreatedStatefulSetMovesOnlyClaimProvenance(t *testing.T) {
	destination := primitives.TransferIdentity{APIVersion: primitives.PlanDestinationAPIVersion, Kind: clusterKind,
		Namespace: "openbao", Name: "bao", UID: "destination-uid"}
	saved := statefulSetFixture(destination, "source-uid")
	saved.Annotations[constants.AnnotationClusterGeneration] = "1"
	saved.Spec.UpdateStrategy = partitionedStrategy(3)
	original := saved.DeepCopy()
	require.False(t, claimTemplatesOwnedBy(saved, destination.UID))

	set, err := recreatedStatefulSet(saved, destination)
	require.NoError(t, err)
	require.Empty(t, set.UID)
	require.Empty(t, set.ResourceVersion)
	require.Empty(t, set.ManagedFields)
	require.Zero(t, set.Generation)
	require.Equal(t, appsv1.StatefulSetStatus{}, set.Status)
	require.Equal(t, saved.Spec.Template, set.Spec.Template, "the Pod template must be unchanged to avoid a rollout")
	require.Equal(t, saved.Spec.UpdateStrategy, set.Spec.UpdateStrategy, "the existing rollout strategy must be preserved")
	require.NotContains(t, set.Annotations, constants.AnnotationClusterGeneration,
		"a coincidentally equal imported generation must not impersonate a destination render")
	require.Equal(t, original, saved, "marker invalidation must not change the saved manifest")
	set.Annotations["test"] = "annotation-copy"
	set.Labels["test"] = "label-copy"
	require.Equal(t, original, saved, "recreation metadata must not alias saved maps")
	require.Equal(t, saved.OwnerReferences, set.OwnerReferences)
	require.Equal(t, "destination-uid", set.Spec.VolumeClaimTemplates[0].Annotations[constants.AnnotationOpenBaoOwnerUID])
	require.Equal(t, "me", set.Spec.VolumeClaimTemplates[0].Annotations["keep"])
	require.Equal(t, "source-uid", saved.Spec.VolumeClaimTemplates[0].Annotations[constants.AnnotationOpenBaoOwnerUID],
		"the saved manifest must not be modified")
	require.True(t, claimTemplatesOwnedBy(set, destination.UID))
}

func TestRecreatedStatefulSetRequiresTransferredOwnership(t *testing.T) {
	source := primitives.TransferIdentity{APIVersion: primitives.PlanSourceAPIVersion, Kind: clusterKind,
		Namespace: "openbao", Name: "bao", UID: "source-uid"}
	destination := source
	destination.APIVersion, destination.UID = primitives.PlanDestinationAPIVersion, "destination-uid"
	_, err := recreatedStatefulSet(statefulSetFixture(source, "source-uid"), destination)
	require.ErrorContains(t, err, "must be transferred")
}

func readyCluster() *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "bao", "generation": int64(2), "finalizers": []any{clusterFinalizer}},
		"spec":     map[string]any{"replicas": int64(3)},
		"status": map[string]any{"phase": "Running", "observedGeneration": int64(2), "readyReplicas": int64(3),
			"conditions": []any{map[string]any{"type": "Available", "status": "True", "observedGeneration": int64(2)}}},
	}}
	return object
}

func TestClusterReady(t *testing.T) {
	require.True(t, clusterReady(readyCluster()))
	require.False(t, clusterReady(nil))
	for name, edit := range map[string]func(*unstructured.Unstructured){
		"imported status without conditions": func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "status", "conditions")
		},
		"stale generation": func(o *unstructured.Unstructured) { o.SetGeneration(3) },
		"not ready": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(2), "status", "readyReplicas")
		},
		"no finalizer": func(o *unstructured.Unstructured) { o.SetFinalizers(nil) },
		"read pool not registered": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(1), "spec", "readReplicas", "replicas")
			_ = unstructured.SetNestedMap(o.Object, map[string]any{"readyReplicas": int64(1)}, "status", "readReplicas")
		},
		"deleting": func(o *unstructured.Unstructured) { o.SetDeletionTimestamp(ptr.To(metav1.Now())) },
	} {
		t.Run(name, func(t *testing.T) {
			object := readyCluster()
			edit(object)
			require.False(t, clusterReady(object))
		})
	}
}

// A paused Cluster is never available; its operator records the pause instead.
func TestClusterReadyAcceptsAnObservedPause(t *testing.T) {
	paused := func() *unstructured.Unstructured {
		object := readyCluster()
		_ = unstructured.SetNestedField(object.Object, true, "spec", "paused")
		_ = unstructured.SetNestedField(object.Object, int64(0), "status", "readyReplicas")
		_ = unstructured.SetNestedField(object.Object, "Initializing", "status", "phase")
		_ = unstructured.SetNestedSlice(object.Object, []any{map[string]any{"type": "Available", "status": "Unknown",
			"reason": primitives.PausedReason, "observedGeneration": int64(2)}}, "status", "conditions")
		return object
	}
	require.True(t, clusterReady(paused()))
	for name, edit := range map[string]func(*unstructured.Unstructured){
		"pause not observed": func(o *unstructured.Unstructured) { o.SetGeneration(3) },
		"stale pause condition": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(3), "status", "observedGeneration")
			o.SetGeneration(3)
		},
		"imported status without conditions": func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "status", "conditions")
		},
		"no finalizer": func(o *unstructured.Unstructured) { o.SetFinalizers(nil) },
		"resumed": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, false, "spec", "paused")
		},
	} {
		t.Run(name, func(t *testing.T) {
			object := paused()
			edit(object)
			require.False(t, clusterReady(object))
		})
	}
}

func TestStateDirectoryIsPrivateAndExclusive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	state, err := openState(dir, true)
	require.NoError(t, err)
	_, err = openState(dir, false)
	require.ErrorContains(t, err, "another api-migration process")
	plan := &Plan{Version: 1, Phase: phasePlanned,
		Clusters: []ClusterPlan{{Name: "bao", VoterStatefulSet: "bao-blue-revision"}}}
	require.NoError(t, state.Save(plan))
	info, err := os.Stat(state.path(planFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	loaded, err := state.Load()
	require.NoError(t, err)
	require.Equal(t, phasePlanned, loaded.Phase)
	require.Equal(t, "bao-blue-revision", loaded.Clusters[0].voterStatefulSet(), "resume must retain the active voter workload")
	state.Close()

	_, err = openState(dir, true)
	require.ErrorContains(t, err, "new, empty state directory")
	shared := filepath.Join(t.TempDir(), "shared")
	require.NoError(t, os.Mkdir(shared, 0o755))
	_, err = openState(shared, false)
	require.ErrorContains(t, err, "other users")
}

func TestPartitionCustodyAccountsForBothDirections(t *testing.T) {
	resource := func(kind, name string) primitives.TransferIdentity {
		return primitives.TransferIdentity{APIVersion: "v1", Kind: kind, Namespace: "openbao", Name: name, UID: types.UID(name)}
	}
	set := resource("StatefulSet", "bao")
	pending, settled := resource("ConfigMap", "pending"), resource("Secret", "settled")
	forwardPod, reversePod := resource("Pod", "bao-0"), resource("Pod", "bao-1")
	foreign := resource("ConfigMap", "foreign")
	forward := &primitives.ManagedDiscovery{
		Transfers:   []primitives.TransferRequest{{Resource: pending}, {Resource: set}},
		Descendants: []primitives.DiscoveryDescendant{{Resource: forwardPod, Owner: set}},
		Blockers: []primitives.DiscoveryBlocker{
			{Resource: settled, Code: "UnsupportedOwnerReference"},
			{Resource: reversePod, Code: "UnsupportedResource"},
			{Resource: foreign, Code: "OwnershipProofIncompleteOrConflicting"},
		},
	}
	reverse := &primitives.ManagedDiscovery{
		Transfers:   []primitives.TransferRequest{{Resource: settled}},
		Descendants: []primitives.DiscoveryDescendant{{Resource: reversePod, Owner: set}},
		Blockers: []primitives.DiscoveryBlocker{
			{Resource: pending, Code: "UnsupportedOwnerReference"},
			{Resource: set, Code: "UnsupportedOwnerReference"},
			{Resource: forwardPod, Code: "UnsupportedResource"},
			{Resource: foreign, Code: "OwnershipProofIncompleteOrConflicting"},
		},
	}
	state := partitionCustody(forward, reverse)
	require.Len(t, state.pending, 2)
	require.Equal(t, []primitives.TransferIdentity{settled}, state.settled)
	require.Len(t, state.blockers, 1, "only a resource neither direction accounts for is a blocker")
	require.Equal(t, foreign, state.blockers[0].Resource)
	require.ErrorContains(t, state.blockerError(), "foreign")
	require.NoError(t, partitionCustody(&primitives.ManagedDiscovery{}, &primitives.ManagedDiscovery{}).blockerError())
}

func TestStatefulSetSettled(t *testing.T) {
	settled := func() *appsv1.StatefulSet {
		return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Generation: 2}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3))},
			Status: appsv1.StatefulSetStatus{ObservedGeneration: 2, CurrentRevision: "bao-1", UpdateRevision: "bao-1",
				Replicas: 3, ReadyReplicas: 3, UpdatedReplicas: 3}}
	}
	require.True(t, statefulSetSettled(settled(), true))
	for name, edit := range map[string]func(*appsv1.StatefulSet){
		"rolling":       func(s *appsv1.StatefulSet) { s.Status.UpdateRevision = "bao-2"; s.Status.UpdatedReplicas = 1 },
		"not observed":  func(s *appsv1.StatefulSet) { s.Generation = 3 },
		"pod not ready": func(s *appsv1.StatefulSet) { s.Status.ReadyReplicas = 2 },
		"no status yet": func(s *appsv1.StatefulSet) { s.Status = appsv1.StatefulSetStatus{} },
		"scaling":       func(s *appsv1.StatefulSet) { s.Status.Replicas = 4 },
	} {
		t.Run(name, func(t *testing.T) {
			set := settled()
			edit(set)
			require.False(t, statefulSetSettled(set, true))
		})
	}
	notReady := settled()
	notReady.Status.ReadyReplicas = 0
	require.True(t, statefulSetSettled(notReady, false), "a paused Cluster's Pods need not be ready")
	notReady.Status.UpdateRevision = "bao-2"
	require.False(t, statefulSetSettled(notReady, false), "a paused Cluster must not be rolling")
}

func TestStatefulSetSettledRespectsUpdateStrategy(t *testing.T) {
	onDelete := appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	for _, tc := range []struct {
		name     string
		strategy appsv1.StatefulSetUpdateStrategy
		updated  int32
		ready    int32
		observed int64
		settled  bool
	}{
		{name: "OnDelete retains old Pods", strategy: onDelete, updated: 0, ready: 3, observed: 2, settled: true},
		{name: "OnDelete has mixed revisions", strategy: onDelete, updated: 1, ready: 3, observed: 2, settled: true},
		{name: "OnDelete Pod is unavailable", strategy: onDelete, updated: 0, ready: 2, observed: 2},
		{name: "OnDelete generation is not observed", strategy: onDelete, updated: 0, ready: 3, observed: 1},
		{name: "partition holds all Pods", strategy: partitionedStrategy(3), updated: 0, ready: 3, observed: 2, settled: true},
		{name: "partition holds one Pod", strategy: partitionedStrategy(1), updated: 2, ready: 3, observed: 2, settled: true},
		{name: "partition rollout is incomplete", strategy: partitionedStrategy(1), updated: 1, ready: 3, observed: 2},
		{name: "partition rollout has an unavailable Pod", strategy: partitionedStrategy(1), updated: 2, ready: 2, observed: 2},
		{name: "unpartitioned rollout is incomplete", strategy: partitionedStrategy(0), updated: 2, ready: 3, observed: 2},
		{name: "unpartitioned revision has not converged", strategy: partitionedStrategy(0), updated: 3, ready: 3, observed: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3)), UpdateStrategy: tc.strategy},
				Status: appsv1.StatefulSetStatus{ObservedGeneration: tc.observed, CurrentRevision: "old", UpdateRevision: "new",
					Replicas: 3, ReadyReplicas: tc.ready, UpdatedReplicas: tc.updated}}
			require.Equal(t, tc.settled, statefulSetSettled(set, true))
			set.DeletionTimestamp = ptr.To(metav1.Now())
			require.False(t, statefulSetSettled(set, true), "deleting workloads are never settled")
		})
	}
}

func partitionedStrategy(partition int32) appsv1.StatefulSetUpdateStrategy {
	return appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To(partition)}}
}

func TestPortForwardSelectsThePinnedVoterWorkload(t *testing.T) {
	for _, voterSet := range []string{"", "bao-blue-revision"} {
		t.Run(voterSet, func(t *testing.T) {
			plan := ClusterPlan{Name: "bao", VoterStatefulSet: voterSet}
			pod := func(name, owner string) *corev1.Pod {
				return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant",
					Labels:          map[string]string{constants.LabelOpenBaoCluster: plan.Name},
					OwnerReferences: []metav1.OwnerReference{{Kind: statefulSetKind, Name: owner, Controller: ptr.To(true)}}},
					Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
			}
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			active := pod("active", plan.voterStatefulSet())
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				pod("aaa-read-replica", "bao-read"), pod("aab-inactive", "bao-other-revision"), active).Build()
			forward := newPortForward("", "", c, active.Namespace, plan.Name, plan.voterStatefulSet())
			selected, err := forward.readyPod(t.Context())
			require.NoError(t, err)
			require.Equal(t, active.Name, selected.Name)
			require.NoError(t, c.Delete(t.Context(), active))
			_, err = forward.readyPod(t.Context())
			require.ErrorContains(t, err, "no ready OpenBao Pod")
		})
	}
}

func TestOpenBaoRetriesOnlyIdempotentRequests(t *testing.T) {
	openBaoRetryDelay = 0
	t.Cleanup(func() { openBaoRetryDelay = 2 * time.Second })
	snapshot := bytes.Repeat([]byte("s"), 2<<20)
	var callsMu sync.Mutex
	calls := map[string]int{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsMu.Lock()
		calls[r.Method+" "+r.URL.Path]++
		count := calls[r.Method+" "+r.URL.Path]
		callsMu.Unlock()
		if count < 3 {
			hijacker, _ := w.(http.Hijacker)
			conn, _, _ := hijacker.Hijack()
			_ = conn.Close() // a transport failure, as when a port-forward drops
			return
		}
		if r.URL.Path == "/v1/sys/storage/raft/snapshot" {
			_, _ = w.Write(snapshot)
			return
		}
		if r.URL.Path == "/v1/auth/jwt/login" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"config":{"servers":[{"node_id":"bao-0","voter":true}]}}}`))
	}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	address := strings.TrimPrefix(server.URL, "https://")
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, address)
	}
	endpoint := openBaoEndpoint{URL: "https://example.test", ServerName: "example.com", CA: ca, Token: "admin", Dial: dial}
	bao, err := newOpenBao(endpoint)
	require.NoError(t, err)

	peers, err := bao.RaftPeers(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"bao-0/voter=true"}, peers)
	path := filepath.Join(t.TempDir(), "snapshot")
	require.NoError(t, bao.Snapshot(t.Context(), path), "snapshot reads recover when the port-forward drops")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, snapshot, data, "snapshots stream beyond the ordinary response limit")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0o077)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		_, err = bao.request(t.Context(), method, "sys/mounts/test", nil, true)
		require.Error(t, err, "mutations must not replay after a transport failure")
	}
	var denied *openBaoHTTPError
	require.ErrorAs(t, bao.Login(t.Context(), "jwt", "operator", "token"), &denied, "a denial ends the retry")
	callsMu.Lock()
	counts := maps.Clone(calls)
	callsMu.Unlock()
	require.Equal(t, 3, counts["GET /v1/sys/storage/raft/snapshot"])
	require.Equal(t, 3, counts["POST /v1/auth/jwt/login"])
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		require.Equal(t, 1, counts[method+" /v1/sys/mounts/test"])
	}

	endpoint.Token = ""
	anonymous, err := newOpenBao(endpoint)
	require.NoError(t, err)
	_, err = anonymous.RaftPeers(t.Context())
	require.ErrorContains(t, err, "no OpenBao administrator token", "evidence needs a configured token")
}

func TestReadTokenRequiresAPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("admin\n"), 0o600))
	token, err := readToken(path)
	require.NoError(t, err)
	require.Equal(t, "admin", token)
	require.NoError(t, os.Chmod(path, 0o644))
	_, err = readToken(path)
	require.ErrorContains(t, err, "private regular file")
}

func TestImportKeysDropSourceBoundAnnotations(t *testing.T) {
	source := &unstructured.Unstructured{}
	source.SetLabels(map[string]string{"example.org/b": "1", "example.org/a": "2",
		fluxKustomizeName: "resources", fluxKustomizeNamespace: "flux-system", fluxHelmName: "operator"})
	source.SetAnnotations(map[string]string{"example.org/kept": "x",
		constants.AnnotationRestoreAcknowledge:             "source-uid/Resume",
		"kubectl.kubernetes.io/last-applied-configuration": "{}",
		primitives.GateSourceUIDAnnotation:                 "older-import",
		argoTrackingID:                                     "resources:openbao.org/OpenBaoCluster:openbao/bao"})
	labels, annotations := importKeys(source)
	require.Equal(t, []string{"example.org/a", "example.org/b"}, labels,
		"Flux ownership labels name the source; Flux labels the destination once Git names it")
	require.Equal(t, []string{"example.org/kept"}, annotations,
		"the acknowledgement names the source record's UID; its decision is in the imported status; "+
			"a copied Argo CD tracking ID could let Argo CD prune the destination as the source")
}

func TestClusterScopeNamesPlannedRestoreRecords(t *testing.T) {
	p := &Procedure{plan: &Plan{Restores: []RecordPlan{
		{Kind: restoreKind, Namespace: "openbao", Name: "imported", SourceUID: "source-1", DestinationUID: "destination-1"},
		{Kind: restoreKind, Namespace: "openbao", Name: "planned", SourceUID: "source-2"},
		{Kind: restoreKind, Namespace: "elsewhere", Name: "other", SourceUID: "source-3"},
	}}}
	cluster := ClusterPlan{Namespace: "openbao", NamespaceUID: "namespace-uid",
		Retained: []primitives.TransferIdentity{{APIVersion: "v1", Kind: "Secret", Namespace: "openbao", Name: "s", UID: "s"}}}
	scope := p.clusterScope(cluster)
	require.Equal(t, cluster.Retained, scope.retained)
	require.Equal(t, []primitives.TransferIdentity{
		p.plan.Restores[0].source(), p.plan.Restores[0].destination(), p.plan.Restores[1].source(),
	}, scope.foreign, "a record's destination joins once it is imported; other namespaces never do")
	require.Empty(t, p.plan.Restores[1].scope().foreign, "a restore record's own discovery has no foreign roots")
}

func TestScopeFollowsTenancyMode(t *testing.T) {
	p := &Procedure{plan: &Plan{Operator: OperatorPlan{Mode: "single", TargetNamespace: "openbao"}}}
	require.Equal(t, "openbao", p.scopeNamespace())
	p.plan.Operator = OperatorPlan{Mode: tenancyMulti}
	require.Empty(t, p.scopeNamespace(), "a multi-tenant operator manages every tenant namespace")
}

func TestRoleGrantsClustersNamesTheGroup(t *testing.T) {
	role := &rbacv1.Role{Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"apps"}, Resources: []string{"statefulsets"}},
		{APIGroups: []string{"openbao.org"}, Resources: []string{"openbaoclusters", "openbaoclusters/status"}},
	}}
	require.True(t, roleGrantsClusters(role, "openbao.org"))
	require.False(t, roleGrantsClusters(role, "kubebao.org"), "a Role rendered by the other provisioner")
	require.Equal(t, "tenant operator/team-a", RecordPlan{Kind: tenantKind, Namespace: "operator", Name: "team-a"}.label())
}

func TestManagedMigratorGrantsExactlyTheHandoffWrites(t *testing.T) {
	config := validConfig()
	config.Migrator = ""
	p := &Procedure{plan: &Plan{Config: config, MigrationID: "m1"}}
	objects := p.migratorObjects([]string{"openbao", "team-b"})
	require.Len(t, objects, 5, "a ServiceAccount, and a Role and RoleBinding per namespace")
	account, ok := objects[0].(*corev1.ServiceAccount)
	require.True(t, ok)
	require.Equal(t, "openbao-operator-system", account.Namespace)
	require.Equal(t, config.migratorPrincipal(), "system:serviceaccount:"+account.Namespace+":"+account.Name)
	for _, object := range objects {
		require.Equal(t, "m1", object.GetLabels()[labelMigrationID], "removal deletes only its own objects")
	}
	role, ok := objects[1].(*rbacv1.Role)
	require.True(t, ok)
	require.Len(t, role.Rules, len(migratorAccess()))
	for i, access := range migratorAccess() {
		rule := role.Rules[i]
		require.Equal(t, []string{access.Verb}, rule.Verbs)
		require.Equal(t, []string{access.Group}, rule.APIGroups)
	}
	binding, ok := objects[2].(*rbacv1.RoleBinding)
	require.True(t, ok)
	require.Equal(t, account.Name, binding.Subjects[0].Name)
	require.Equal(t, account.Namespace, binding.Subjects[0].Namespace)
}

func TestShippedComponentsMatchTheCheckpointCopies(t *testing.T) {
	for name, content := range map[string]string{"api-migration-stopped": componentStopped,
		"api-migration-hold": componentHold} {
		data, err := os.ReadFile(filepath.Join("..", "..", "config", "components", name, "kustomization.yaml"))
		require.NoError(t, err)
		require.Equal(t, content, string(data), "config/components/%s must match the copy a checkpoint writes", name)
	}
}

func TestComponentsProduceTheCheckpointStates(t *testing.T) {
	kustomize, err := exec.LookPath(filepath.Join("..", "..", "bin", "kustomize"))
	if err != nil {
		t.Skip("bin/kustomize is not available")
	}
	for name, check := range map[string]func(t *testing.T, deployments map[string]appsv1.Deployment){
		"api-migration-stopped": func(t *testing.T, deployments map[string]appsv1.Deployment) {
			t.Helper()
			for name, deployment := range deployments {
				require.EqualValues(t, 0, *deployment.Spec.Replicas, "%s is stopped", name)
			}
		},
		"api-migration-hold": func(t *testing.T, deployments map[string]appsv1.Deployment) {
			t.Helper()
			for name, deployment := range deployments {
				held := slices.Contains(deployment.Spec.Template.Spec.Containers[0].Args, holdArgument)
				require.Equal(t, strings.HasSuffix(name, "controller"), held, "only the controller is held: %s", name)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp(".", ".kustomize-")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(
				"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ../../../config/default\n"+
					"components:\n  - ../../../config/components/"+name+"\n"), 0o600))
			out, err := exec.Command(kustomize, "build", dir).Output()
			require.NoError(t, err)
			deployments := map[string]appsv1.Deployment{}
			for _, document := range strings.Split(string(out), "\n---\n") {
				var deployment appsv1.Deployment
				if yaml.Unmarshal([]byte(document), &deployment) == nil && deployment.Kind == "Deployment" {
					deployments[deployment.Name] = deployment
				}
			}
			require.Len(t, deployments, 2)
			check(t, deployments)
		})
	}
}

func TestObservationMatchesOnlyItsState(t *testing.T) {
	held := operatorObservation{group: "kubebao.org", running: true, held: true, crds: true}
	require.True(t, held.matches(stateHeld))
	require.False(t, held.matches(stateNormal), "the hold must be released")
	stopped := operatorObservation{group: "kubebao.org", stopped: true, crds: true}
	require.True(t, stopped.matches(stateStopped))
	stopped.crds = false
	require.False(t, stopped.matches(stateStopped), "the destination installation brings its CRDs")
	source := operatorObservation{group: "openbao.org", running: true}
	require.True(t, source.matches(stateSource))
	require.False(t, source.matches(stateNormal))
}

func TestStepsResumeAfterTheLastCompletedOne(t *testing.T) {
	state, err := openState(filepath.Join(t.TempDir(), "state"), true)
	require.NoError(t, err)
	t.Cleanup(state.Close)
	p := &Procedure{state: state, plan: &Plan{}, log: io.Discard}
	runs := 0
	run := func() (string, error) { runs++; return "done", nil }
	p.begin("migrate")
	require.NoError(t, p.step(t.Context(), "stop-operators", run))
	require.NoError(t, p.step(t.Context(), "stop-operators", run))
	require.Equal(t, 1, runs, "a completed step is not repeated: it would undo a later checkpoint")
	p.begin("rollback")
	require.NoError(t, p.step(t.Context(), "stop-operators", run))
	require.Equal(t, 2, runs, "another command starts from its first step")
	failing := func() (string, error) { return "", errors.New("not yet") }
	require.Error(t, p.step(t.Context(), "restore-source", failing))
	require.NotContains(t, p.plan.Progress.Completed, "restore-source", "a failed step runs again")
}

func TestConvertRewritesOnlyOpenBaoResources(t *testing.T) {
	input := `# A Cluster managed by GitOps.
apiVersion: openbao.org/v1alpha1 # keep this comment
kind: OpenBaoCluster
metadata:
  name: bao
  annotations:
    note: "apiVersion: openbao.org/v1alpha1 stays in a value"
spec:
  replicas: 3
---
apiVersion: v1
kind: ConfigMap
metadata: {name: unrelated}
data:
  apiVersion: openbao.org/v1alpha1
---
kind: OpenBaoTenant
apiVersion: "openbao.org/v1alpha1"
metadata: {name: team}
spec: {targetNamespace: team}
---
apiVersion: openbao.org/v1alpha1
kind: SomethingElse
`
	output, changed := convertManifest(input)
	require.Equal(t, 2, changed)
	require.Contains(t, output, "apiVersion: kubebao.org/v1alpha1 # keep this comment\nkind: OpenBaoCluster")
	require.Contains(t, output, `note: "apiVersion: openbao.org/v1alpha1 stays in a value"`)
	require.Contains(t, output, "data:\n  apiVersion: openbao.org/v1alpha1", "nested keys are not resource versions")
	require.Contains(t, output, "kind: OpenBaoTenant\napiVersion: \"kubebao.org/v1alpha1\"")
	require.Contains(t, output, "apiVersion: openbao.org/v1alpha1\nkind: SomethingElse")
	again, changed := convertManifest(output)
	require.Zero(t, changed, "conversion is idempotent")
	require.Equal(t, output, again)

	path := filepath.Join(t.TempDir(), "cluster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(input), 0o640))
	var out bytes.Buffer
	require.NoError(t, runConvert([]string{path}, &out, io.Discard))
	unchanged, _ := os.ReadFile(path)
	require.Equal(t, input, string(unchanged), "without --write the files stay as they are")
	require.NoError(t, runConvert([]string{"--write", path}, &out, io.Discard))
	written, _ := os.ReadFile(path)
	require.Equal(t, output, string(written))
	info, _ := os.Stat(path)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

func gitopsObject(gvk schema.GroupVersionKind, namespace, name string, fields map[string]any) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{}}
	for key, value := range fields {
		object.Object[key] = value
	}
	object.SetGroupVersionKind(gvk)
	object.SetNamespace(namespace)
	object.SetName(name)
	return object
}

func gitopsProcedure(t *testing.T, objects ...client.Object) *Procedure {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	for _, gvk := range []schema.GroupVersionKind{fluxHelmRelease, fluxKustomization, argoApplication, argoApplicationSet} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &Procedure{plan: &Plan{}, kube: &Kube{client: c}}
}

func TestGitOpsManagersMustStaySuspended(t *testing.T) {
	spec := func(fields map[string]any) map[string]any { return map[string]any{"spec": fields} }
	release := gitopsObject(fluxHelmRelease, "flux-system", "openbao-operator", spec(map[string]any{"suspend": true}))
	release.SetLabels(map[string]string{fluxKustomizeName: "infrastructure", fluxKustomizeNamespace: "flux-system"})
	parent := gitopsObject(fluxKustomization, "flux-system", "infrastructure", spec(map[string]any{"suspend": false}))
	p := gitopsProcedure(t, release, parent)

	deployment := &metav1.ObjectMeta{Labels: map[string]string{fluxHelmName: "openbao-operator",
		fluxHelmNamespace: "flux-system"}}
	managers, err := p.gitopsManagers(t.Context(), deployment)
	require.NoError(t, err)
	require.Equal(t, []GitOpsManager{{Kind: "HelmRelease", Namespace: "flux-system", Name: "openbao-operator"}}, managers)
	p.plan.GitOps = managers
	require.ErrorContains(t, p.checkGitOps(t.Context()), "Kustomization flux-system/infrastructure is not suspended",
		"a parent Kustomization would re-apply the HelmRelease without its suspension")

	parent.Object["spec"] = map[string]any{"suspend": true}
	p = gitopsProcedure(t, release, parent)
	p.plan.GitOps = managers
	require.NoError(t, p.checkGitOps(t.Context()))
}

func TestArgoApplicationsMustNotSyncAutomatically(t *testing.T) {
	automated := gitopsObject(argoApplication, "argocd", "openbao", map[string]any{
		"spec": map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": true}}}})
	p := gitopsProcedure(t, automated)
	cluster := &metav1.ObjectMeta{Annotations: map[string]string{argoTrackingID: "openbao:openbao.org/OpenBaoCluster:ns/bao"}}
	managers, err := p.gitopsManagers(t.Context(), cluster)
	require.NoError(t, err)
	require.Equal(t, []GitOpsManager{{Kind: "Application", Namespace: "argocd", Name: "openbao"}}, managers)
	p.plan.GitOps = managers
	require.ErrorContains(t, p.checkGitOps(t.Context()), "syncs automatically")

	manual := gitopsObject(argoApplication, "argocd", "openbao", map[string]any{"spec": map[string]any{}})
	manual.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "ApplicationSet",
		Name: "platform", UID: "set"}})
	set := gitopsObject(argoApplicationSet, "argocd", "platform", map[string]any{"spec": map[string]any{}})
	p = gitopsProcedure(t, manual, set)
	p.plan.GitOps = managers
	require.ErrorContains(t, p.checkGitOps(t.Context()), "would restore the sync policy",
		"an ApplicationSet that updates its Applications would enable sync again")

	// An ignore rule counts only for the Applications it names.
	for name, rule := range map[string]map[string]any{
		"all Applications": {"jsonPointers": []any{"/spec/syncPolicy"}},
		"this Application": {"name": "openbao", "jqPathExpressions": []any{".spec.syncPolicy.automated"}},
	} {
		set.Object["spec"] = map[string]any{"ignoreApplicationDifferences": []any{rule}}
		p = gitopsProcedure(t, manual, set)
		p.plan.GitOps = managers
		require.NoError(t, p.checkGitOps(t.Context()), name)
	}
	set.Object["spec"] = map[string]any{"ignoreApplicationDifferences": []any{
		map[string]any{"name": "other", "jsonPointers": []any{"/spec/syncPolicy"}}}}
	p = gitopsProcedure(t, manual, set)
	p.plan.GitOps = managers
	require.ErrorContains(t, p.checkGitOps(t.Context()), "would restore the sync policy",
		"a rule for another Application does not keep this one suspended")
}

// applicationsSync applies only where the ApplicationSet controller lets it: a
// controller policy takes precedence unless policy override is enabled.
func TestApplicationSetPolicyFollowsTheController(t *testing.T) {
	application := gitopsObject(argoApplication, "argocd", "openbao", map[string]any{"spec": map[string]any{}})
	application.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "ApplicationSet",
		Name: "platform", UID: "set"}})
	managers := []GitOpsManager{{Kind: "Application", Namespace: "argocd", Name: "openbao"}}
	set := func(policy string) *unstructured.Unstructured {
		spec := map[string]any{}
		if policy != "" {
			spec["syncPolicy"] = map[string]any{"applicationsSync": policy}
		}
		return gitopsObject(argoApplicationSet, "argocd", "platform", map[string]any{"spec": spec})
	}
	controller := func(args []string, params map[string]string) []client.Object {
		env := make([]corev1.EnvVar, 0, 2)
		for name, key := range map[string]string{argoPolicyEnv: argoPolicyKey, argoPolicyOverrideEnv: argoPolicyOverrideKey} {
			env = append(env, corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "argocd-cmd-params-cm"}, Key: key, Optional: ptr.To(true)}}})
		}
		deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: argoApplicationSetControllerName,
			Labels: map[string]string{"app.kubernetes.io/name": argoApplicationSetControllerName}},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: argoApplicationSetControllerName, Args: args, Env: env}}}}}}
		return []client.Object{deployment, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-cmd-params-cm"}, Data: params}}
	}
	for _, test := range []struct {
		name      string
		policy    string
		objects   []client.Object
		accepted  bool
		rejection string
	}{
		{"controller not visible", "create-only", nil, false, "were not found in this cluster"},
		{"controller policy unset", "create-only", controller(nil, nil), true, ""},
		{"no ApplicationSet policy", "", controller(nil, nil), false, "effective policy is sync"},
		{"controller policy takes precedence", "create-only",
			controller(nil, map[string]string{argoPolicyKey: "sync"}), false, "effective policy is sync"},
		{"policy override enabled", "create-only",
			controller(nil, map[string]string{argoPolicyKey: "sync", argoPolicyOverrideKey: "true"}), true, ""},
		{"controller flag", "", controller([]string{"--policy", "create-only"}, nil), true, ""},
		{"flag overrides the ApplicationSet", "create-only",
			controller([]string{"--policy=create-update"}, nil), false, "effective policy is create-update"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := gitopsProcedure(t, append([]client.Object{application, set(test.policy)}, test.objects...)...)
			p.plan.GitOps = managers
			err := p.checkGitOps(t.Context())
			if test.accepted {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.rejection)
		})
	}
}

// Tracking metadata can name a GitOps object in another cluster, where the tool
// cannot check it: the administrator must confirm it is suspended there.
func TestGitOpsManagersOutsideTheClusterNeedConfirmation(t *testing.T) {
	for name, object := range map[string]*metav1.ObjectMeta{
		"Argo CD tracking ID": {Annotations: map[string]string{argoTrackingID: "openbao:openbao.org/OpenBaoCluster:ns/bao"}},
		"Flux HelmRelease": {Labels: map[string]string{fluxHelmName: "openbao-operator",
			fluxHelmNamespace: "flux-system"}},
	} {
		t.Run(name, func(t *testing.T) {
			p := gitopsProcedure(t)
			managers, err := p.gitopsManagers(t.Context(), object)
			require.NoError(t, err)
			require.Len(t, managers, 1)
			p.plan.GitOps = managers
			require.ErrorContains(t, p.checkGitOps(t.Context()), "is not in this cluster")
			p.plan.Config.ExternalGitOpsSuspended = true
			require.NoError(t, p.checkGitOps(t.Context()))
		})
	}
}

func TestHelmInstanceLabelAloneIsNotArgoTracking(t *testing.T) {
	p := gitopsProcedure(t)
	managers, err := p.gitopsManagers(t.Context(), &metav1.ObjectMeta{Labels: map[string]string{
		argoInstanceLabel: "openbao-operator"}})
	require.NoError(t, err)
	require.Empty(t, managers, "Helm sets the same label; it is Argo tracking only if such an Application exists")
	require.Contains(t, (&Procedure{plan: &Plan{GitOps: []GitOpsManager{{Kind: "Application", Namespace: "argocd",
		Name: "openbao", Manages: []string{"the operator"}}}}}).gitopsAfterCommit(), "Application argocd/openbao")
}

func TestPodChangingComparesUIDAndDeletion(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bao-2", Namespace: "openbao", UID: "current"}}
	deleting := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bao-1", Namespace: "openbao", UID: "current",
		DeletionTimestamp: &metav1.Time{Time: time.Now()}, Finalizers: []string{"test.example/hold"}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, deleting).Build()
	k := &Kube{client: c, metadata: c}
	identity := func(kind, name string, uid types.UID) primitives.TransferIdentity {
		return primitives.TransferIdentity{APIVersion: "v1", Kind: kind, Namespace: "openbao", Name: name, UID: uid}
	}
	for name, tt := range map[string]struct {
		resource primitives.TransferIdentity
		changing bool
	}{
		"same Pod":     {identity("Pod", "bao-2", "current"), false},
		"replaced Pod": {identity("Pod", "bao-2", "previous"), true},
		"deleted Pod":  {identity("Pod", "bao-9", "previous"), true},
		"deleting Pod": {identity("Pod", "bao-1", "current"), true},
		"not a Pod":    {identity("Secret", "bao-2", "previous"), false},
	} {
		t.Run(name, func(t *testing.T) {
			changing, err := k.podChanging(t.Context(), tt.resource)
			require.NoError(t, err)
			require.Equal(t, tt.changing, changing)
		})
	}
}

func TestSourceImagePinRequiresDestinationValue(t *testing.T) {
	image := func(values map[string]any) map[string]any { return map[string]any{"image": values} }
	require.NoError(t, sourceImagePinned(map[string]any{}, map[string]any{}), "chart defaults follow the chart")
	require.NoError(t, sourceImagePinned(image(map[string]any{"tag": "", "digest": ""}), map[string]any{}))
	require.ErrorContains(t, sourceImagePinned(image(map[string]any{"digest": "sha256:abc"}), map[string]any{}),
		"pin image.digest")
	require.ErrorContains(t, sourceImagePinned(image(map[string]any{"tag": "0.6.1"}),
		image(map[string]any{"digest": ""})), "pin image.tag")
	require.NoError(t, sourceImagePinned(image(map[string]any{"tag": "0.6.1", "digest": "sha256:abc"}),
		image(map[string]any{"tag": "0.7.0", "digest": ""})))
	helpers := map[string]any{"helperImages": map[string]any{"init": "ghcr.io/kubebao/openbao-init@sha256:abc"},
		"operatorVersion": "0.6.1"}
	require.ErrorContains(t, sourceImagePinned(helpers, map[string]any{"operatorVersion": "0.7.0"}),
		"pin helperImages.init")
	require.ErrorContains(t, sourceImagePinned(helpers, map[string]any{"helperImages": map[string]any{"init": ""}}),
		"pin operatorVersion")
	require.NoError(t, sourceImagePinned(helpers, map[string]any{"helperImages": map[string]any{"init": ""},
		"operatorVersion": ""}))
}

func TestOrphaningDeletionResumesAcceptedRetirement(t *testing.T) {
	object := func(deleting bool, finalizers ...string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetKind(clusterKind)
		u.SetNamespace("openbao")
		u.SetName("bao")
		u.SetFinalizers(finalizers)
		if deleting {
			now := metav1.Now()
			u.SetDeletionTimestamp(&now)
		}
		return u
	}
	for name, tt := range map[string]struct {
		object   *unstructured.Unstructured
		deleting bool
		fails    bool
	}{
		"not deleted":                  {object: object(false, clusterFinalizer)},
		"orphaning after a lost reply": {object: object(true, metav1.FinalizerOrphanDependents), deleting: true},
		"orphaned, about to vanish":    {object: object(true), deleting: true},
		"deleted by someone else":      {object: object(true, clusterFinalizer), fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			deleting, err := orphaningDeletion(tt.object)
			require.Equal(t, tt.fails, err != nil, "error: %v", err)
			require.Equal(t, tt.deleting, deleting)
		})
	}
}

func TestAfterCommitOrdersHelmReleaseResumption(t *testing.T) {
	afterCommit := func(kind string) string {
		return (&Procedure{plan: &Plan{GitOps: []GitOpsManager{{Kind: kind, Namespace: "flux-system",
			Name: "openbao-operator", Manages: []string{"the operator"}}}}}).gitopsAfterCommit()
	}
	require.Contains(t, afterCommit(fluxHelmRelease.Kind), "only after its chart source serves the destination chart")
	require.NotContains(t, afterCommit(fluxKustomization.Kind), "chart source")
}

func TestCheckpointPathsResolveSymlinks(t *testing.T) {
	real := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(real, "state", "checkpoints", "held"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(real, "release"), 0o700))
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))
	kustomization := filepath.Join(link, "state", "checkpoints", "held")
	resource, err := checkpointResource(kustomization, filepath.Join(real, "release"))
	require.NoError(t, err)
	resolved, err := filepath.EvalSymlinks(kustomization)
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(resolved, resource), "the path holds from the resolved directory, as Kustomize reads it")
}

func TestStopAfterMustNameAStep(t *testing.T) {
	p := &Procedure{plan: &Plan{}, log: io.Discard, stopAfter: "migration-chart"}
	err := p.runSteps(t.Context(), []struct {
		name string
		run  func() (string, error)
	}{{"install-destination", func() (string, error) { return "", nil }}})
	require.ErrorContains(t, err, `--stop-after "migration-chart" is not a step; the steps are install-destination`)
}

// The controller logs in with a JWT only where the operator does; a legacy
// Cluster without self-init OIDC uses its root token Secret instead.
func TestControllerUsesJWTMirrorsTheOperator(t *testing.T) {
	cluster := func(spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	}
	require.True(t, controllerUsesJWT(cluster(map[string]any{"controllerJWTMode": "Target"})))
	require.True(t, controllerUsesJWT(cluster(map[string]any{
		"selfInit": map[string]any{"enabled": true, "oidc": map[string]any{"enabled": true}}})))
	require.False(t, controllerUsesJWT(cluster(map[string]any{})), "legacy Shared without self-init")
	require.False(t, controllerUsesJWT(cluster(map[string]any{"selfInit": map[string]any{"enabled": true}})),
		"self-init without OIDC bootstrap")
}

// A source created after planning would stay behind without an operator: the
// source group must hold exactly the plan, the inactive group only planned ones.
func TestInventoryMatchesThePlan(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, version := range []string{primitives.PlanSourceAPIVersion, primitives.PlanDestinationAPIVersion} {
		for _, kind := range []string{clusterKind, restoreKind, tenantKind} {
			gvk := schema.FromAPIVersionAndKind(version, kind)
			scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
			scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(kind+"List"), &unstructured.UnstructuredList{})
		}
	}
	cluster := func(version, name string) *unstructured.Unstructured {
		object := &unstructured.Unstructured{}
		object.SetGroupVersionKind(schema.FromAPIVersionAndKind(version, clusterKind))
		object.SetNamespace("openbao")
		object.SetName(name)
		return object
	}
	procedure := func(objects ...client.Object) *Procedure {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
		return &Procedure{kube: &Kube{client: c}, plan: &Plan{
			Operator: OperatorPlan{Mode: tenancyMulti},
			Clusters: []ClusterPlan{{Namespace: "openbao", Name: "bao"}},
		}}
	}
	p := procedure(cluster(primitives.PlanSourceAPIVersion, "bao"))
	require.NoError(t, p.inventoryMatches(t.Context(), primitives.PlanSourceAPIVersion, false))
	require.ErrorContains(t, p.inventoryMatches(t.Context(), primitives.PlanDestinationAPIVersion, false),
		"0 kubebao.org/v1alpha1 object(s) exist, 1 were planned")
	require.NoError(t, p.inventoryMatches(t.Context(), primitives.PlanDestinationAPIVersion, true),
		"the inactive group may already be retired")

	p = procedure(cluster(primitives.PlanSourceAPIVersion, "bao"), cluster(primitives.PlanSourceAPIVersion, "late"))
	require.ErrorContains(t, p.inventoryMatches(t.Context(), primitives.PlanSourceAPIVersion, false),
		"OpenBaoCluster openbao/late is not part of this migration")
	require.ErrorContains(t, p.inventoryMatches(t.Context(), primitives.PlanSourceAPIVersion, true),
		"would be left without an operator")
}

// Commit refuses a source whose specification changed after planning, because
// the destination holds the planned one. Rollback accepts it: the source
// operator takes the Cluster back as it is.
func TestRollbackAcceptsAChangedSourceSpecification(t *testing.T) {
	scheme := runtime.NewScheme()
	gvk := schema.FromAPIVersionAndKind(primitives.PlanSourceAPIVersion, clusterKind)
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(clusterKind+"List"), &unstructured.UnstructuredList{})
	source := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"replicas": int64(3)}}}
	source.SetGroupVersionKind(gvk)
	source.SetNamespace("openbao")
	source.SetName("bao")
	source.SetUID("source")
	planned := ClusterPlan{Namespace: "openbao", Name: "bao", SourceUID: "source",
		SpecDigest: digestJSON(map[string]any{"replicas": int64(1)})}
	p := &Procedure{kube: &Kube{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(source).Build()},
		plan: &Plan{Clusters: []ClusterPlan{planned}}}
	_, err := p.sourceIntact(t.Context(), planned)
	require.ErrorContains(t, err, "specification changed since planning")
	require.NoError(t, p.rollbackPossible(t.Context()))

	p.kube.client = fake.NewClientBuilder().WithScheme(scheme).Build()
	require.ErrorContains(t, p.rollbackPossible(t.Context()), "was retired")
}

// A release binary migrates only to its own release; a development build to any.
func TestDestinationMustBeTheBinarysRelease(t *testing.T) {
	require.NoError(t, destinationRelease("0.9.0"), "a development build accepts any destination")
	defer func(previous string) { version = previous }(version)
	version = "0.7.0"
	require.NoError(t, destinationRelease("0.7.0"))
	require.ErrorContains(t, destinationRelease("0.7.1"), `use the api-migration binary of release "0.7.1"`)
	require.Error(t, destinationRelease(""), "a destination without a release is refused")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Chart.yaml"),
		[]byte("apiVersion: v2\nname: openbao-operator\nversion: 0.7.0\nappVersion: \"0.7.0\"\n"), 0o600))
	release, err := chartAppVersion(dir)
	require.NoError(t, err)
	require.Equal(t, "0.7.0", release)
}

func TestEdgeBinaryRequiresMatchingDestinationAndSavedPlan(t *testing.T) {
	defer func(previous string) { version = previous }(version)
	version = "edge-aaaaaaaaaaaa"
	require.NoError(t, destinationRelease(version))
	for _, target := range []string{"edge-bbbbbbbbbbbb", "edge", "0.7.0", ""} {
		require.Error(t, destinationRelease(target))
	}
	for _, command := range []string{commandMigrate, commandCommit, commandRollback, commandStatus} {
		t.Run(command, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			state, err := openState(dir, true)
			require.NoError(t, err)
			require.NoError(t, state.Save(&Plan{Version: planVersion, DestinationVersion: "edge-bbbbbbbbbbbb"}))
			state.Close()
			err = run(t.Context(), []string{command, "--state", dir}, io.Discard, io.Discard)
			require.ErrorContains(t, err, `saved plan`)
			require.ErrorContains(t, err, `use the api-migration binary of release "edge-bbbbbbbbbbbb"`)
		})
	}
}

func TestRestampRevisionsMovesOnlyTheRetiredOwnersHistory(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	owner := primitives.TransferIdentity{APIVersion: testSourceAPI, Kind: clusterKind, Namespace: "openbao",
		Name: "bao", UID: "source"}
	set := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: "openbao", UID: "set",
		Labels: map[string]string{constants.LabelOpenBaoCluster: "bao"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: owner.APIVersion, Kind: owner.Kind, Name: owner.Name,
			UID: owner.UID, Controller: ptr.To(true)}}}}
	revision := func(name string, ownerUID string, refs ...metav1.OwnerReference) *appsv1.ControllerRevision {
		return &appsv1.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "openbao",
			Annotations: map[string]string{constants.AnnotationOpenBaoOwnerUID: ownerUID}, OwnerReferences: refs}}
	}
	setRef := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "bao", UID: "set",
		Controller: ptr.To(true)}
	otherSet := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "other", UID: "other",
		Controller: ptr.To(true)}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(set,
		revision("destination-history", "destination", setRef),
		revision("source-history", "source", setRef),
		revision("foreign-set", "destination", otherSet),
		revision("unowned", "destination"),
	).Build()
	k := &Kube{client: c}

	restamped, err := restampRevisions(t.Context(), k, "destination", owner)
	require.NoError(t, err)
	require.Equal(t, 1, restamped)
	for name, want := range map[string]string{"destination-history": "source", "source-history": "source",
		"foreign-set": "destination", "unowned": "destination"} {
		got := &appsv1.ControllerRevision{}
		require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "openbao", Name: name}, got))
		require.Equal(t, want, got.Annotations[constants.AnnotationOpenBaoOwnerUID], name)
	}

	restamped, err = restampRevisions(t.Context(), k, "destination", owner)
	require.NoError(t, err)
	require.Zero(t, restamped, "a rerun finds nothing left to move")
}

func TestCheckpointNamesUnmetConditions(t *testing.T) {
	observed := operatorObservation{group: "kubebao.org", stopped: true}
	require.Equal(t, []string{"the kubebao.org CRDs are not installed"}, observed.unmet(stateStopped))
	observed.crds = true
	require.Empty(t, observed.unmet(stateStopped))
	require.True(t, observed.matches(stateStopped))
	require.Equal(t, []string{"the admission policies are for kubebao.org, not openbao.org",
		"an operator Deployment is not ready at its planned replicas"}, observed.unmet(stateSource))
	require.Equal(t, []string{"no operator admission policies are installed",
		"the controller does not run with the API migration hold",
		"an operator Deployment is not ready at its planned replicas"}, operatorObservation{}.unmet(stateHeld))

	failure := &checkpointError{State: "stopped", Instructions: "stopped.md",
		Unmet: []string{"the kubebao.org CRDs are not installed"}}
	require.Equal(t, "waiting for the stopped operator state (the kubebao.org CRDs are not installed): "+
		"apply it as described in stopped.md, then run the command again", failure.Error())
}
