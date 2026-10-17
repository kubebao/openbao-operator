package rolling

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

func retryExecutionCluster() *api.OpenBaoCluster {
	started := metav1.NewTime(time.Now().Add(-time.Minute))
	return &api.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "execution", Namespace: "default", UID: "cluster-uid"},
		Spec: api.OpenBaoClusterSpec{Version: "2.6.2", Upgrade: &api.UpgradeConfig{
			Requests: &api.UpgradeRequestConfig{Retry: "retry-2"},
		}},
		Status: api.OpenBaoClusterStatus{Upgrade: &api.UpgradeProgress{
			ExecutionID: "attempt-1", FromVersion: "2.5.5", TargetVersion: "2.6.2", StartedAt: &started, CurrentPartition: 3,
			Failure: &api.ControllerErrorStatus{Reason: upgrade.ReasonPodNotReady},
		}},
	}
}

func TestRollingRetryIdentityIsAtomicAndIdempotent(t *testing.T) {
	for _, newerAttempt := range []bool{false, true} {
		t.Run(map[bool]string{false: "duplicate request", true: "changed attempt"}[newerAttempt], func(t *testing.T) {
			cluster := retryExecutionCluster()
			c := fake.NewClientBuilder().WithScheme(newRollingEventTestScheme(t)).
				WithObjects(withoutUpgradeStatus(cluster)).WithStatusSubresource(&api.OpenBaoCluster{}).Build()
			seedUpgradeStatus(t, c, cluster)
			stale := cluster.DeepCopy()
			m := &Manager{client: c, reader: c, adminOpsMutator: testAdminOpsMutator(c)}
			if newerAttempt {
				require.NoError(t, m.adminOpsMutator(t.Context(), cluster, func(obj *api.OpenBaoCluster) error {
					obj.Status.Upgrade.ExecutionID = "newer-attempt"
					return nil
				}, adminops.RespectOwnership))
				require.ErrorIs(t, m.patchRetryStatusSSA(t.Context(), stale, "retry-2"), operatorerrors.ErrTransientKubernetesAPI)
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
				require.Equal(t, "newer-attempt", cluster.Status.Upgrade.ExecutionID)
				require.True(t, upgrade.RetryRequestPending(cluster))
				return
			}
			require.NoError(t, m.patchRetryStatusSSA(t.Context(), cluster, "retry-2"))
			accepted := cluster.Status.Upgrade.ExecutionID
			require.NotEmpty(t, accepted)
			require.NotEqual(t, "attempt-1", accepted)
			require.Nil(t, cluster.Status.Upgrade.Failure)
			require.NoError(t, m.patchRetryStatusSSA(t.Context(), stale, "retry-2"))
			require.Equal(t, accepted, stale.Status.Upgrade.ExecutionID, "a repeated acknowledgement must not rotate identity")
		})
	}
}

