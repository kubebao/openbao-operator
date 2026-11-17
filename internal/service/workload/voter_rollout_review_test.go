package workload

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

const correctedVoterRevision = "corrected"

func TestVoterRolloutFinishesBeforeDeferredReplicaAndTemplateChange(t *testing.T) {
	m, cluster, sts, observer := voterTemplateFixture(t)
	cluster.Spec.Replicas = 5
	cluster.Generation++
	err := m.EnsureStatefulSet(t.Context(), logr.Discard(), cluster, "deferred config", StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter})
	require.ErrorIs(t, err, portworkload.ErrVoterTemplateRolloutPending)
	_, err = m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	peer := observer.state.Servers["bao-2"]
	peer.LastContact = "500us"
	observer.state.Servers["bao-2"] = peer
	_, err = m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.EqualValues(t, 3, *sts.Spec.Replicas, "the requested scale-up remains deferred")
	require.EqualValues(t, 1, *sts.Spec.UpdateStrategy.RollingUpdate.Partition, "the active three-voter rollout can still advance")
	require.EqualValues(t, 5, cluster.Spec.Replicas, "the published rollout view must not mutate the requested spec")
	for _, name := range []string{"bao-0", "bao-1"} {
		pod := &corev1.Pod{}
		require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: name}, pod))
		pod.Labels[appsv1.StatefulSetRevisionLabel] = sts.Status.UpdateRevision
		require.NoError(t, m.client.Update(t.Context(), pod))
	}
	_, err = m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	sts.Status.CurrentRevision = sts.Status.UpdateRevision
	require.NoError(t, m.client.Status().Update(t.Context(), sts))
	desired := sts.DeepCopy()
	desired.Spec.Replicas = ptr.To(int32(5))
	desired.Spec.Template.Annotations = map[string]string{"example.com/request": "deferred-B"}
	require.NoError(t, m.prepareVoterTemplateRollout(t.Context(), cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter}, sts, desired))
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.EqualValues(t, 5, *sts.Spec.UpdateStrategy.RollingUpdate.Partition, "template B is locked for the requested count after template A finishes")
}

func TestVoterTemplateCorrectionStagesWithoutRecreatingFromCurrentRevision(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(map[bool]string{false: "unready replacement", true: "deferred blue-green template"}[deferred], func(t *testing.T) {
			m, cluster, sts, _ := voterTemplateFixture(t)
			if deferred {
				sts.Annotations[constants.AnnotationVoterRolloutTemplate] = deferredVoterTemplate + "template"
				require.NoError(t, m.client.Update(t.Context(), sts))
			} else {
				pod := &corev1.Pod{}
				require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-2"}, pod))
				pod.Status.Conditions[0].Status = corev1.ConditionFalse
				require.NoError(t, m.client.Status().Update(t.Context(), pod))
			}
			desired := sts.DeepCopy()
			desired.Spec.Template.Annotations = map[string]string{"example.com/correction": "B"}
			err := m.prepareVoterTemplateRollout(t.Context(), cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter}, sts, desired)
			require.NoError(t, err, "a corrected template must be publishable")
			require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
			require.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type, "OnDelete protects existing updated Pods from currentRevision recreation")
			require.Nil(t, sts.Spec.UpdateStrategy.RollingUpdate)
			require.Nil(t, sts.Spec.Template.Annotations, "holding replacements precedes publishing the correction")
			require.Equal(t, desired.Annotations[constants.AnnotationVoterRolloutTemplate], sts.Annotations[constants.AnnotationVoterTemplateRepair])
			_, err = m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-1"}, &corev1.Pod{}), "a hold alone does not publish the repair template")
		})
	}
}

