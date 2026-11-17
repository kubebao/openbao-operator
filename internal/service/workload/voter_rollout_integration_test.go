//go:build integration

package workload

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestVoterTemplateRolloutRealAPI(t *testing.T) {
	environment := &envtest.Environment{}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	live, err := client.NewWithWatch(config, client.Options{Scheme: testScheme})
	require.NoError(t, err)
	ctx, logger := t.Context(), logr.Discard()
	require.NoError(t, live.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "voter-rollout"}}))
	require.NoError(t, live.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "voter-rollout"}}))
	cluster := newMinimalCluster("bao", "voter-rollout")
	cluster.Spec.TLS.Enabled = false
	cluster.Generation = 1
	cluster.Status.Initialized = true
	cluster.Status.CurrentVersion = cluster.Spec.Version
	spec := StatefulSetSpec{Name: cluster.Name, Pool: constants.LabelValueOpenBaoWorkloadPoolVoter, Replicas: 3}
	m := NewManager(live, testScheme, "")
	require.NoError(t, m.EnsureStatefulSet(ctx, logger, cluster, "fixture", spec))
	sts := &appsv1.StatefulSet{}
	key := client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}
	require.NoError(t, live.Get(ctx, key, sts))
	sts.Status = appsv1.StatefulSetStatus{Replicas: 3, ObservedGeneration: sts.Generation, CurrentRevision: "old", UpdateRevision: "old", ReadyReplicas: 3, UpdatedReplicas: 3}
	require.NoError(t, live.Status().Update(ctx, sts))
	cluster.Spec.Probes = &openbaov1alpha1.ProbeConfig{Startup: &openbaov1alpha1.StartupProbeConfig{FailureThreshold: 61}}
	cluster.Generation++
	require.NoError(t, m.EnsureStatefulSet(ctx, logger, cluster, "fixture", spec))
	require.NoError(t, live.Get(ctx, key, sts))
	require.EqualValues(t, 3, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	require.EqualValues(t, 61, sts.Spec.Template.Spec.Containers[0].StartupProbe.FailureThreshold)
	fingerprint := sts.Annotations[constants.AnnotationVoterRolloutTemplate]
	require.NotEmpty(t, fingerprint)
	require.NoError(t, m.EnsureStatefulSet(ctx, logger, cluster, "fixture", spec))
	require.NoError(t, live.Get(ctx, key, sts))
	require.Equal(t, fingerprint, sts.Annotations[constants.AnnotationVoterRolloutTemplate], "API defaults must not create a new rollout on every reconcile")
	sts.Spec.UpdateStrategy.RollingUpdate.Partition = ptr.To(int32(2))
	require.NoError(t, live.Update(ctx, sts))
	sts.Status = appsv1.StatefulSetStatus{Replicas: 3, ObservedGeneration: sts.Generation, CurrentRevision: "old", UpdateRevision: rolloutNewRevision, ReadyReplicas: 3, UpdatedReplicas: 1}
	require.NoError(t, live.Status().Update(ctx, sts))
	observer := &voterMembershipStub{configuration: &portopenbao.RaftConfigurationResponse{Config: portopenbao.RaftConfiguration{Index: 7}}, state: &portopenbao.RaftAutopilotStateResponse{Leader: "bao-0", Servers: map[string]portopenbao.RaftAutopilotServerState{}}}
	m.WithRaftMembershipReader(observer)
	for ordinal := 0; ordinal < 3; ordinal++ {
		name, revision := fmt.Sprintf("bao-%d", ordinal), "old"
		if ordinal == 2 {
			revision = rolloutNewRevision
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cluster.Namespace, Labels: map[string]string{appsv1.StatefulSetRevisionLabel: revision},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: cluster.Spec.Image}}}}
		require.NoError(t, live.Create(ctx, pod))
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}
		require.NoError(t, live.Status().Update(ctx, pod))
		observer.configuration.Config.Servers = append(observer.configuration.Config.Servers, portopenbao.RaftServer{NodeID: name, Voter: true, Leader: ordinal == 0})
		observer.state.Voters = append(observer.state.Voters, name)
		observer.state.Servers[name] = portopenbao.RaftAutopilotServerState{ID: name, Healthy: true, NodeStatus: "alive", LastContact: "1ms", LastIndex: 2000, LastTerm: 3}
	}
	peer := observer.state.Servers["bao-2"]
	peer.LastIndex = 10
	peer.Healthy = false
	observer.state.Servers["bao-2"] = peer
	_, err = m.ReconcileVoterRollout(ctx, logger, cluster)
	require.NoError(t, err)
	require.NoError(t, live.Get(ctx, key, sts))
	require.EqualValues(t, 2, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	peer.LastIndex = 2000
	peer.Healthy = true
	observer.state.Servers["bao-2"] = peer
	_, err = m.ReconcileVoterRollout(ctx, logger, cluster)
	require.NoError(t, err)
	require.NoError(t, live.Get(ctx, key, sts))
	observation := sts.Annotations[constants.AnnotationVoterRolloutObservation]
	require.NotEmpty(t, observation)
	require.NoError(t, m.EnsureStatefulSet(ctx, logger, cluster, "fixture", spec))
	require.NoError(t, live.Get(ctx, key, sts))
	require.Equal(t, observation, sts.Annotations[constants.AnnotationVoterRolloutObservation], "SSA must retain the independent health observation")
	require.EqualValues(t, 2, *sts.Spec.UpdateStrategy.RollingUpdate.Partition, "SSA must retain an in-flight partition")
	peer.LastContact = freshVoterContact
	observer.state.Servers["bao-2"] = peer
	_, err = NewManager(live, testScheme, "").WithRaftMembershipReader(observer).ReconcileVoterRollout(ctx, logger, cluster)
	require.NoError(t, err)
	require.NoError(t, live.Get(ctx, key, sts))
	require.EqualValues(t, 1, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)

	// Finish the current rollout before testing another desired template.
	sts.Status.CurrentRevision = sts.Status.UpdateRevision
	require.NoError(t, live.Status().Update(ctx, sts))

	// A failed lock must prevent the new template from reaching the API server.
	blocked := interceptor.NewClient(live, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if _, ok := obj.(*appsv1.StatefulSet); ok {
			return apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, obj.GetName(), fmt.Errorf("concurrent partition write"))
		}
		return c.Patch(ctx, obj, patch, opts...)
	}})
	cluster.Spec.Probes.Startup.FailureThreshold = 62
	cluster.Generation++
	err = NewManager(blocked, testScheme, "").WithReader(live).EnsureStatefulSet(ctx, logger, cluster, "fixture", spec)
	require.True(t, apierrors.IsConflict(err), "lock conflicts must remain retryable: %v", err)
	require.NoError(t, live.Get(ctx, key, sts))
	require.EqualValues(t, 61, sts.Spec.Template.Spec.Containers[0].StartupProbe.FailureThreshold)
}
