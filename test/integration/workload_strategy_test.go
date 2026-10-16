//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

func TestWorkloadStrategyLockPrecedesTemplateApply(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	for _, outcome := range []string{"success", "conflict", "lost response", "replacement"} {
		t.Run(outcome, func(t *testing.T) {
			namespace := newTestNamespace(t)
			cluster := createMinimalCluster(t, namespace, "strategy-order")
			createTLSSecret(t, namespace, cluster.Name)
			updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) {
				status.Initialized = true
				status.CurrentVersion = cluster.Spec.Version
			})
			controller := newControllerClient(t)
			require.NoError(t, reconcileClusterResources(ctx, logr.Discard(), controller, k8sScheme,
				cluster, newTestStatefulSetSpec(cluster)))
			key := client.ObjectKeyFromObject(cluster)
			original := &appsv1.StatefulSet{}
			require.NoError(t, controller.Get(ctx, key, original))
			oldImage := original.Spec.Template.Spec.Containers[0].Image
			uid := original.UID

			require.NoError(t, k8sClient.Get(ctx, key, cluster))
			cluster.Spec.Version = "2.6.4"
			cluster.Spec.Image = "openbao/openbao:2.6.4"
			require.NoError(t, k8sClient.Update(ctx, cluster))
			spec := newTestStatefulSetSpec(cluster)
			patches := 0
			wrapped := interceptor.NewClient(controller, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object,
					patch client.Patch, opts ...client.PatchOption) error {
					if _, ok := obj.(*appsv1.StatefulSet); !ok {
						return c.Patch(ctx, obj, patch, opts...)
					}
					data, err := patch.Data(obj)
					require.NoError(t, err)
					var fields struct {
						Spec struct {
							UpdateStrategy *json.RawMessage `json:"updateStrategy"`
						} `json:"spec"`
					}
					require.NoError(t, json.Unmarshal(data, &fields))
					if fields.Spec.UpdateStrategy == nil {
						return c.Patch(ctx, obj, patch, opts...)
					}
					patches++
					current := &appsv1.StatefulSet{}
					require.NoError(t, c.Get(ctx, key, current))
					require.Equal(t, oldImage, current.Spec.Template.Spec.Containers[0].Image,
						"partition must be locked before applying the new template")
					if patches == 1 && outcome == "conflict" {
						return apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, obj.GetName(), nil)
					}
					if patches == 1 && outcome == "replacement" {
						require.NoError(t, c.Delete(ctx, current, client.Preconditions{UID: &current.UID}))
						replacement := current.DeepCopy()
						replacement.ObjectMeta = metav1.ObjectMeta{
							Name: current.Name, Namespace: current.Namespace,
							Labels: current.Labels, Annotations: current.Annotations, OwnerReferences: current.OwnerReferences,
						}
						require.NoError(t, c.Create(ctx, replacement))
						require.NotEqual(t, uid, replacement.UID)
						uid = replacement.UID
					}
					if err := c.Patch(ctx, obj, patch, opts...); err != nil {
						return err
					}
					if patches == 1 && outcome == "lost response" {
						return apierrors.NewTimeoutError("partition patch response lost", 1)
					}
					return nil
				},
			})
			err := reconcileClusterResources(ctx, logr.Discard(), wrapped, k8sScheme, cluster, spec)
			if outcome != "success" {
				if outcome == "conflict" || outcome == "replacement" {
					require.True(t, apierrors.IsConflict(err), "expected conflict, got %v", err)
				} else {
					require.True(t, apierrors.IsTimeout(err), "expected timeout, got %v", err)
				}
				unchanged := &appsv1.StatefulSet{}
				require.NoError(t, controller.Get(ctx, key, unchanged))
				require.Equal(t, oldImage, unchanged.Spec.Template.Spec.Containers[0].Image)
				// The helper constructs a fresh manager. Only API state survives.
				err = reconcileClusterResources(ctx, logr.Discard(), wrapped, k8sScheme, cluster, spec)
			}
			require.NoError(t, err)
			expectedPatches := 1
			if outcome == "conflict" || outcome == "replacement" {
				expectedPatches = 2
			}
			require.Equal(t, expectedPatches, patches)
			updated := &appsv1.StatefulSet{}
			require.NoError(t, controller.Get(ctx, key, updated))
			require.Equal(t, uid, updated.UID)
			require.Equal(t, cluster.Spec.Image, updated.Spec.Template.Spec.Containers[0].Image)
			require.Equal(t, ptr.To(cluster.Spec.Replicas), updated.Spec.UpdateStrategy.RollingUpdate.Partition)

			// AdminOps now owns progress. Workload reconciliation must retain its
			// partition even while currentVersion still differs from spec.version.
			updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) {
				status.Upgrade = &api.UpgradeProgress{
					FromVersion: status.CurrentVersion, TargetVersion: cluster.Spec.Version, CurrentPartition: 1,
				}
			})
			before := updated.DeepCopy()
			updated.Spec.UpdateStrategy.RollingUpdate.Partition = ptr.To(int32(1))
			require.NoError(t, controller.Patch(ctx, updated, client.MergeFrom(before)))
			require.NoError(t, reconcileClusterResources(ctx, logr.Discard(), wrapped, k8sScheme, cluster, spec))
			require.Equal(t, expectedPatches, patches, "active upgrade must not trigger a workload strategy patch")
			require.NoError(t, controller.Get(ctx, key, updated))
			require.Equal(t, ptr.To(int32(1)), updated.Spec.UpdateStrategy.RollingUpdate.Partition)
		})
	}
}

func TestWorkloadIdleStrategyTransitionClearsRollingPayload(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)
	cluster := createMinimalCluster(t, namespace, "idle-strategy")
	createTLSSecret(t, namespace, cluster.Name)
	controller := newControllerClient(t)
	require.NoError(t, reconcileClusterResources(ctx, logr.Discard(), controller, k8sScheme,
		cluster, newTestStatefulSetSpec(cluster)))

	// Project an accepted BlueGreen transition at the workload boundary. The
	// strategy admission and acceptance workflow has separate integration tests.
	cluster.Status.AcceptedUpgradeStrategy = api.UpdateStrategyBlueGreen
	manager := workloadsvc.NewManager(controller, k8sScheme, "")
	require.NoError(t, manager.EnsureStatefulSet(ctx, logr.Discard(), cluster,
		"listener \"tcp\" {}", newTestStatefulSetSpec(cluster)))
	current := &appsv1.StatefulSet{}
	require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(cluster), current))
	require.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, current.Spec.UpdateStrategy.Type)
	require.Nil(t, current.Spec.UpdateStrategy.RollingUpdate)
}
