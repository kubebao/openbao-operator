package workload

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

const (
	rolloutNewRevision = "new"
	freshVoterContact  = "2ms"
)

type voterMembershipStub struct {
	configuration *portopenbao.RaftConfigurationResponse
	state         *portopenbao.RaftAutopilotStateResponse
}

func (s *voterMembershipStub) ReadRaftConfiguration(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (*portopenbao.RaftConfigurationResponse, error) {
	return s.configuration, nil
}
func (s *voterMembershipStub) ReadRaftAutopilotState(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (*portopenbao.RaftAutopilotStateResponse, error) {
	return s.state, nil
}

func voterTemplateFixture(t *testing.T) (*Manager, *openbaov1alpha1.OpenBaoCluster, *appsv1.StatefulSet, *voterMembershipStub) {
	t.Helper()
	cluster := newMinimalCluster("bao", "ns")
	cluster.Status.Initialized = true
	cluster.Status.CurrentVersion = cluster.Spec.Version
	cluster.Generation = 1
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: "ns", UID: "set", Generation: 1,
		Annotations:     map[string]string{constants.AnnotationVoterRolloutTemplate: "template"},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))}},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3)), UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType, RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To(int32(2))}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: "old"}}}}},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, CurrentRevision: "old", UpdateRevision: rolloutNewRevision}}
	objects := []client.Object{cluster, sts}
	observer := &voterMembershipStub{configuration: &portopenbao.RaftConfigurationResponse{Config: portopenbao.RaftConfiguration{Index: 7}},
		state: &portopenbao.RaftAutopilotStateResponse{Leader: "bao-0", Servers: map[string]portopenbao.RaftAutopilotServerState{}}}
	for ordinal := 0; ordinal < 3; ordinal++ {
		name, revision := fmt.Sprintf("bao-%d", ordinal), "old"
		if ordinal == 2 {
			revision = rolloutNewRevision
		}
		objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(name),
			Labels:          map[string]string{appsv1.StatefulSetRevisionLabel: revision},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}}})
		observer.configuration.Config.Servers = append(observer.configuration.Config.Servers, portopenbao.RaftServer{NodeID: name, Voter: true, Leader: ordinal == 0})
		observer.state.Voters = append(observer.state.Voters, name)
		observer.state.Servers[name] = portopenbao.RaftAutopilotServerState{ID: name, NodeStatus: "alive", Healthy: true, LastContact: "1ms", LastIndex: 2000, LastTerm: 3}
	}
	c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objects...).Build()
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	return NewManager(c, testScheme, "").WithRaftMembershipReader(observer), cluster, sts, observer
}

func TestVoterTemplateRolloutHoldsLaggingReadyPodAcrossControllerRestart(t *testing.T) {
	m, cluster, sts, observer := voterTemplateFixture(t)
	peer := observer.state.Servers["bao-2"]
	peer.LastIndex = 100
	peer.Healthy = false
	observer.state.Servers["bao-2"] = peer
	check := func(manager *Manager) {
		result, err := manager.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
		require.NoError(t, err)
		require.Positive(t, result.RequeueAfter)
		require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	}
	check(m)
	require.EqualValues(t, 2, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	peer.LastIndex = 2000
	peer.Healthy = true
	observer.state.Servers["bao-2"] = peer
	check(m)
	require.NotEmpty(t, sts.Annotations[constants.AnnotationVoterRolloutObservation])
	restarted := NewManager(m.client, testScheme, "").WithRaftMembershipReader(observer)
	check(restarted)
	require.EqualValues(t, 2, *sts.Spec.UpdateStrategy.RollingUpdate.Partition, "cached pre-restart health must not release another voter")
	peer.LastContact = freshVoterContact
	observer.state.Servers["bao-2"] = peer
	check(restarted)
	require.EqualValues(t, 1, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
}

func TestVoterTemplateRolloutFinishesBeforePublishingRequestedVersion(t *testing.T) {
	m, cluster, sts, observer := voterTemplateFixture(t)
	cluster.Spec.Version = "2.7.2"
	cluster.Generation++
	_, err := m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	peer := observer.state.Servers["bao-2"]
	peer.LastContact = freshVoterContact
	observer.state.Servers["bao-2"] = peer
	_, err = m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.EqualValues(t, 1, *sts.Spec.UpdateStrategy.RollingUpdate.Partition, "a pending version request must let the previous template finish")
}

func TestPrepareVoterTemplateLocksBeforeChangingTemplate(t *testing.T) {
	m, cluster, sts, _ := voterTemplateFixture(t)
	sts.Status.CurrentRevision = sts.Status.UpdateRevision
	require.NoError(t, m.client.Status().Update(t.Context(), sts))
	desired := sts.DeepCopy()
	desired.Spec.Template.Spec.Containers[0].Image = rolloutNewRevision
	err := m.prepareVoterTemplateRollout(t.Context(), cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter}, sts, desired)
	require.NoError(t, err)
	stored := &appsv1.StatefulSet{}
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), stored))
	require.EqualValues(t, 3, *stored.Spec.UpdateStrategy.RollingUpdate.Partition)
	require.Equal(t, "old", stored.Spec.Template.Spec.Containers[0].Image, "the partition lock must precede the template write")
	require.NotEmpty(t, desired.Annotations[constants.AnnotationVoterRolloutTemplate])
}

