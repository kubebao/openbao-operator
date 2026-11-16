package restore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestFreshTargetPreservesStartupBudget(t *testing.T) {
	_, request := freshTargetFixture(t)
	request.Spec.ClusterTemplate.Probes = &api.ProbeConfig{
		Startup: &api.StartupProbeConfig{FailureThreshold: 180},
	}
	cluster := projectRestoreTarget(request)
	require.Equal(t, request.Spec.ClusterTemplate.Probes, cluster.Spec.Probes)
	cluster.Spec.Probes.Startup.FailureThreshold = 240
	require.EqualValues(t, 180, request.Spec.ClusterTemplate.Probes.Startup.FailureThreshold,
		"projecting the target must not share mutable configuration with the restore request")
}

func TestFreshTargetStartupDeadlines(t *testing.T) {
	_, request := freshTargetFixture(t)
	require.Equal(t, 10*time.Minute, targetApplicationDeadline(request))
	require.Equal(t, 30*time.Minute, targetBootstrapDeadline(request))
	request.Spec.ClusterTemplate.Probes = &api.ProbeConfig{Startup: &api.StartupProbeConfig{FailureThreshold: 720}}
	require.Equal(t, 65*time.Minute, targetApplicationDeadline(request))
	require.Equal(t, 85*time.Minute, targetBootstrapDeadline(request))
}

func TestFreshTargetSlowStartupRecoveryBudget(t *testing.T) {
	for _, age := range []time.Duration{11 * time.Minute, 21 * time.Minute} {
		t.Run(age.String(), func(t *testing.T) {
			claimedAt := metav1.NewTime(time.Now().Add(-age))
			request := &api.OpenBaoRestore{
				Spec: api.OpenBaoRestoreSpec{TargetLifecycle: api.RestoreTargetLifecycleDisposable,
					ClusterTemplate: &api.RestoreClusterTemplate{Probes: &api.ProbeConfig{
						Startup: &api.StartupProbeConfig{FailureThreshold: 180},
					}},
				},
				Status: api.OpenBaoRestoreStatus{
					Target:          &api.RestoreTargetStatus{BootstrapClusterID: "bootstrap"},
					SubmissionClaim: &api.RestoreSubmissionClaim{ClaimedAt: claimedAt},
				},
			}
			want := targetRecoveryWait
			if age > 20*time.Minute {
				want = targetRecoveryCleanup
			}
			for range 3 {
				require.Equal(t, want, decideTargetRecovery(request, targetApplicationPending, time.Now()))
				require.Equal(t, claimedAt, request.Status.SubmissionClaim.ClaimedAt,
					"repeated observation must not renew the deadline")
			}
		})
	}
}
