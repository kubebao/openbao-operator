//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/workload"
)

func TestWorkloadFinalizationRealAPI(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	for _, scenario := range []string{"failed", "succeeded", "running", "pending", "finalizer", "paused",
		"pod-version-race", "replacement-race", "cluster-replacement", "restore-hold-race"} {
		t.Run(scenario, func(t *testing.T) {
			cluster, set, pod := createTerminalCleanupFixture(t, scenario)
			cluster.Finalizers = []string{openbaov1alpha1.OpenBaoClusterFinalizer}
			if scenario == "paused" {
				cluster.Spec.Paused = true
			}
			require.NoError(t, k8sClient.Update(ctx, cluster))
			require.NoError(t, k8sClient.Delete(ctx, cluster))
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
			operator := newControllerClient(t)
			podDeletes := 0
			writer := interceptor.NewClient(operator, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					options := (&client.DeleteOptions{}).ApplyOptions(opts)
					require.Equal(t, obj.GetUID(), *options.Preconditions.UID)
					require.Equal(t, obj.GetResourceVersion(), *options.Preconditions.ResourceVersion)
					if _, ok := obj.(*appsv1.StatefulSet); ok {
						require.Equal(t, metav1.DeletePropagationForeground, *options.PropagationPolicy)
						return c.Delete(ctx, obj, opts...)
					}
					podDeletes++
					require.EqualValues(t, 0, *options.GracePeriodSeconds)
					if scenario == "pod-version-race" {
						current := &corev1.Pod{}
						require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), current))
						current.Annotations = map[string]string{"test.openbao.org/race": "changed"}
						require.NoError(t, c.Update(ctx, current))
					}
					if scenario == "replacement-race" {
						require.NoError(t, c.Delete(ctx, pod.DeepCopy(), client.GracePeriodSeconds(0), client.Preconditions{UID: &pod.UID}))
						replacement := pod.DeepCopy()
						replacement.UID, replacement.ResourceVersion = "", ""
						replacement.DeletionTimestamp, replacement.DeletionGracePeriodSeconds = nil, nil
						replacement.Status = corev1.PodStatus{}
						require.NoError(t, c.Create(ctx, replacement))
					}
					return c.Delete(ctx, obj, opts...)
				},
			})
			manager := workload.NewManager(writer, k8sScheme, "").WithReader(operator)
			complete, err := manager.FinalizeDeletion(ctx, logr.Discard(), cluster)
			require.NoError(t, err)
			require.False(t, complete)
			require.NoError(t, operator.Get(ctx, client.ObjectKeyFromObject(set), set))
			require.NotNil(t, set.DeletionTimestamp)
			require.Contains(t, set.Finalizers, metav1.FinalizerDeleteDependents)
			require.Zero(t, podDeletes)

			if scenario == "cluster-replacement" {
				stored := cluster.DeepCopy()
				stored.Finalizers = nil
				require.NoError(t, k8sClient.Update(ctx, stored))
				replacement := createMinimalCluster(t, cluster.Namespace, cluster.Name)
				require.NotEqual(t, cluster.UID, replacement.UID)
			}
			if scenario == "restore-hold-race" {
				stored := cluster.DeepCopy()
				stored.Annotations = map[string]string{constants.AnnotationRestoreHold: "recovery"}
				require.NoError(t, k8sClient.Update(ctx, stored))
			}
			complete, err = manager.FinalizeDeletion(ctx, logr.Discard(), cluster)
			require.False(t, complete)
			switch scenario {
			case "pod-version-race", "replacement-race":
				require.True(t, apierrors.IsConflict(err), "conditional Pod deletion must fail: %v", err)
			case "cluster-replacement", "restore-hold-race":
				require.Error(t, err)
			default:
				require.NoError(t, err)
			}
			stored := &corev1.Pod{}
			getErr := operator.Get(ctx, client.ObjectKeyFromObject(pod), stored)
			if scenario == "failed" || scenario == "succeeded" || scenario == "paused" {
				require.True(t, apierrors.IsNotFound(getErr))
				require.Equal(t, 1, podDeletes)
				// Envtest has no garbage collector. Emulate only its completion
				// of the foreground StatefulSet after its last Pod is gone.
				set.Finalizers = nil
				require.NoError(t, operator.Update(ctx, set))
				complete, err = manager.FinalizeDeletion(ctx, logr.Discard(), cluster)
				require.NoError(t, err)
				require.True(t, complete)
			} else {
				require.NoError(t, getErr)
				if scenario == "replacement-race" {
					require.NotEqual(t, pod.UID, stored.UID)
				} else {
					require.Equal(t, pod.UID, stored.UID)
					require.Equal(t, pod.Finalizers, stored.Finalizers)
				}
			}
		})
	}
}

func TestWorkloadFinalizationRejectsStatefulSetWriteRace(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "resource-version", true: "replacement"}[replacement], func(t *testing.T) {
			cluster, set, _ := createTerminalCleanupFixture(t, "failed")
			cluster.Finalizers = []string{openbaov1alpha1.OpenBaoClusterFinalizer}
			require.NoError(t, k8sClient.Update(ctx, cluster))
			require.NoError(t, k8sClient.Delete(ctx, cluster))
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
			operator := newControllerClient(t)
			writer := interceptor.NewClient(operator, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					if replacement {
						require.NoError(t, c.Delete(ctx, set.DeepCopy()))
						next := set.DeepCopy()
						next.UID, next.ResourceVersion = "", ""
						require.NoError(t, c.Create(ctx, next))
					} else {
						current := &appsv1.StatefulSet{}
						require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(set), current))
						current.Annotations["test.openbao.org/race"] = "changed"
						require.NoError(t, c.Update(ctx, current))
					}
					return c.Delete(ctx, obj, opts...)
				},
			})
			complete, err := workload.NewManager(writer, k8sScheme, "").WithReader(operator).
				FinalizeDeletion(ctx, logr.Discard(), cluster)
			require.False(t, complete)
			require.True(t, apierrors.IsConflict(err), "conditional StatefulSet deletion must reject a race: %v", err)
			stored := &appsv1.StatefulSet{}
			require.NoError(t, operator.Get(ctx, client.ObjectKeyFromObject(set), stored))
			require.Nil(t, stored.DeletionTimestamp)
			if replacement {
				require.NotEqual(t, set.UID, stored.UID)
			}
		})
	}
}
