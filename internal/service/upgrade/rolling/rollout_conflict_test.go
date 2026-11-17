package rolling

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

func TestReconcileUpgradeExecutionPartitionErrorPolicy(t *testing.T) {
	for _, tc := range []struct {
		name             string
		sourceGeneration string
		patchError       error
		wantRetry        bool
		retryOnce        bool
		expireAfterRetry bool
	}{
		{
			name:             "resource version conflict",
			sourceGeneration: "2",
			patchError:       apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, "example", errors.New("modified")),
			wantRetry:        true,
			retryOnce:        true,
		},
		{
			name:             "target template not observed",
			sourceGeneration: "1",
			wantRetry:        true,
		},
		{
			name:             "persistent conflict reaches upgrade timeout",
			sourceGeneration: "2",
			patchError:       apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, "example", errors.New("modified")),
			wantRetry:        true,
			expireAfterRetry: true,
		},
		{
			name:             "permanent patch rejection",
			sourceGeneration: "2",
			patchError:       apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, "example", errors.New("denied")),
		},
		{
			name:             "non-conflict patch API server error",
			sourceGeneration: "2",
			patchError:       apierrors.NewInternalError(errors.New("server error")),
		},
		{
			name:             "non-conflict patch request deadline",
			sourceGeneration: "2",
			patchError:       context.DeadlineExceeded,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, appsv1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "ns", Generation: 2},
				Spec:       openbaov1alpha1.OpenBaoClusterSpec{Replicas: 1, Version: "2.6.2"},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					CurrentVersion: "2.6.1",
					Upgrade: &openbaov1alpha1.UpgradeProgress{
						FromVersion: "2.6.1", TargetVersion: "2.6.2", CurrentPartition: 1, StartedAt: ptr.To(metav1.Now()),
					},
				},
			}
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "example", Namespace: "ns", Generation: 7,
					Annotations: map[string]string{constants.AnnotationClusterGeneration: tc.sourceGeneration},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(cluster.Spec.Replicas),
					UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
						Type:          appsv1.RollingUpdateStatefulSetStrategyType,
						RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To(int32(1))},
					},
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: "openbao/openbao:2.6.2"}}}},
				},
				Status: appsv1.StatefulSetStatus{ObservedGeneration: 7, UpdateRevision: "target-revision"},
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "example-0", Namespace: "ns", Labels: map[string]string{appsv1.StatefulSetRevisionLabel: "source-revision"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: "openbao/openbao:2.6.1"}}},
			}
			patchAttempts := 0
			patchFailuresRemaining := 1
			statusWrites := 0
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts, pod).
				WithInterceptorFuncs(interceptor.Funcs{
					Patch: func(ctx context.Context, delegate client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						if _, ok := obj.(*appsv1.StatefulSet); ok {
							patchAttempts++
							if tc.patchError != nil && patchFailuresRemaining > 0 {
								patchFailuresRemaining--
								return tc.patchError
							}
						}
						return delegate.Patch(ctx, obj, patch, opts...)
					},
				}).Build()
			recorder := events.NewFakeRecorder(2)
			m := &Manager{
				client:   c,
				recorder: recorder,
				adminOpsMutator: func(_ context.Context, obj *openbaov1alpha1.OpenBaoCluster, mutate func(*openbaov1alpha1.OpenBaoCluster) error, _ adminops.OwnershipPolicy) error {
					statusWrites++
					return mutate(obj)
				},
			}

			result, err := m.reconcileUpgradeExecution(t.Context(), logr.Discard(), cluster, nil, string(openbaov1alpha1.UpdateStrategyRollingUpdate))
			if tc.wantRetry {
				require.NoError(t, err)
				require.Equal(t, constants.RequeueShort, result.RequeueAfter)
				require.Nil(t, cluster.Status.Upgrade.Failure)
				require.Zero(t, statusWrites)
				expectEventContains(t, recorder, "Warning", upgrade.ReasonRollingPartitionRetry)
			} else {
				require.Error(t, err)
				require.Equal(t, upgrade.ReasonUpgradeFailed, cluster.Status.Upgrade.Failure.Reason)
				require.Equal(t, 1, statusWrites)
				expectEventContains(t, recorder, "Warning", upgrade.ReasonUpgradeFailed)
			}
			if tc.sourceGeneration == "2" {
				require.Equal(t, 1, patchAttempts)
			} else {
				require.Zero(t, patchAttempts)
			}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
			require.Equal(t, int32(1), *sts.Spec.UpdateStrategy.RollingUpdate.Partition)

			if tc.retryOnce {
				result, err = m.reconcileUpgradeExecution(t.Context(), logr.Discard(), cluster, nil, string(openbaov1alpha1.UpdateStrategyRollingUpdate))
				require.NoError(t, err)
				require.Equal(t, constants.RequeueShort, result.RequeueAfter)
				require.Nil(t, cluster.Status.Upgrade.Failure)
				require.Equal(t, 1, statusWrites)
				require.Equal(t, 2, patchAttempts)
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
				require.Equal(t, int32(0), *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
			}
			if tc.expireAfterRetry {
				startedAt := metav1.NewTime(time.Now().Add(-upgrade.DefaultPodReadyTimeout - time.Second))
				cluster.Status.Upgrade.StartedAt = &startedAt
				_, err = m.reconcileUpgradeExecution(t.Context(), logr.Discard(), cluster, nil, string(openbaov1alpha1.UpdateStrategyRollingUpdate))
				require.Error(t, err)
				require.NotNil(t, cluster.Status.Upgrade.Failure)
				require.Equal(t, 1, statusWrites)
				require.Equal(t, 1, patchAttempts)
			}
		})
	}
}
