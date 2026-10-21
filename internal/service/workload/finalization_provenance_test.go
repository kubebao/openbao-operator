package workload

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

func TestFinalizationLegacyProvenanceBoundaries(t *testing.T) {
	const foreignOwnerUID = "foreign"
	for _, scenario := range []string{"legacy", "empty-map", "other-annotation", "paused", "already-deleting",
		"wrong-owner-uid", "wrong-owner-kind", "wrong-owner-api", "wrong-owner-name", "not-controller", "wrong-namespace",
		"wrong-managed-label", "wrong-cluster-label", "conflicting-provenance", "empty-provenance", "restore-held", "cluster-active"} {
		t.Run(scenario, func(t *testing.T) {
			cluster, set, _ := terminalCleanupFixture()
			stamp := metav1.Now()
			cluster.DeletionTimestamp = &stamp
			cluster.Finalizers = []string{api.OpenBaoClusterFinalizer}
			set.Annotations = nil
			switch scenario {
			case "empty-map":
				set.Annotations = map[string]string{}
			case "other-annotation":
				set.Annotations = map[string]string{"example.com/keep": "value"}
			case "paused":
				cluster.Spec.Paused = true
			case "already-deleting":
				set.DeletionTimestamp = &stamp
				set.Finalizers = []string{metav1.FinalizerDeleteDependents, "example.com/hold"}
			case "wrong-owner-uid":
				set.OwnerReferences[0].UID = foreignOwnerUID
			case "wrong-owner-kind":
				set.OwnerReferences[0].Kind = "Other"
			case "wrong-owner-api":
				set.OwnerReferences[0].APIVersion = "other.example.com/v1"
			case "wrong-owner-name":
				set.OwnerReferences[0].Name = "different-owner-name"
			case "not-controller":
				set.OwnerReferences[0].Controller = nil
			case "wrong-namespace":
				set.Namespace = "different-tenant-namespace"
			case "wrong-managed-label":
				set.Labels[constants.LabelAppManagedBy] = "different-manager"
			case "wrong-cluster-label":
				set.Labels[constants.LabelOpenBaoCluster] = "different-cluster"
			case "conflicting-provenance":
				set.Annotations = map[string]string{constants.AnnotationOpenBaoOwnerUID: foreignOwnerUID}
			case "empty-provenance":
				set.Annotations = map[string]string{constants.AnnotationOpenBaoOwnerUID: ""}
			case "restore-held":
				cluster.Annotations = map[string]string{constants.AnnotationRestoreHold: "recovery"}
			case "cluster-active":
				cluster.DeletionTimestamp = nil
			}
			before := set.DeepCopy()
			patches := 0
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cluster, set).
				WithInterceptorFuncs(interceptor.Funcs{
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						patches++
						data, err := patch.Data(obj)
						require.NoError(t, err)
						var operations []map[string]any
						require.NoError(t, json.Unmarshal(data, &operations))
						require.Equal(t, map[string]any{"op": "test", "path": "/metadata/uid", "value": string(set.UID)}, operations[0])
						require.Equal(t, map[string]any{"op": "test", "path": "/metadata/resourceVersion", "value": obj.GetResourceVersion()}, operations[1])
						return c.Patch(ctx, obj, patch, opts...)
					},
					Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
						t.Fatal("provenance migration must not delete workloads")
						return nil
					},
				}).Build()
			stamped, err := NewManager(c, testScheme, "").backfillFinalizingStatefulSetProvenance(t.Context(), cluster, set)
			allowed := scenario == "legacy" || scenario == "empty-map" || scenario == "other-annotation" || scenario == "paused" || scenario == "already-deleting"
			if allowed || scenario == "conflicting-provenance" || scenario == "empty-provenance" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, allowed, stamped)
			require.Equal(t, map[bool]int{true: 1, false: 0}[allowed], patches)
			stored := &appsv1.StatefulSet{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(set), stored))
			require.Equal(t, before.Spec, stored.Spec)
			require.Equal(t, before.OwnerReferences, stored.OwnerReferences)
			require.Equal(t, before.Finalizers, stored.Finalizers)
			if allowed {
				require.True(t, resourceownership.HasManagedControllerOwnerProof(stored, cluster, api.GroupVersion.WithKind("OpenBaoCluster")))
				for key, value := range before.Annotations {
					require.Equal(t, value, stored.Annotations[key])
				}
			} else {
				require.Equal(t, before.Annotations, stored.Annotations)
			}
		})
	}
}
