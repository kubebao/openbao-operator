package openbao_test

import (
	"reflect"
	"slices"
	"testing"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func ptrToInt32(v int32) *int32 {
	return &v
}

func TestBuildAutopilotConfig_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		profile       openbaov1alpha1.Profile
		replicas      int32
		configuration *openbaov1alpha1.OpenBaoConfiguration
		wantQuorum    int
		wantCleanup   bool
		wantLogs      int
	}{
		{name: "empty configuration", replicas: 3, configuration: &openbaov1alpha1.OpenBaoConfiguration{}, wantQuorum: 3, wantCleanup: true, wantLogs: 1000},
		{name: "empty raft configuration", replicas: 3, configuration: &openbaov1alpha1.OpenBaoConfiguration{Raft: &openbaov1alpha1.RaftConfig{}}, wantQuorum: 3, wantCleanup: true, wantLogs: 1000},
		{name: "unknown profile uses replica count", profile: "unknown", replicas: 5, wantQuorum: 5, wantCleanup: true, wantLogs: 1000},
		{name: "negative replicas use minimum one", replicas: -1, wantQuorum: 1, wantLogs: 1000},
		{name: "hardened zero replicas use minimum three", profile: openbaov1alpha1.ProfileHardened, wantQuorum: 3, wantCleanup: true, wantLogs: 1000},
		{
			name: "zero quorum override uses replica count", replicas: 5, wantQuorum: 5, wantCleanup: true, wantLogs: 1000,
			configuration: &openbaov1alpha1.OpenBaoConfiguration{Raft: &openbaov1alpha1.RaftConfig{Autopilot: &openbaov1alpha1.RaftAutopilotConfig{MinQuorum: ptrToInt32(0)}}},
		},
		{
			name: "zero quorum override uses hardened minimum", profile: openbaov1alpha1.ProfileHardened, replicas: 1, wantQuorum: 3, wantCleanup: true, wantLogs: 1000,
			configuration: &openbaov1alpha1.OpenBaoConfiguration{Raft: &openbaov1alpha1.RaftConfig{Autopilot: &openbaov1alpha1.RaftAutopilotConfig{MinQuorum: ptrToInt32(0)}}},
		},
		{
			name: "nonzero quorum is not validated here", replicas: 3, wantQuorum: -1, wantLogs: 1000,
			configuration: &openbaov1alpha1.OpenBaoConfiguration{Raft: &openbaov1alpha1.RaftConfig{Autopilot: &openbaov1alpha1.RaftAutopilotConfig{MinQuorum: ptrToInt32(-1)}}},
		},
		{
			name: "explicit cleanup true derives minimum quorum for one voter", replicas: 1, wantQuorum: 3, wantCleanup: true, wantLogs: 1000,
			configuration: &openbaov1alpha1.OpenBaoConfiguration{Raft: &openbaov1alpha1.RaftConfig{Autopilot: &openbaov1alpha1.RaftAutopilotConfig{CleanupDeadServers: ptrTo(true)}}},
		},
		{
			name: "explicit cleanup true derives minimum quorum for two voters", replicas: 2, wantQuorum: 3, wantCleanup: true, wantLogs: 1000,
			configuration: &openbaov1alpha1.OpenBaoConfiguration{Raft: &openbaov1alpha1.RaftConfig{Autopilot: &openbaov1alpha1.RaftAutopilotConfig{CleanupDeadServers: ptrTo(true)}}},
		},
		{
			name: "explicit cleanup true retains larger quorum", replicas: 5, wantQuorum: 5, wantCleanup: true, wantLogs: 1000,
			configuration: &openbaov1alpha1.OpenBaoConfiguration{Raft: &openbaov1alpha1.RaftConfig{Autopilot: &openbaov1alpha1.RaftAutopilotConfig{CleanupDeadServers: ptrTo(true)}}},
		},
		{
			name: "explicit cleanup false survives large quorum", replicas: 5, wantQuorum: 5, wantLogs: 1000,
			configuration: &openbaov1alpha1.OpenBaoConfiguration{Raft: &openbaov1alpha1.RaftConfig{Autopilot: &openbaov1alpha1.RaftAutopilotConfig{CleanupDeadServers: ptrTo(false)}}},
		},
		{
			name: "zero trailing logs is an override and empty strings use defaults", replicas: 3, wantQuorum: 3, wantCleanup: true,
			configuration: &openbaov1alpha1.OpenBaoConfiguration{Raft: &openbaov1alpha1.RaftConfig{Autopilot: &openbaov1alpha1.RaftAutopilotConfig{MaxTrailingLogs: ptrToInt32(0)}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &openbaov1alpha1.OpenBaoCluster{Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Profile: tt.profile, Replicas: tt.replicas, Configuration: tt.configuration,
			}}
			before := cluster.DeepCopy()
			want := portopenbao.AutopilotConfig{
				CleanupDeadServers: tt.wantCleanup, MinQuorum: tt.wantQuorum, MaxTrailingLogs: tt.wantLogs,
				DeadServerLastContactThreshold: "5m", LastContactThreshold: "10s", ServerStabilizationTime: "10s",
			}
			if got := portopenbao.BuildAutopilotConfig(cluster); got != want {
				t.Errorf("BuildAutopilotConfig() = %+v, want %+v", got, want)
			}
			if !reflect.DeepEqual(cluster, before) {
				t.Fatal("BuildAutopilotConfig() mutated the input cluster")
			}
		})
	}
}

