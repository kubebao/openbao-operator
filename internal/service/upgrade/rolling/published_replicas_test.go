package rolling

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestRollingRaftGateUsesPublishedReplicasDuringScaleDown(t *testing.T) {
	m, cluster, sts, observer := rollingRaftFixture(t, 2)
	cluster.Spec.Replicas = 1
	peer := observer.state.Servers["bao-2"]
	peer.Healthy = false
	observer.state.Servers["bao-2"] = peer
	pod := &corev1.Pod{}
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-1"}, pod))
	pod.Labels[appsv1.StatefulSetRevisionLabel] = oldRaftRevision
	require.NoError(t, m.client.Update(t.Context(), pod))
	require.ErrorIs(t, m.checkRaftVoterRollout(t.Context(), logr.Discard(), cluster, pod.Name, true), errPartitionRetry)
	require.ErrorIs(t, m.setStatefulSetPartition(t.Context(), cluster, 1), errPartitionRetry)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.EqualValues(t, 2, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
}

func TestRollingStartLocksAndRecordsPublishedReplicas(t *testing.T) {
	cluster, objects := rollingLifecycleObjects(false)
	cluster.Spec.Replicas = 1
	for _, object := range objects {
		if sts, ok := object.(*appsv1.StatefulSet); ok {
			sts.Spec.Replicas = ptr.To(int32(3))
		}
	}
	c := fake.NewClientBuilder().WithScheme(newRollingEventTestScheme(t)).WithObjects(append(objects, cluster)...).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).Build()
	cached := interceptor.NewClient(c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		err := c.Get(ctx, key, obj, opts...)
		if sts, ok := obj.(*appsv1.StatefulSet); ok && err == nil {
			sts.Spec.Replicas = ptr.To(int32(1)) // The cache has not observed all voters.
		}
		return err
	}})
	m := &Manager{client: cached, reader: c, adminOpsMutator: testAdminOpsMutator(c)}
	require.NoError(t, m.startUpgradeExecutionIfNeeded(t.Context(), logr.Discard(), cluster, nil, "rolling"))
	require.EqualValues(t, 3, cluster.Status.Upgrade.CurrentPartition)
	sts := &appsv1.StatefulSet{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), sts))
	require.EqualValues(t, 3, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	require.EqualValues(t, 1, cluster.Spec.Replicas)
}

func TestRollingRaftGateSkipsOnlyPublishedSingleVoter(t *testing.T) {
	for _, actual := range []*int32{nil, ptr.To(int32(0)), ptr.To(int32(1))} {
		name := "unset"
		if actual != nil {
			name = fmt.Sprint(*actual)
		}
		t.Run(name, func(t *testing.T) {
			m, cluster, sts, _ := rollingRaftFixture(t, 2)
			m.voterRolloutGuard = nil
			sts.Spec.Replicas = actual
			require.NoError(t, m.client.Update(t.Context(), sts))
			err := m.checkRaftVoterRollout(t.Context(), logr.Discard(), cluster, "bao-0", true)
			if actual != nil && *actual == 1 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errPartitionRetry)
				err = m.setStatefulSetPartition(t.Context(), cluster, 0)
				require.ErrorIs(t, err, errPartitionRetry)
				require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
				require.EqualValues(t, 2, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
			}
		})
	}
}

func TestRollingRetryHoldsAllPublishedVotersDuringScaleDown(t *testing.T) {
	m, cluster, sts, _ := raftRetryFixture(t)
	cluster.Spec.Replicas = 1
	m.voterRolloutGuard = nil
	require.Error(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
	require.NotEmpty(t, sts.Annotations[constants.AnnotationVoterRetryHold])
}
