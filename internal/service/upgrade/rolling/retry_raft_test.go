package rolling

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

func raftRetryFixture(t *testing.T) (*Manager, *openbaov1alpha1.OpenBaoCluster, *appsv1.StatefulSet, *rollingMembershipStub) {
	t.Helper()
	m, cluster, sts, observer := rollingRaftFixture(t, 2)
	cluster.Status.Upgrade.ExecutionID = "retry-attempt"
	cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{Requests: &openbaov1alpha1.UpgradeRequestConfig{Retry: "retry-request"}}
	sts.Annotations[constants.AnnotationResolvedMainImage] = cluster.Spec.Image
	require.NoError(t, m.client.Update(t.Context(), sts))
	pod := &corev1.Pod{}
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-1"}, pod))
	pod.Labels[appsv1.StatefulSetRevisionLabel] = oldRaftRevision
	pod.Spec.Containers[0].Image = "openbao/openbao:2.7.1"
	require.NoError(t, m.client.Update(t.Context(), pod))
	return m, cluster, sts, observer
}

func TestRaftRetryDoesNotDeleteAnUnreleasedVoterWithoutRaftGate(t *testing.T) {
	m, cluster, _, _ := raftRetryFixture(t)
	require.Error(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-1"}, &corev1.Pod{}), "retry preparation must gate this replacement")
}

func TestRaftRetryHoldsResetWhilePreviousVoterLags(t *testing.T) {
	m, cluster, sts, observer := raftRetryFixture(t)
	sts.Spec.UpdateStrategy.RollingUpdate.Partition = ptr.To(int32(1))
	require.NoError(t, m.client.Update(t.Context(), sts))
	peer := observer.state.Servers["bao-2"]
	peer.Healthy, peer.LastIndex = false, 10
	observer.state.Servers["bao-2"] = peer
	require.Error(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-1"}, &corev1.Pod{}))
}

func TestRaftRetryDoesNotResetReplacementAfterLostAcknowledgement(t *testing.T) {
	m, cluster, sts, observer := raftRetryFixture(t)
	sts.Spec.UpdateStrategy.RollingUpdate.Partition = ptr.To(int32(1))
	require.NoError(t, m.client.Update(t.Context(), sts))
	old := &corev1.Pod{}
	key := client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-1"}
	require.NoError(t, m.client.Get(t.Context(), key, old))
	require.Error(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	peer := observer.state.Servers["bao-2"]
	peer.LastContact = refreshedRaftContact
	observer.state.Servers["bao-2"] = peer
	require.Error(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster), "the replacement must return before retry preparation completes")
	replacement := old.DeepCopy()
	replacement.UID, replacement.ResourceVersion = "retry-replacement", ""
	replacement.Labels[appsv1.StatefulSetRevisionLabel] = updatedRaftRevision
	replacement.Spec.Containers[0].Image = cluster.Spec.Image
	replacement.Status.Conditions[0].Status = corev1.ConditionFalse
	require.NoError(t, m.client.Create(t.Context(), replacement))
	// Reconstruct while the replacement is still starting.
	restarted := &Manager{client: m.client, reader: m.client, voterRolloutGuard: workloadsvc.NewManager(m.client, m.scheme, "").WithRaftMembershipReader(observer)}
	require.ErrorContains(t, restarted.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster), "readiness")
	current := &corev1.Pod{}
	require.NoError(t, m.client.Get(t.Context(), key, current))
	require.Equal(t, replacement.UID, current.UID, "a long startup must not be restarted by replaying the retry preparation")

	current.Status.Conditions[0].Status = corev1.ConditionTrue
	require.NoError(t, m.client.Status().Update(t.Context(), current))
	require.Error(t, restarted.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	for _, name := range []string{"bao-1", "bao-2"} {
		peer := observer.state.Servers[name]
		peer.LastContact = "3ms"
		observer.state.Servers[name] = peer
	}
	require.NoError(t, restarted.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	// Simulate a lost status acknowledgement after the strategy and completion
	// receipt were persisted. A later probe failure must not replay the reset.
	require.NoError(t, m.client.Get(t.Context(), key, current))
	current.Status.Conditions[0].Status = corev1.ConditionFalse
	require.NoError(t, m.client.Status().Update(t.Context(), current))
	require.NoError(t, restarted.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	require.NoError(t, m.client.Get(t.Context(), key, current))
	require.Equal(t, replacement.UID, current.UID)
}

func TestRaftRetryRepairsFailedTemplateOneVoterAtATime(t *testing.T) {
	m, cluster, sts, observer := raftRetryFixture(t)
	sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	sts.Annotations[constants.AnnotationVoterRetryHold] = voterRetryHoldValue
	require.NoError(t, m.client.Update(t.Context(), sts))
	old := map[string]*corev1.Pod{}
	for _, name := range []string{"bao-0", "bao-1", "bao-2"} {
		pod := &corev1.Pod{}
		require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: name}, pod))
		pod.Labels[appsv1.StatefulSetRevisionLabel] = oldRaftRevision
		pod.Spec.Containers[0].Image = "openbao/openbao:2.7.1"
		require.NoError(t, m.client.Update(t.Context(), pod))
		old[name] = pod.DeepCopy()
	}
	check := func() error { return m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster) }
	require.Error(t, check(), "the first remaining-quorum observation must hold")
	peer := observer.state.Servers["bao-1"]
	peer.LastContact = refreshedRaftContact
	observer.state.Servers["bao-1"] = peer
	require.Error(t, check(), "deleting one voter must yield before the next reset")
	require.True(t, apierrors.IsNotFound(m.client.Get(t.Context(), client.ObjectKeyFromObject(old["bao-2"]), &corev1.Pod{})))
	replacement := old["bao-2"].DeepCopy()
	replacement.UID, replacement.ResourceVersion = "repaired-voter-2", ""
	replacement.Labels[appsv1.StatefulSetRevisionLabel] = updatedRaftRevision
	replacement.Spec.Containers[0].Image = cluster.Spec.Image
	require.NoError(t, m.client.Create(t.Context(), replacement))
	peer = observer.state.Servers["bao-2"]
	peer.Healthy, peer.LastIndex = false, 10
	observer.state.Servers["bao-2"] = peer
	require.ErrorContains(t, check(), "bao-2 to become healthy")
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(old["bao-1"]), &corev1.Pod{}), "Ready alone must not release the next repair")
	peer.Healthy, peer.LastIndex, peer.LastContact = true, 2000, "3ms"
	observer.state.Servers["bao-2"] = peer
	require.Error(t, check())
	peer.LastContact = "4ms"
	observer.state.Servers["bao-2"] = peer
	require.Error(t, check(), "the last remaining repair still needs another reconcile")
	require.True(t, apierrors.IsNotFound(m.client.Get(t.Context(), client.ObjectKeyFromObject(old["bao-1"]), &corev1.Pod{})))
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(old["bao-0"]), &corev1.Pod{}))
}