func TestRaftPeerRemovalDecisions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		podName         string
		servers         []portopenbao.RaftServer
		nilConfig       bool
		wantServerID    string
		wantPeer        portopenbao.RaftPeerRemovalAction
		wantReadReplica portopenbao.RaftPeerRemovalAction
	}{
		{name: "nil observation", podName: "cluster-2", nilConfig: true},
		{name: "empty membership", podName: "cluster-2"},
		{name: "blank pod name", podName: " \t", servers: []portopenbao.RaftServer{{NodeID: " \t"}}},
		{name: "empty pod name", servers: []portopenbao.RaftServer{{NodeID: ""}}},
		{name: "unmatched pod", podName: "cluster-2", servers: []portopenbao.RaftServer{{NodeID: "cluster-0"}}},
		{
			name: "exact node id", podName: "cluster-2", servers: []portopenbao.RaftServer{{NodeID: "cluster-2", Address: "unrelated", Voter: true}},
			wantServerID: "cluster-2", wantPeer: portopenbao.RaftPeerRemove, wantReadReplica: portopenbao.RaftPeerRefuseVoter,
		},
		{
			name: "address fallback preserves server id", podName: "cluster-2", servers: []portopenbao.RaftServer{{NodeID: "raft-id", Address: "https://cluster-2.cluster.ns.svc:8200"}},
			wantServerID: "raft-id", wantPeer: portopenbao.RaftPeerRemove, wantReadReplica: portopenbao.RaftPeerRemove,
		},
		{
			name: "earlier address match precedes exact id", podName: "cluster-2", servers: []portopenbao.RaftServer{
				{NodeID: "first", Address: "cluster-2.cluster.ns.svc", Leader: true}, {NodeID: "cluster-2", Voter: true},
			},
			wantServerID: "first", wantPeer: portopenbao.RaftPeerStepDown, wantReadReplica: portopenbao.RaftPeerRemove,
		},
		{
			name: "first exact match wins", podName: "cluster-2", servers: []portopenbao.RaftServer{
				{NodeID: "cluster-2", Voter: true}, {NodeID: "cluster-2", Leader: true},
			},
			wantServerID: "cluster-2", wantPeer: portopenbao.RaftPeerRemove, wantReadReplica: portopenbao.RaftPeerRefuseVoter,
		},
		{
			name: "address substring matching is preserved", podName: "cluster-2", servers: []portopenbao.RaftServer{{NodeID: "prefixed", Address: "prefix-cluster-2.cluster.ns.svc"}},
			wantServerID: "prefixed", wantPeer: portopenbao.RaftPeerRemove, wantReadReplica: portopenbao.RaftPeerRemove,
		},
		{name: "ordinal prefix is not a match", podName: "cluster-2", servers: []portopenbao.RaftServer{{NodeID: "cluster-20", Address: "cluster-20.cluster.ns.svc"}}},
		{name: "nonblank pod name is not trimmed", podName: " cluster-2 ", servers: []portopenbao.RaftServer{{NodeID: "cluster-2", Address: "cluster-2.cluster.ns.svc"}}},
		{
			name: "voter leader is refused for read replica", podName: "cluster-2", servers: []portopenbao.RaftServer{{NodeID: "cluster-2", Leader: true, Voter: true}},
			wantServerID: "cluster-2", wantPeer: portopenbao.RaftPeerStepDown, wantReadReplica: portopenbao.RaftPeerRefuseVoter,
		},
		{
			name: "nonvoter leader is removed as read replica", podName: "cluster-2", servers: []portopenbao.RaftServer{{NodeID: "cluster-2", Leader: true}},
			wantServerID: "cluster-2", wantPeer: portopenbao.RaftPeerStepDown, wantReadReplica: portopenbao.RaftPeerRemove,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var config, before *portopenbao.RaftConfigurationResponse
			if !tt.nilConfig {
				config = &portopenbao.RaftConfigurationResponse{Config: portopenbao.RaftConfiguration{Servers: tt.servers, Index: 17}}
				before = &portopenbao.RaftConfigurationResponse{Config: portopenbao.RaftConfiguration{Servers: slices.Clone(tt.servers), Index: 17}}
			}
			want := portopenbao.RaftPeerRemovalDecision{Action: tt.wantPeer, ServerID: tt.wantServerID}
			if got := portopenbao.DecideRaftPeerRemoval(config, tt.podName); got != want {
				t.Errorf("DecideRaftPeerRemoval() = %+v, want %+v", got, want)
			}
			want.Action = tt.wantReadReplica
			if got := portopenbao.DecideReadReplicaRemoval(config, tt.podName); got != want {
				t.Errorf("DecideReadReplicaRemoval() = %+v, want %+v", got, want)
			}
			if !reflect.DeepEqual(config, before) {
				t.Fatal("peer removal decision mutated the input configuration")
			}
		})
	}
}