func TestVoterTemplateRepairPrioritizesUnavailablePodAndSurvivesRestart(t *testing.T) {
	m, cluster, sts, observer := voterTemplateFixture(t)
	sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	sts.Annotations[constants.AnnotationVoterTemplateRepair] = sts.Annotations[constants.AnnotationVoterRolloutTemplate]
	require.NoError(t, m.client.Update(t.Context(), sts))
	sts.Status.UpdateRevision = correctedVoterRevision
	require.NoError(t, m.client.Status().Update(t.Context(), sts))
	pods := map[string]*corev1.Pod{}
	for _, name := range []string{"bao-0", "bao-1", "bao-2"} {
		pod := &corev1.Pod{}
		require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: name}, pod))
		pods[name] = pod
	}
	pods["bao-1"].Status.Conditions[0].Status = corev1.ConditionFalse
	require.NoError(t, m.client.Status().Update(t.Context(), pods["bao-1"]))
	check := func() {
		_, err := m.ReconcileVoterRollout(t.Context(), testr.New(t), cluster)
		require.NoError(t, err)
	}
	refresh := func(contact string) {
		for _, name := range []string{"bao-1", "bao-2"} {
			peer := observer.state.Servers[name]
			peer.LastContact = contact
			observer.state.Servers[name] = peer
		}
	}
	replace := func(name string, ready bool) {
		pod := pods[name].DeepCopy()
		pod.ResourceVersion, pod.UID = "", types.UID(name+"-corrected")
		pod.Labels[appsv1.StatefulSetRevisionLabel] = sts.Status.UpdateRevision
		pod.Status.Conditions[0].Status = corev1.ConditionFalse
		if ready {
			pod.Status.Conditions[0].Status = corev1.ConditionTrue
		}
		require.NoError(t, m.client.Create(t.Context(), pod))
	}
	check()
	refresh("500us")
	check()
	require.True(t, apierrors.IsNotFound(m.client.Get(t.Context(), client.ObjectKeyFromObject(pods["bao-1"]), &corev1.Pod{})))
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(pods["bao-2"]), &corev1.Pod{}), "repair the unavailable voter before a healthy higher ordinal")
	replace("bao-1", false)
	m = NewManager(m.client, testScheme, "").WithRaftMembershipReader(observer)
	check()
	current := &corev1.Pod{}
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(pods["bao-1"]), current))
	require.Equal(t, types.UID("bao-1-corrected"), current.UID, "a slow desired-revision Pod is not reset after restart")
	current.Status.Conditions[0].Status = corev1.ConditionTrue
	require.NoError(t, m.client.Status().Update(t.Context(), current))
	peer := observer.state.Servers["bao-1"]
	peer.Healthy, peer.LastIndex = false, 10
	observer.state.Servers["bao-1"] = peer
	check()
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(pods["bao-2"]), &corev1.Pod{}), "Ready but lagging must hold the next repair")
	peer.Healthy, peer.LastIndex = true, 2000
	observer.state.Servers["bao-1"] = peer
	check()
	refresh("250us")
	check()
	require.True(t, apierrors.IsNotFound(m.client.Get(t.Context(), client.ObjectKeyFromObject(pods["bao-2"]), &corev1.Pod{})))
	replace("bao-2", true)
	check()
	refresh("125us")
	check()
	require.True(t, apierrors.IsNotFound(m.client.Get(t.Context(), client.ObjectKeyFromObject(pods["bao-0"]), &corev1.Pod{})))
	replace("bao-0", true)
	check()
	refresh("60us")
	check()
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.Equal(t, appsv1.RollingUpdateStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
	require.EqualValues(t, 0, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	require.Empty(t, sts.Annotations[constants.AnnotationVoterTemplateRepair])
	check()
	for name := range pods {
		require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: name}, current))
		require.Equal(t, types.UID(name+"-corrected"), current.UID)
	}
}

func TestDeferredTemplateCompletesBeforeRequestedVersionOrScale(t *testing.T) {
	for _, change := range []string{"version", "replicas"} {
		t.Run(change, func(t *testing.T) {
			m, cluster, sts, observer := voterTemplateFixture(t)
			sts.Annotations[constants.AnnotationVoterRolloutTemplate] = deferredVoterTemplate + "published-template"
			require.NoError(t, m.client.Update(t.Context(), sts))
			cluster.Generation++
			if change == "version" {
				cluster.Spec.Version = "2.7.2"
			} else {
				cluster.Spec.Replicas = 5
			}
			desired := sts.DeepCopy()
			desired.Spec.Template.Annotations = map[string]string{"example.com/request": "next"}
			require.ErrorIs(t, m.prepareVoterTemplateRollout(t.Context(), cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter}, sts, desired), portworkload.ErrVoterTemplateRolloutPending)
			require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
			require.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
			require.Equal(t, "published-template", sts.Annotations[constants.AnnotationVoterRolloutTemplate])
			require.Nil(t, sts.Spec.Template.Annotations, "the requested template remains deferred")
			_, err := m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			peer := observer.state.Servers["bao-2"]
			peer.LastContact = freshVoterContact
			observer.state.Servers["bao-2"] = peer
			_, err = m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			require.True(t, apierrors.IsNotFound(m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-1"}, &corev1.Pod{})))
			promoted := &corev1.Pod{}
			require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-2"}, promoted))
			require.Equal(t, types.UID("bao-2"), promoted.UID, "a promoted voter remains protected while the published template converges")
		})
	}
}