func TestConcurrentVoterTemplateKeepsCurrentPartitionAndConfig(t *testing.T) {
	m, cluster, sts, _ := voterTemplateFixture(t)
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cluster.Name + "-config", Namespace: cluster.Namespace}, Data: map[string]string{"config.hcl": "current"}}
	require.NoError(t, m.client.Create(t.Context(), config))
	spec := StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter}
	err := m.EnsureStatefulSet(t.Context(), logr.Discard(), cluster, "next", spec)
	require.ErrorIs(t, err, portworkload.ErrVoterTemplateRolloutPending)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.EqualValues(t, 2, *sts.Spec.UpdateStrategy.RollingUpdate.Partition, "raising the partition could recreate an upgraded Pod from the previous revision")
	require.Equal(t, "old", sts.Spec.Template.Spec.Containers[0].Image)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(config), config))
	require.Equal(t, "current", config.Data["config.hcl"], "restarting Pods must retain the current rollout's config")
}

func TestPrepareVoterTemplateWaitsForControllerToObservePreviousTemplate(t *testing.T) {
	m, cluster, sts, _ := voterTemplateFixture(t)
	// Revision counters can still look converged before the StatefulSet
	// controller observes the template that was just published.
	sts.Status.CurrentRevision = sts.Status.UpdateRevision
	sts.Status.ObservedGeneration = 0
	require.NoError(t, m.client.Status().Update(t.Context(), sts))
	desired := sts.DeepCopy()
	desired.Spec.Template.Spec.Containers[0].Image = rolloutNewRevision
	err := m.prepareVoterTemplateRollout(t.Context(), cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter}, sts, desired)
	require.ErrorIs(t, err, portworkload.ErrVoterTemplateRolloutPending)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.EqualValues(t, 2, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
}

func TestPrepareVoterTemplateDetectsRemovedFields(t *testing.T) {
	m, cluster, sts, _ := voterTemplateFixture(t)
	sts.Status.CurrentRevision = sts.Status.UpdateRevision
	require.NoError(t, m.client.Status().Update(t.Context(), sts))
	sts.Spec.Template.Annotations = map[string]string{"example.com/retired": "value"}
	require.NoError(t, m.client.Update(t.Context(), sts))
	desired := sts.DeepCopy()
	spec := StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter}
	require.NoError(t, m.prepareVoterTemplateRollout(t.Context(), cluster, spec, sts, desired))
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	sts.Annotations[constants.AnnotationVoterRolloutTemplate] = desired.Annotations[constants.AnnotationVoterRolloutTemplate]
	sts.Spec.UpdateStrategy.RollingUpdate.Partition = ptr.To(int32(0))
	require.NoError(t, m.client.Update(t.Context(), sts))
	desired = sts.DeepCopy()
	desired.Spec.Template.Annotations = nil
	require.NoError(t, m.prepareVoterTemplateRollout(t.Context(), cluster, spec, sts, desired))
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.EqualValues(t, 3, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
}

func TestPrepareVoterTemplatePreservesIdleBlueGreenTransition(t *testing.T) {
	m, cluster, sts, _ := voterTemplateFixture(t)
	sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	require.NoError(t, m.client.Update(t.Context(), sts))
	desired := sts.DeepCopy()
	spec := StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter}
	require.NoError(t, m.reconcileStatefulSetUpdateStrategy(t.Context(), logr.Discard(), cluster, spec.Pool, sts))
	require.NoError(t, m.prepareVoterTemplateRollout(t.Context(), cluster, spec, sts, desired))
	require.Contains(t, desired.Annotations[constants.AnnotationVoterRolloutTemplate], deferredVoterTemplate)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	sts.Annotations[constants.AnnotationVoterRolloutTemplate] = desired.Annotations[constants.AnnotationVoterRolloutTemplate]
	require.NoError(t, m.client.Update(t.Context(), sts))
	result, err := m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.EqualValues(t, 3, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
}

func TestVoterTemplateRolloutHoldsWithoutAuthenticatedObserver(t *testing.T) {
	m, cluster, sts, _ := voterTemplateFixture(t)
	m.WithRaftMembershipReader(nil)
	recorder := events.NewFakeRecorder(1)
	m.WithRecorder(recorder)
	result, err := m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Positive(t, result.RequeueAfter)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.EqualValues(t, 2, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	select {
	case event := <-recorder.Events:
		require.Contains(t, event, constants.ReasonVoterRolloutWaiting)
		require.Contains(t, event, "authenticated Raft membership observer is unavailable")
	default:
		t.Fatal("missing actionable rollout hold event")
	}
}

func TestVoterTemplateRolloutDoesNotAdvanceWhilePausedOrInBreakGlass(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprintf("paused=%t", paused), func(t *testing.T) {
			m, cluster, sts, _ := voterTemplateFixture(t)
			cluster.Spec.Paused = paused
			if !paused {
				cluster.Status.BreakGlass = &openbaov1alpha1.BreakGlassStatus{Active: true, Nonce: "pending-ack"}
			}
			result, err := m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			require.Zero(t, result.RequeueAfter)
			require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
			require.EqualValues(t, 2, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
		})
	}
}