func ptrTo(v bool) *bool {
	return &v
}

func TestBuildAutopilotConfig(t *testing.T) {
	tests := []struct {
		name    string
		cluster *openbaov1alpha1.OpenBaoCluster
		want    portopenbao.AutopilotConfig
	}{
		{
			name: "Hardened profile with 3 replicas",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileHardened,
					Replicas: 3,
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             true,
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      3,
			},
		},
		{
			name: "Hardened profile with 5 replicas",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileHardened,
					Replicas: 5,
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             true,
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      5,
			},
		},
		{
			name: "Hardened profile with 1 replica (edge case, should still use 3)",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileHardened,
					Replicas: 1,
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             true,
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      3,
			},
		},
		{
			name: "Development profile with 1 replica",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Replicas: 1,
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             false, // Auto-disabled because MinQuorum < 3
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      1,
			},
		},
		{
			name: "Development profile with 2 replicas",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Replicas: 2,
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             false, // Auto-disabled because MinQuorum < 3
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      2,
			},
		},
		{
			name: "Development profile with 3 replicas",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Replicas: 3,
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             true,
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      3,
			},
		},
		{
			name: "User-provided MinQuorum override (Hardened)",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileHardened,
					Replicas: 3,
					Configuration: &openbaov1alpha1.OpenBaoConfiguration{
						Raft: &openbaov1alpha1.RaftConfig{
							Autopilot: &openbaov1alpha1.RaftAutopilotConfig{
								MinQuorum: ptrToInt32(5),
							},
						},
					},
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             true,
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      5, // User override respected
			},
		},
		{
			name: "User-provided MinQuorum override (Development)",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Replicas: 1,
					Configuration: &openbaov1alpha1.OpenBaoConfiguration{
						Raft: &openbaov1alpha1.RaftConfig{
							Autopilot: &openbaov1alpha1.RaftAutopilotConfig{
								MinQuorum: ptrToInt32(2),
							},
						},
					},
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             false, // Auto-disabled because MinQuorum < 3
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      2, // User override respected
			},
		},
		{
			name: "User-provided ServerStabilizationTime override",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Replicas: 1,
					Configuration: &openbaov1alpha1.OpenBaoConfiguration{
						Raft: &openbaov1alpha1.RaftConfig{
							Autopilot: &openbaov1alpha1.RaftAutopilotConfig{
								ServerStabilizationTime: "30s",
							},
						},
					},
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             false, // Auto-disabled because MinQuorum < 3
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "30s", // User override respected
				MinQuorum:                      1,
			},
		},
		{
			name: "User-provided DeadServerLastContactThreshold override",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Replicas: 1,
					Configuration: &openbaov1alpha1.OpenBaoConfiguration{
						Raft: &openbaov1alpha1.RaftConfig{
							Autopilot: &openbaov1alpha1.RaftAutopilotConfig{
								DeadServerLastContactThreshold: "1m",
							},
						},
					},
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             false, // Auto-disabled because MinQuorum < 3
				DeadServerLastContactThreshold: "1m",  // User override respected
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      1,
			},
		},
		{
			name: "User-provided CleanupDeadServers override (disabled)",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Replicas: 1,
					Configuration: &openbaov1alpha1.OpenBaoConfiguration{
						Raft: &openbaov1alpha1.RaftConfig{
							Autopilot: &openbaov1alpha1.RaftAutopilotConfig{
								CleanupDeadServers: ptrTo(false),
							},
						},
					},
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             false, // User override respected
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      1,
			},
		},
		{
			name: "User-provided LastContactThreshold override",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Replicas: 1,
					Configuration: &openbaov1alpha1.OpenBaoConfiguration{
						Raft: &openbaov1alpha1.RaftConfig{
							Autopilot: &openbaov1alpha1.RaftAutopilotConfig{
								LastContactThreshold: "30s",
							},
						},
					},
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             false, // Auto-disabled because MinQuorum < 3
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "30s", // User override respected
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      1,
			},
		},
		{
			name: "User-provided MaxTrailingLogs override",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Replicas: 1,
					Configuration: &openbaov1alpha1.OpenBaoConfiguration{
						Raft: &openbaov1alpha1.RaftConfig{
							Autopilot: &openbaov1alpha1.RaftAutopilotConfig{
								MaxTrailingLogs: ptrToInt32(2000),
							},
						},
					},
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             false, // Auto-disabled because MinQuorum < 3
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                2000, // User override respected
				ServerStabilizationTime:        "10s",
				MinQuorum:                      1,
			},
		},
		{
			name: "All user overrides provided",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileHardened,
					Replicas: 3,
					Configuration: &openbaov1alpha1.OpenBaoConfiguration{
						Raft: &openbaov1alpha1.RaftConfig{
							Autopilot: &openbaov1alpha1.RaftAutopilotConfig{
								CleanupDeadServers:             ptrTo(false),
								DeadServerLastContactThreshold: "10m",
								LastContactThreshold:           "30s",
								MaxTrailingLogs:                ptrToInt32(2000),
								ServerStabilizationTime:        "20s",
								MinQuorum:                      ptrToInt32(7),
							},
						},
					},
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             false,
				DeadServerLastContactThreshold: "10m",
				LastContactThreshold:           "30s",
				MaxTrailingLogs:                2000,
				ServerStabilizationTime:        "20s",
				MinQuorum:                      7,
			},
		},
		{
			name: "Development profile with 0 replicas (edge case, should use 1)",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Replicas: 0,
				},
			},
			want: portopenbao.AutopilotConfig{
				CleanupDeadServers:             false, // Auto-disabled because MinQuorum < 3
				DeadServerLastContactThreshold: "5m",
				LastContactThreshold:           "10s",
				MaxTrailingLogs:                1000,
				ServerStabilizationTime:        "10s",
				MinQuorum:                      1, // Minimum enforced
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.cluster.DeepCopy()
			got := portopenbao.BuildAutopilotConfig(tt.cluster)
			if !reflect.DeepEqual(tt.cluster, before) {
				t.Fatal("BuildAutopilotConfig() mutated the input cluster")
			}

			if got.CleanupDeadServers != tt.want.CleanupDeadServers {
				t.Errorf("CleanupDeadServers = %v, want %v", got.CleanupDeadServers, tt.want.CleanupDeadServers)
			}
			if got.DeadServerLastContactThreshold != tt.want.DeadServerLastContactThreshold {
				t.Errorf("DeadServerLastContactThreshold = %q, want %q", got.DeadServerLastContactThreshold, tt.want.DeadServerLastContactThreshold)
			}
			if got.ServerStabilizationTime != tt.want.ServerStabilizationTime {
				t.Errorf("ServerStabilizationTime = %q, want %q", got.ServerStabilizationTime, tt.want.ServerStabilizationTime)
			}
			if got.MinQuorum != tt.want.MinQuorum {
				t.Errorf("MinQuorum = %d, want %d", got.MinQuorum, tt.want.MinQuorum)
			}
			if got.LastContactThreshold != tt.want.LastContactThreshold {
				t.Errorf("LastContactThreshold = %q, want %q", got.LastContactThreshold, tt.want.LastContactThreshold)
			}
			if got.MaxTrailingLogs != tt.want.MaxTrailingLogs {
				t.Errorf("MaxTrailingLogs = %d, want %d", got.MaxTrailingLogs, tt.want.MaxTrailingLogs)
			}
		})
	}
}
