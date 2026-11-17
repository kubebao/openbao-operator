package rolling

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	openbaoapi "github.com/kubebao/openbao-operator/internal/platform/testutil/openbao"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

type rollingMembershipStub struct {
	configuration *portopenbao.RaftConfigurationResponse
	state         *portopenbao.RaftAutopilotStateResponse
}

func (s *rollingMembershipStub) ReadRaftConfiguration(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (*portopenbao.RaftConfigurationResponse, error) {
	return s.configuration, nil
}
func (s *rollingMembershipStub) ReadRaftAutopilotState(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (*portopenbao.RaftAutopilotStateResponse, error) {
	return s.state, nil
}

func rollingRaftFixture(t *testing.T, partition int32) (*Manager, *openbaov1alpha1.OpenBaoCluster, *appsv1.StatefulSet, *rollingMembershipStub) {
	t.Helper()
	scheme := newScheme()
	require.NoError(t, batchv1.AddToScheme(scheme))
	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: testNamespace, UID: "cluster", Generation: 1},
		Spec:   openbaov1alpha1.OpenBaoClusterSpec{Replicas: 3, Version: "2.7.2", Image: "openbao/openbao:2.7.2"},
		Status: openbaov1alpha1.OpenBaoClusterStatus{Initialized: true, CurrentVersion: "2.7.1", Upgrade: &openbaov1alpha1.UpgradeProgress{StartedAt: ptr.To(metav1.Now()), CurrentPartition: partition, TargetVersion: "2.7.2", FromVersion: "2.7.1"}}}
	for ordinal := int32(2); ordinal >= partition; ordinal-- {
		cluster.Status.Upgrade.CompletedPods = append(cluster.Status.Upgrade.CompletedPods, ordinal)
	}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: testNamespace, UID: "set", Generation: 1,
		Annotations:     map[string]string{constants.AnnotationClusterGeneration: "1", constants.AnnotationVoterRolloutTemplate: "template"},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))}},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3)), UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType, RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: cluster.Spec.Image}}}}},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, CurrentRevision: "new", UpdateRevision: "new", ReadyReplicas: 3, UpdatedReplicas: 3}}
	objects := []client.Object{cluster, sts, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: cluster.Name + constants.SuffixTLSCA, Namespace: testNamespace}, Data: map[string][]byte{"ca.crt": []byte("test-ca")}}}
	observer := &rollingMembershipStub{configuration: &portopenbao.RaftConfigurationResponse{Config: portopenbao.RaftConfiguration{Index: 7}}, state: &portopenbao.RaftAutopilotStateResponse{Leader: "bao-0", Servers: map[string]portopenbao.RaftAutopilotServerState{}}}
	for ordinal := 0; ordinal < 3; ordinal++ {
		name := fmt.Sprintf("bao-%d", ordinal)
		labels := resourceidentity.VoterPodSelectorLabels(cluster)
		labels[appsv1.StatefulSetRevisionLabel] = "new"
		objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, UID: types.UID(name), Labels: labels,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}},
			Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: cluster.Spec.Image}}},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}}})
		observer.configuration.Config.Servers = append(observer.configuration.Config.Servers, portopenbao.RaftServer{NodeID: name, Voter: true, Leader: ordinal == 0})
		observer.state.Voters = append(observer.state.Voters, name)
		observer.state.Servers[name] = portopenbao.RaftAutopilotServerState{ID: name, Healthy: true, NodeStatus: "alive", LastContact: "1ms", LastTerm: 3, LastIndex: 2000}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	m := newManagerWithClientFactory(c, scheme, nil, func(portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{IsHealthyFunc: func(context.Context) (bool, error) { return true, nil }}, nil
	}, nil).WithVoterRolloutGuard(workloadsvc.NewManager(c, scheme, "").WithRaftMembershipReader(observer))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	return m, cluster, sts, observer
}

func TestRollingPartitionDoesNotReleaseNextVoterWhilePreviousVoterLags(t *testing.T) {
	m, cluster, sts, observer := rollingRaftFixture(t, 2)
	// Retain converged aggregate status: fresh Pod observations must still hold
	// the partition when a target is old and a returning peer is behind.
	for _, name := range []string{"bao-0", "bao-1"} {
		pod := &corev1.Pod{}
		require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: testNamespace, Name: name}, pod))
		pod.Labels[appsv1.StatefulSetRevisionLabel] = "old"
		require.NoError(t, m.client.Update(t.Context(), pod))
	}
	peer := observer.state.Servers["bao-2"]
	peer.Healthy = false
	peer.LastIndex = 10
	observer.state.Servers["bao-2"] = peer
	require.ErrorIs(t, m.setStatefulSetPartition(t.Context(), cluster, 1), errPartitionRetry)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.EqualValues(t, 2, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	require.Empty(t, cluster.Status.Upgrade.CompletedPods[1:])
}

func TestRollingResumeRequiresRaftCatchupEvenWhenTargetIsAlreadyReady(t *testing.T) {
	m, cluster, sts, observer := rollingRaftFixture(t, 1)
	peer := observer.state.Servers["bao-2"]
	peer.Healthy = false
	peer.LastIndex = 10
	observer.state.Servers["bao-2"] = peer
	completed, err := m.performPodByPodUpgrade(t.Context(), logr.Discard(), cluster, upgrade.NewMetrics(cluster.Namespace, cluster.Name))
	require.False(t, completed)
	require.ErrorIs(t, err, errPartitionRetry)
	require.EqualValues(t, 1, cluster.Status.Upgrade.CurrentPartition)
	require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.EqualValues(t, 1, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	jobs := &batchv1.JobList{}
	require.NoError(t, m.client.List(t.Context(), jobs))
	require.Empty(t, jobs.Items)
}
