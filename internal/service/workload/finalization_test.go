package workload

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestFinalizeDeletionBoundaries(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*openbaov1alpha1.OpenBaoCluster, *appsv1.StatefulSet, *corev1.Pod)
		deleting  bool
		deleteSet bool
		deletePod bool
		wantError bool
	}{
		{name: "foreground-request", deleteSet: true},
		{name: "paused-deletion-is-authorized", change: func(c *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, _ *corev1.Pod) {
			c.Spec.Paused = true
		}, deleteSet: true},
		{name: "non-deleting-cluster", change: func(c *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, _ *corev1.Pod) {
			c.DeletionTimestamp = nil
		}, wantError: true},
		{name: "restore-hold", change: func(c *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, _ *corev1.Pod) {
			c.Annotations = map[string]string{constants.AnnotationRestoreHold: "restore"}
		}, wantError: true},
		{name: "foreign-statefulset", change: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet, _ *corev1.Pod) {
			s.OwnerReferences[0].UID = "foreign"
		}, wantError: true},
		{name: "missing-statefulset-provenance", change: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet, _ *corev1.Pod) {
			s.Annotations = nil
		}},
		{name: "retain-refuses-deleting-pvcs", change: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet, _ *corev1.Pod) {
			s.Spec.PersistentVolumeClaimRetentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
				WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType}
		}, wantError: true},
		{name: "failed-pod", deleting: true, deletePod: true},
		{name: "succeeded-pod", deleting: true, deletePod: true, change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.Phase = corev1.PodSucceeded
		}},
		{name: "running-pod", deleting: true, change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
		}},
		{name: "unknown-pod", deleting: true, change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.Phase = corev1.PodUnknown
		}},
		{name: "pending-pod", deleting: true, change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.Phase = corev1.PodPending
		}},
		{name: "pod-not-deleting", deleting: true, change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.DeletionTimestamp = nil
		}},
		{name: "foreign-pod-finalizer", deleting: true, change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Finalizers = []string{"example.com/hold"}
		}},
		{name: "running-init-container", deleting: true, change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.InitContainerStatuses = activeCleanupContainer()
		}},
		{name: "foreign-pod-owner", deleting: true, change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.OwnerReferences[0].UID = "foreign"
		}},
		{name: "background-deletion-is-not-authority", deleting: true, change: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet, _ *corev1.Pod) {
			s.Finalizers = []string{"example.com/hold"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cluster, set, pod := terminalCleanupFixture()
			stamp := metav1.Now()
			cluster.DeletionTimestamp = &stamp
			cluster.Finalizers = []string{openbaov1alpha1.OpenBaoClusterFinalizer}
			if test.deleting {
				set.DeletionTimestamp = &stamp
				set.Finalizers = []string{metav1.FinalizerDeleteDependents}
			}
			if test.change != nil {
				test.change(cluster, set, pod)
			}
			setDeletes, podDeletes := 0, 0
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cluster, set).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if list, ok := list.(*corev1.PodList); ok {
							list.Items = []corev1.Pod{*pod.DeepCopy()}
							return nil
						}
						return c.List(ctx, list, opts...)
					},
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if target, ok := obj.(*corev1.Pod); ok {
							*target = *pod.DeepCopy()
							return nil
						}
						return c.Get(ctx, key, obj, opts...)
					},
					Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						options := (&client.DeleteOptions{}).ApplyOptions(opts)
						require.Equal(t, obj.GetUID(), *options.Preconditions.UID)
						require.Equal(t, obj.GetResourceVersion(), *options.Preconditions.ResourceVersion)
						switch obj.(type) {
						case *appsv1.StatefulSet:
							setDeletes++
							require.Equal(t, metav1.DeletePropagationForeground, *options.PropagationPolicy)
						case *corev1.Pod:
							podDeletes++
							require.EqualValues(t, 0, *options.GracePeriodSeconds)
						}
						return nil
					},
				}).Build()
			complete, err := NewManager(c, testScheme, "").FinalizeDeletion(t.Context(), logr.Discard(), cluster)
			if test.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.False(t, complete)
			require.Equal(t, test.deleteSet, setDeletes == 1)
			require.Equal(t, test.deletePod, podDeletes == 1)
		})
	}
}

func TestFinalizeDeletionCompletesOnlyWithoutWorkloads(t *testing.T) {
	cluster, _, _ := terminalCleanupFixture()
	stamp := metav1.Now()
	cluster.DeletionTimestamp = &stamp
	cluster.Finalizers = []string{openbaov1alpha1.OpenBaoClusterFinalizer}
	c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cluster).Build()
	complete, err := NewManager(c, testScheme, "").FinalizeDeletion(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.True(t, complete)
}
