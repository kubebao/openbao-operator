//go:build integration

package rolling

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

func TestRetryTemplateRepairRestoresRollingStrategyThroughRealAPI(t *testing.T) {
	environment := &envtest.Environment{}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	fixture, cluster, sts, observer := rollingRaftFixture(t, 0)
	live, err := client.NewWithWatch(config, client.Options{Scheme: fixture.scheme})
	require.NoError(t, err)
	cluster.Namespace = "retry-template-real-api"
	require.NoError(t, live.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cluster.Namespace}}))
	require.NoError(t, live.Create(t.Context(), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: cluster.Namespace}}))
	statuses := sts.Status
	sts.Namespace, sts.UID, sts.ResourceVersion = cluster.Namespace, "", ""
	sts.Spec.ServiceName = sts.Name
	sts.Spec.Template.Labels = map[string]string{"app": sts.Name}
	sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: sts.Spec.Template.Labels}
	sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	sts.Annotations[constants.AnnotationVoterRetryHold] = voterRetryHoldValue
	sts.Annotations[constants.AnnotationResolvedMainImage] = cluster.Spec.Image
	sts.Status = appsv1.StatefulSetStatus{}
	require.NoError(t, live.Create(t.Context(), sts))
	statuses.CurrentRevision, statuses.ObservedGeneration, statuses.Replicas = oldRaftRevision, sts.Generation, 3
	sts.Status = statuses
	require.NoError(t, live.Status().Update(t.Context(), sts))
	identities := map[string]string{}
	for _, name := range []string{"bao-0", "bao-1", "bao-2"} {
		pod := &corev1.Pod{}
		require.NoError(t, fixture.client.Get(t.Context(), client.ObjectKey{Namespace: testNamespace, Name: name}, pod))
		status := pod.Status
		pod.Namespace, pod.UID, pod.ResourceVersion = cluster.Namespace, "", ""
		pod.Labels["app"] = sts.Name
		pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}
		pod.Status = corev1.PodStatus{}
		require.NoError(t, live.Create(t.Context(), pod))
		identities[name] = string(pod.UID)
		pod.Status = status
		require.NoError(t, live.Status().Update(t.Context(), pod))
	}
	manager := &Manager{client: live, reader: live, scheme: fixture.scheme,
		voterRolloutGuard: workloadsvc.NewManager(live, fixture.scheme, "").WithRaftMembershipReader(observer)}
	require.Error(t, manager.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
	for _, name := range []string{"bao-1", "bao-2"} {
		peer := observer.state.Servers[name]
		peer.LastContact = refreshedRaftContact
		observer.state.Servers[name] = peer
	}
	require.NoError(t, manager.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster))
	require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
	require.Equal(t, appsv1.RollingUpdateStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
	require.EqualValues(t, 0, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	require.Empty(t, sts.Annotations[constants.AnnotationVoterRetryHold])
	for name, uid := range identities {
		pod := &corev1.Pod{}
		require.NoError(t, live.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: name}, pod))
		require.Equal(t, uid, string(pod.UID))
	}
}