func TestRollingRetryIdentityWriteFailures(t *testing.T) {
	for _, readBackFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "read-back"}[readBackFailure], func(t *testing.T) {
			cluster := retryExecutionCluster()
			base := fake.NewClientBuilder().WithScheme(newRollingEventTestScheme(t)).
				WithObjects(withoutUpgradeStatus(cluster)).WithStatusSubresource(&api.OpenBaoCluster{}).Build()
			seedUpgradeStatus(t, base, cluster)
			writeErr := errors.New("injected retry checkpoint failure")
			applied := false
			c := interceptor.NewClient(base, interceptor.Funcs{
				SubResourceApply: func(ctx context.Context, c client.Client, subresource string, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
					if !readBackFailure {
						return writeErr
					}
					err := c.SubResource(subresource).Apply(ctx, obj, opts...)
					applied = err == nil
					return err
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if applied {
						return writeErr
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			m := &Manager{client: c, reader: c, adminOpsMutator: testAdminOpsMutator(c)}
			require.ErrorIs(t, m.patchRetryStatusSSA(t.Context(), cluster, "retry-2"), writeErr)
			require.Equal(t, "attempt-1", cluster.Status.Upgrade.ExecutionID)
			require.NoError(t, base.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			if !readBackFailure {
				require.Equal(t, "attempt-1", cluster.Status.Upgrade.ExecutionID)
				require.True(t, upgrade.RetryRequestPending(cluster))
				require.NotNil(t, cluster.Status.Upgrade.Failure)
				return
			}
			accepted := cluster.Status.Upgrade.ExecutionID
			require.NotEqual(t, "attempt-1", accepted)
			require.False(t, upgrade.RetryRequestPending(cluster))
			m = &Manager{client: base, reader: base, adminOpsMutator: testAdminOpsMutator(base)}
			require.NoError(t, m.patchRetryStatusSSA(t.Context(), cluster, "retry-2"))
			require.Equal(t, accepted, cluster.Status.Upgrade.ExecutionID)
		})
	}
}

func TestRollingAttemptRetirementWaitsBeforeRetryAndRetarget(t *testing.T) {
	for _, action := range []upgradeAction{upgradeRetry, upgradeRetarget} {
		t.Run(string(action), func(t *testing.T) {
			cluster := retryExecutionCluster()
			if action == upgradeRetarget {
				cluster.Spec.Version = "2.7.1"
			}
			job := managedRollingJob(&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name: rollingStepDownJobName(cluster, "execution-2"), Namespace: cluster.Namespace,
				UID: "old-executor", Finalizers: []string{"test.openbao.org/hold"},
			}, Status: batchv1.JobStatus{Active: 1}}, cluster)
			base := fake.NewClientBuilder().WithScheme(newRollingEventTestScheme(t)).
				WithObjects(withoutUpgradeStatus(cluster), job).WithStatusSubresource(&api.OpenBaoCluster{}).Build()
			seedUpgradeStatus(t, base, cluster)
			deletes := 0
			c := interceptor.NewClient(base, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deletes++
					options := &client.DeleteOptions{}
					options.ApplyOptions(opts)
					require.Equal(t, metav1.DeletePropagationForeground, *options.PropagationPolicy)
					require.Equal(t, job.UID, *options.Preconditions.UID)
					return c.Delete(ctx, obj, opts...)
				},
			})
			m := &Manager{client: c, reader: c, adminOpsMutator: testAdminOpsMutator(c)}
			decision := upgradeDecision{action: action, retryRequest: "retry-2"}
			require.ErrorIs(t, m.applyUpgradeDecision(t.Context(), logr.Discard(), cluster, decision), operatorerrors.ErrTransientKubernetesAPI)
			require.Equal(t, "attempt-1", cluster.Status.Upgrade.ExecutionID)
			require.NotNil(t, cluster.Status.Upgrade.Failure)
			require.True(t, upgrade.RetryRequestPending(cluster))
			require.Equal(t, 1, deletes)
			require.NoError(t, base.Get(t.Context(), client.ObjectKeyFromObject(job), job))
			job.Finalizers = nil
			require.NoError(t, base.Update(t.Context(), job))
			require.NoError(t, m.applyUpgradeDecision(t.Context(), logr.Discard(), cluster, decision))
			if action == upgradeRetarget {
				require.Nil(t, cluster.Status.Upgrade)
			} else {
				require.NotEqual(t, "attempt-1", cluster.Status.Upgrade.ExecutionID)
				require.Nil(t, cluster.Status.Upgrade.Failure)
			}
			require.Equal(t, 1, deletes)
		})
	}
}

func TestRollingExecutionIdentityPersistsAcrossRestartAndChangesForNewUpgrade(t *testing.T) {
	cluster, objects := rollingLifecycleObjects(false)
	c := fake.NewClientBuilder().WithScheme(newRollingEventTestScheme(t)).
		WithObjects(append(objects, cluster)...).WithStatusSubresource(&api.OpenBaoCluster{}).Build()
	newManager := func() *Manager { return &Manager{client: c, reader: c, adminOpsMutator: testAdminOpsMutator(c)} }
	require.NoError(t, newManager().startUpgradeExecutionIfNeeded(t.Context(), logr.Discard(), cluster, nil, "rolling"))
	first := cluster.Status.Upgrade.ExecutionID
	require.NotEmpty(t, first)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.NoError(t, newManager().startUpgradeExecutionIfNeeded(t.Context(), logr.Discard(), cluster, nil, "rolling"))
	require.Equal(t, first, cluster.Status.Upgrade.ExecutionID)
	require.NoError(t, newManager().patchFinalizedUpgradeStatus(t.Context(), cluster))
	require.NoError(t, newManager().startUpgradeExecutionIfNeeded(t.Context(), logr.Discard(), cluster, nil, "rolling"))
	require.NotEqual(t, first, cluster.Status.Upgrade.ExecutionID)
}