func TestRaftRetryReleasesOnDeleteHoldOnlyAfterAllVotersCatchUp(t *testing.T) {
	m, cluster, sts, observer := rollingRaftFixture(t, 0)
	sts.Annotations[constants.AnnotationResolvedMainImage] = cluster.Spec.Image
	sts.Annotations[constants.AnnotationVoterRetryHold] = voterRetryHoldValue
	sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	sts.Status.CurrentRevision = oldRaftRevision
	require.NoError(t, m.client.Update(t.Context(), sts))
	require.Error(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
	for _, name := range []string{"bao-1", "bao-2"} {
		peer := observer.state.Servers[name]
		peer.LastContact = refreshedRaftContact
		observer.state.Servers[name] = peer
	}
	require.NoError(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.Equal(t, appsv1.RollingUpdateStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
	require.EqualValues(t, 0, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	require.Empty(t, sts.Annotations[constants.AnnotationVoterRetryHold])
	for _, name := range []string{"bao-0", "bao-1", "bao-2"} {
		pod := &corev1.Pod{}
		require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: name}, pod))
		require.EqualValues(t, name, pod.UID, "restoring RollingUpdate must not replace any already-repaired Pod")
	}
}

func TestRaftRetryRejectsInvalidResetCheckpoints(t *testing.T) {
	for _, data := range []string{"not-json", `{"version":99}`} {
		t.Run(data, func(t *testing.T) {
			m, cluster, sts, _ := raftRetryFixture(t)
			sts.Annotations[constants.AnnotationVoterRetryReset] = data
			require.NoError(t, m.client.Update(t.Context(), sts))
			require.ErrorContains(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster), "invalid rolling retry reset checkpoint")
			require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-1"}, &corev1.Pod{}))
		})
	}
}

func TestRaftRetryWaitsForCorrectedTemplateEvenWhenLogicalPartitionIsZero(t *testing.T) {
	m, cluster, sts, _ := rollingRaftFixture(t, 0)
	sts.Annotations[constants.AnnotationResolvedMainImage] = cluster.Spec.Image
	require.NoError(t, m.client.Update(t.Context(), sts))
	cluster.Generation++
	require.Error(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
	require.NotEmpty(t, sts.Annotations[constants.AnnotationVoterRetryHold])
}

func TestRaftRetryKeepsPodThatRecoversBeforeReset(t *testing.T) {
	m, cluster, _, observer := raftRetryFixture(t)
	require.Error(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	key := client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-1"}
	pod := &corev1.Pod{}
	require.NoError(t, m.client.Get(t.Context(), key, pod))
	originalUID := pod.UID
	pod.Labels[appsv1.StatefulSetRevisionLabel] = updatedRaftRevision
	pod.Spec.Containers[0].Image = cluster.Spec.Image
	require.NoError(t, m.client.Update(t.Context(), pod))
	require.Error(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	for _, name := range []string{"bao-1", "bao-2"} {
		peer := observer.state.Servers[name]
		peer.LastContact = refreshedRaftContact
		observer.state.Servers[name] = peer
	}
	require.NoError(t, m.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	require.NoError(t, m.client.Get(t.Context(), key, pod))
	require.Equal(t, originalUID, pod.UID)
}
