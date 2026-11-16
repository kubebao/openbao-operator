package rolling

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

func TestStartupBudgetExtendsRollingDeadline(t *testing.T) {
	configured := &api.OpenBaoCluster{Spec: api.OpenBaoClusterSpec{
		Probes: &api.ProbeConfig{Startup: &api.StartupProbeConfig{FailureThreshold: 180}},
	}}
	for _, stage := range []struct {
		name string
		spec upgradeTimeoutSpec
	}{
		{name: "revision", spec: podRevisionTimeout("bao-0", configured)},
		{name: "ready", spec: podReadyTimeout("bao-0", configured)},
		{name: "health", spec: podHealthTimeout("bao-0", configured)},
	} {
		for _, age := range []time.Duration{11 * time.Minute, 21 * time.Minute} {
			t.Run(stage.name+"/"+age.String(), func(t *testing.T) {
				started := metav1.NewTime(time.Now().Add(-age))
				cluster := &api.OpenBaoCluster{
					Spec: api.OpenBaoClusterSpec{Probes: &api.ProbeConfig{
						Startup: &api.StartupProbeConfig{FailureThreshold: 180},
					}},
					Status: api.OpenBaoClusterStatus{Upgrade: &api.UpgradeProgress{StartedAt: &started}},
				}
				err := failUpgradeIfStartedTimeout(cluster, stage.spec)
				if age < 20*time.Minute {
					require.NoError(t, err, "a permitted slow start must not hit the old ten-minute deadline")
					require.Nil(t, cluster.Status.Upgrade.Failure)
				} else {
					require.Error(t, err, "the extended startup budget must remain bounded")
					require.NotNil(t, cluster.Status.Upgrade.Failure)
					require.Contains(t, err.Error(), "20m", "the error must describe the effective deadline")
				}
			})
		}
	}
}

func TestDefaultRollingDeadlineUnchanged(t *testing.T) {
	started := metav1.NewTime(time.Now().Add(-upgrade.DefaultPodReadyTimeout - time.Minute))
	cluster := &api.OpenBaoCluster{Status: api.OpenBaoClusterStatus{Upgrade: &api.UpgradeProgress{StartedAt: &started}}}
	require.Error(t, failUpgradeIfStartedTimeout(cluster, podReadyTimeout("bao-0", nil)))
}

func TestSlowPodReadinessDoesNotAdvanceRollout(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bao-0", Namespace: "default"}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	manager := &Manager{client: kubeClient}
	started := metav1.NewTime(time.Now().Add(-11 * time.Minute))
	cluster := &api.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace},
		Spec: api.OpenBaoClusterSpec{Probes: &api.ProbeConfig{
			Startup: &api.StartupProbeConfig{FailureThreshold: 180},
		}},
		Status: api.OpenBaoClusterStatus{Upgrade: &api.UpgradeProgress{StartedAt: &started}},
	}
	ready, err := manager.waitForPodReady(context.Background(), testr.New(t), cluster, pod.Name)
	require.NoError(t, err, "the controller must keep waiting within the extended budget")
	require.False(t, ready, "a longer deadline must not treat an unready Pod as ready")
	require.Nil(t, cluster.Status.Upgrade.Failure)

	cluster.Spec.Probes = nil
	ready, err = manager.waitForPodReady(context.Background(), testr.New(t), cluster, pod.Name)
	require.Error(t, err, "the default deadline must still expire")
	require.False(t, ready)
}

func TestRollingStartupBudgetCoversSequentialVoters(t *testing.T) {
	for _, replicas := range []int32{1, 3, 5} {
		cluster := &api.OpenBaoCluster{Spec: api.OpenBaoClusterSpec{
			Replicas: replicas,
			Probes:   &api.ProbeConfig{Startup: &api.StartupProbeConfig{FailureThreshold: 180}},
		}}
		require.Equal(t, time.Duration(replicas)*20*time.Minute, rollingReadinessDeadline(cluster))
		cluster.Spec.Probes = nil
		require.Equal(t, upgrade.DefaultPodReadyTimeout, rollingReadinessDeadline(cluster))
	}
	cluster := &api.OpenBaoCluster{Spec: api.OpenBaoClusterSpec{
		Replicas: 3, ReadReplicas: &api.ReadReplicaConfig{Replicas: 2},
		Probes: &api.ProbeConfig{Startup: &api.StartupProbeConfig{FailureThreshold: 180}},
	}}
	require.Equal(t, 100*time.Minute, rollingReadinessDeadline(cluster), "read replicas also restart before voters")
	cluster = &api.OpenBaoCluster{Spec: api.OpenBaoClusterSpec{
		Replicas: math.MaxInt32,
		Probes:   &api.ProbeConfig{Startup: &api.StartupProbeConfig{FailureThreshold: 720}},
	}}
	require.Positive(t, podHealthTimeout("bao-0", cluster).Deadline, "oversized specs must not overflow the deadline")
}
