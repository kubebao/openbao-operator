//go:build integration

package integration

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestCRD_AutopilotConfiguration(t *testing.T) {
	namespace := newTestNamespace(t)
	for _, voters := range []struct {
		replicas      int32
		cleanupQuorum int
	}{
		{replicas: 1, cleanupQuorum: 3},
		{replicas: 2, cleanupQuorum: 3},
		{replicas: 3, cleanupQuorum: 3},
		{replicas: 5, cleanupQuorum: 5},
	} {
		for index, tt := range []struct {
			name      string
			autopilot *openbaov1alpha1.RaftAutopilotConfig
		}{
			{name: "omitted"},
			{name: "empty", autopilot: &openbaov1alpha1.RaftAutopilotConfig{}},
			{
				name:      "dead server threshold",
				autopilot: &openbaov1alpha1.RaftAutopilotConfig{DeadServerLastContactThreshold: "7m"},
			},
			{name: "last contact threshold", autopilot: &openbaov1alpha1.RaftAutopilotConfig{LastContactThreshold: "20s"}},
			{name: "trailing logs", autopilot: &openbaov1alpha1.RaftAutopilotConfig{MaxTrailingLogs: ptr.To(int32(2000))}},
			{name: "stabilization time", autopilot: &openbaov1alpha1.RaftAutopilotConfig{ServerStabilizationTime: "30s"}},
			{name: "cleanup enabled", autopilot: &openbaov1alpha1.RaftAutopilotConfig{CleanupDeadServers: ptr.To(true)}},
			{name: "cleanup disabled", autopilot: &openbaov1alpha1.RaftAutopilotConfig{CleanupDeadServers: ptr.To(false)}},
			{name: "quorum override", autopilot: &openbaov1alpha1.RaftAutopilotConfig{MinQuorum: ptr.To(int32(7))}},
		} {
			t.Run(fmt.Sprintf("%d voters/%s", voters.replicas, tt.name), func(t *testing.T) {
				cluster := newMinimalClusterObj(namespace, fmt.Sprintf("autopilot-%d-%d", voters.replicas, index))
				cluster.Spec.Replicas = voters.replicas
				require.NoError(t, k8sClient.Create(ctx, cluster))

				// Exercise adding Autopilot settings to an existing cluster, then
				// read the persisted defaults as the controller does.
				cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{
					Raft: &openbaov1alpha1.RaftConfig{Autopilot: tt.autopilot},
				}
				require.NoError(t, k8sClient.Update(ctx, cluster))
				stored := &openbaov1alpha1.OpenBaoCluster{}
				require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), stored))

				wantQuorum := voters.cleanupQuorum
				wantCleanup := true
				if tt.autopilot == nil {
					wantQuorum = int(voters.replicas)
					wantCleanup = voters.replicas >= 3
				} else {
					defaulted := stored.Spec.Configuration.Raft.Autopilot
					require.NotNil(t, defaulted.CleanupDeadServers)
					if tt.autopilot.CleanupDeadServers != nil && !*tt.autopilot.CleanupDeadServers {
						wantCleanup = false
						wantQuorum = int(voters.replicas)
					}
					require.Equal(t, wantCleanup, *defaulted.CleanupDeadServers)
					if tt.autopilot.MinQuorum != nil {
						wantQuorum = int(*tt.autopilot.MinQuorum)
					}
				}

				config := portopenbao.BuildAutopilotConfig(stored)
				require.Equal(t, wantCleanup, config.CleanupDeadServers)
				require.Equal(t, wantQuorum, config.MinQuorum)
				if config.CleanupDeadServers {
					require.GreaterOrEqual(t, config.MinQuorum, 3, "OpenBao rejects cleanup with a smaller quorum")
				}
				if tt.autopilot != nil {
					if tt.autopilot.LastContactThreshold != "" {
						require.Equal(t, tt.autopilot.LastContactThreshold, config.LastContactThreshold)
					}
					if tt.autopilot.ServerStabilizationTime != "" {
						require.Equal(t, tt.autopilot.ServerStabilizationTime, config.ServerStabilizationTime)
					}
					if tt.autopilot.DeadServerLastContactThreshold != "" {
						require.Equal(t, tt.autopilot.DeadServerLastContactThreshold, config.DeadServerLastContactThreshold)
					}
					if tt.autopilot.MaxTrailingLogs != nil {
						require.Equal(t, int(*tt.autopilot.MaxTrailingLogs), config.MaxTrailingLogs)
					}
				}
			})
		}
	}
}
