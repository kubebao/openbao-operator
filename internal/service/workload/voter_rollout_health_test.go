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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

type rolloutMembership struct {
	configuration *portopenbao.RaftConfigurationResponse
	state         *portopenbao.RaftAutopilotStateResponse
	err           error
	afterRead     func()
}

func (s *rolloutMembership) ReadRaftConfiguration(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (*portopenbao.RaftConfigurationResponse, error) {
	return s.configuration, s.err
}
func (s *rolloutMembership) ReadRaftAutopilotState(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (*portopenbao.RaftAutopilotStateResponse, error) {
	if s.afterRead != nil {
		s.afterRead()
	}
	return s.state, s.err
}

func rolloutGateFixture(t *testing.T) (client.Client, *openbaov1alpha1.OpenBaoCluster, *appsv1.StatefulSet, *rolloutMembership) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: "ns", UID: "cluster"}, Spec: openbaov1alpha1.OpenBaoClusterSpec{Replicas: 3}}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: "ns", UID: "set", Generation: 1,
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))}},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3))}, Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, UpdateRevision: rolloutNewRevision}}
	objects := []client.Object{cluster, sts}
	observer := &rolloutMembership{configuration: &portopenbao.RaftConfigurationResponse{Config: portopenbao.RaftConfiguration{Index: 7}},
		state: &portopenbao.RaftAutopilotStateResponse{Leader: "bao-0", Servers: map[string]portopenbao.RaftAutopilotServerState{}}}
	for ordinal := 0; ordinal < 3; ordinal++ {
		name := fmt.Sprintf("bao-%d", ordinal)
		objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(name),
			Labels: map[string]string{appsv1.StatefulSetRevisionLabel: rolloutNewRevision}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}}})
		observer.configuration.Config.Servers = append(observer.configuration.Config.Servers, portopenbao.RaftServer{NodeID: name, Voter: true, Leader: ordinal == 0})
		observer.state.Voters = append(observer.state.Voters, name)
		observer.state.Servers[name] = portopenbao.RaftAutopilotServerState{ID: name, Healthy: true, NodeStatus: "alive", LastIndex: 2000, LastTerm: 3, LastContact: "1ms"}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	return c, cluster, sts, observer
}

func TestVoterRolloutRequiresFreshProgressAfterReady(t *testing.T) {
	c, cluster, sts, observer := rolloutGateFixture(t)
	check := func() error {
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
		return checkVoterRollout(t.Context(), logr.Discard(), c, c, observer, cluster, sts, "bao-1", true)
	}
	peer := observer.state.Servers["bao-2"]
	peer.Healthy = false
	peer.LastIndex = 100
	observer.state.Servers["bao-2"] = peer
	require.ErrorContains(t, check(), "bao-2 to become healthy", "Kubernetes Ready must not release the next voter")
	peer.Healthy = true
	peer.LastIndex = 1990
	observer.state.Servers["bao-2"] = peer
	require.ErrorContains(t, check(), "awaiting a fresh Autopilot observation")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.NotEmpty(t, sts.Annotations[constants.AnnotationVoterRolloutObservation])
	require.ErrorContains(t, check(), "has not refreshed", "a cached healthy response is insufficient")
	leader := observer.state.Servers["bao-0"]
	leader.LastIndex++
	observer.state.Servers["bao-0"] = leader
	require.ErrorContains(t, check(), "has not refreshed voter bao-2", "another peer's changed sample cannot establish fresh contact with the returning voter")
	peer.LastContact = freshVoterContact
	observer.state.Servers["bao-2"] = peer
	require.ErrorContains(t, check(), "has not reached observed leader index 2000")
	peer.LastIndex = 2000
	observer.state.Servers["bao-2"] = peer
	require.NoError(t, check(), "the persisted observation remains usable after constructing another reconciler")
}

func TestVoterRolloutRejectsChangedPodIdentity(t *testing.T) {
	c, cluster, sts, observer := rolloutGateFixture(t)
	require.ErrorIs(t, checkVoterRollout(t.Context(), logr.Discard(), c, c, observer, cluster, sts, "bao-1", true), portworkload.ErrVoterRolloutWaiting)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	observer.afterRead = func() {
		pod := &corev1.Pod{}
		key := client.ObjectKey{Namespace: "ns", Name: "bao-2"}
		require.NoError(t, c.Get(t.Context(), key, pod))
		require.NoError(t, c.Delete(t.Context(), pod))
		pod.UID = "replacement"
		pod.ResourceVersion = ""
		require.NoError(t, c.Create(t.Context(), pod))
	}
	require.ErrorContains(t, checkVoterRollout(t.Context(), logr.Discard(), c, c, observer, cluster, sts, "bao-1", true), "identities or readiness changed")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.Empty(t, sts.Annotations[constants.AnnotationVoterRolloutObservation])
}

func TestVoterRolloutClearsObservationOnLostRaftAccess(t *testing.T) {
	c, cluster, sts, observer := rolloutGateFixture(t)
	require.ErrorIs(t, checkVoterRollout(t.Context(), logr.Discard(), c, c, observer, cluster, sts, "bao-1", true), portworkload.ErrVoterRolloutWaiting)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	observer.err = fmt.Errorf("permission denied")
	require.ErrorContains(t, checkVoterRollout(t.Context(), logr.Discard(), c, c, observer, cluster, sts, "bao-1", true), "permission denied")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.Empty(t, sts.Annotations[constants.AnnotationVoterRolloutObservation])
}

func TestVoterRolloutRejectsAgingContactWithoutNewHeartbeat(t *testing.T) {
	c, cluster, sts, observer := rolloutGateFixture(t)
	check := func() error {
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
		return checkVoterRollout(t.Context(), logr.Discard(), c, c, observer, cluster, sts, "bao-1", true)
	}
	require.ErrorIs(t, check(), portworkload.ErrVoterRolloutWaiting)
	peer := observer.state.Servers["bao-2"]
	peer.LastContact = "2s"
	observer.state.Servers["bao-2"] = peer
	require.ErrorIs(t, check(), portworkload.ErrVoterRolloutWaiting, "an older retained heartbeat is not fresh contact")
	peer.LastContact = "500us"
	observer.state.Servers["bao-2"] = peer
	require.NoError(t, check(), "a newer heartbeat permits an idle caught-up voter to proceed")
}

func TestVoterRolloutAcceptsAdvancedFollowerIndex(t *testing.T) {
	c, cluster, sts, observer := rolloutGateFixture(t)
	require.ErrorIs(t, checkVoterRollout(t.Context(), logr.Discard(), c, c, observer, cluster, sts, "bao-1", true), portworkload.ErrVoterRolloutWaiting)
	peer := observer.state.Servers["bao-2"]
	peer.LastIndex++
	observer.state.Servers["bao-2"] = peer
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.NoError(t, checkVoterRollout(t.Context(), logr.Discard(), c, c, observer, cluster, sts, "bao-1", true))
}
