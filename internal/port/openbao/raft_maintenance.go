package openbao

import (
	"strings"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// BuildAutopilotConfig constructs the Autopilot configuration from CRD settings or defaults.
// Hardened clusters default to a minimum quorum of at least three. Other profiles
// use the replica count, with a minimum of one. Enabling dead-server cleanup
// raises the derived minimum quorum to at least three.
func BuildAutopilotConfig(cluster *openbaov1alpha1.OpenBaoCluster) AutopilotConfig {
	config := AutopilotConfig{
		CleanupDeadServers:             true,
		DeadServerLastContactThreshold: "5m",
		LastContactThreshold:           "10s",
		MaxTrailingLogs:                1000,
		ServerStabilizationTime:        "10s",
	}

	cleanupDeadServersOverridden := false
	if cluster.Spec.Configuration != nil &&
		cluster.Spec.Configuration.Raft != nil &&
		cluster.Spec.Configuration.Raft.Autopilot != nil {
		userConfig := cluster.Spec.Configuration.Raft.Autopilot
		if userConfig.CleanupDeadServers != nil {
			config.CleanupDeadServers = *userConfig.CleanupDeadServers
			cleanupDeadServersOverridden = true
		}
		if userConfig.DeadServerLastContactThreshold != "" {
			config.DeadServerLastContactThreshold = userConfig.DeadServerLastContactThreshold
		}
		if userConfig.ServerStabilizationTime != "" {
			config.ServerStabilizationTime = userConfig.ServerStabilizationTime
		}
		if userConfig.LastContactThreshold != "" {
			config.LastContactThreshold = userConfig.LastContactThreshold
		}
		if userConfig.MaxTrailingLogs != nil {
			config.MaxTrailingLogs = int(*userConfig.MaxTrailingLogs)
		}
		if userConfig.MinQuorum != nil {
			config.MinQuorum = int(*userConfig.MinQuorum)
		}
	}

	// A zero override uses the profile default, as does an omitted override.
	if config.MinQuorum == 0 {
		if cluster.Spec.Profile == openbaov1alpha1.ProfileHardened ||
			(cleanupDeadServersOverridden && config.CleanupDeadServers) {
			config.MinQuorum = max(3, int(cluster.Spec.Replicas))
		} else {
			config.MinQuorum = max(1, int(cluster.Spec.Replicas))
		}
	}

	// OpenBao requires MinQuorum >= 3 for dead-server cleanup. Preserve an
	// explicit cleanup override; its derived quorum is at least three.
	if config.MinQuorum < 3 && !cleanupDeadServersOverridden {
		config.CleanupDeadServers = false
	}

	return config
}

// RaftPeerRemovalAction identifies the next action for a departing Raft peer.
type RaftPeerRemovalAction uint8

const (
	RaftPeerAbsent RaftPeerRemovalAction = iota
	RaftPeerRemove
	RaftPeerStepDown
	RaftPeerRefuseVoter
)

// RaftPeerRemovalDecision contains the membership decision and matched server ID.
type RaftPeerRemovalDecision struct {
	Action   RaftPeerRemovalAction
	ServerID string
}

// DecideRaftPeerRemoval requires leader step-down before removing a departing leader.
// An absent peer needs no further Raft operation.
func DecideRaftPeerRemoval(config *RaftConfigurationResponse, podName string) RaftPeerRemovalDecision {
	server, found := findRaftServerForPod(config, podName)
	if !found {
		return RaftPeerRemovalDecision{Action: RaftPeerAbsent}
	}
	if server.Leader {
		return RaftPeerRemovalDecision{Action: RaftPeerStepDown, ServerID: server.NodeID}
	}
	return RaftPeerRemovalDecision{Action: RaftPeerRemove, ServerID: server.NodeID}
}

// DecideReadReplicaRemoval refuses removal when the departing peer is a voter.
// Read-replica removal does not request leader step-down.
func DecideReadReplicaRemoval(config *RaftConfigurationResponse, podName string) RaftPeerRemovalDecision {
	server, found := findRaftServerForPod(config, podName)
	if !found {
		return RaftPeerRemovalDecision{Action: RaftPeerAbsent}
	}
	if server.Voter {
		return RaftPeerRemovalDecision{Action: RaftPeerRefuseVoter, ServerID: server.NodeID}
	}
	return RaftPeerRemovalDecision{Action: RaftPeerRemove, ServerID: server.NodeID}
}

func findRaftServerForPod(config *RaftConfigurationResponse, podName string) (RaftServer, bool) {
	if config == nil || strings.TrimSpace(podName) == "" {
		return RaftServer{}, false
	}

	for _, server := range config.Config.Servers {
		if server.NodeID == podName || strings.Contains(server.Address, podName+".") {
			return server, true
		}
	}

	return RaftServer{}, false
}
