package openbao

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// RaftMembershipReader observes membership and replication through the controller's credentials.
type RaftMembershipReader interface {
	ReadRaftConfiguration(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (*RaftConfigurationResponse, error)
	ReadRaftAutopilotState(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (*RaftAutopilotStateResponse, error)
}

// VoterRolloutHealth records the replication position required before another disruption.
type VoterRolloutHealth struct {
	LeaderID    string
	LeaderTerm  uint64
	LeaderIndex uint64
	Indices     map[string]uint64
	Followers   map[string]RaftAutopilotServerState
}

// CheckVoterDisruption requires healthy remaining voters before a replacement.
// Three or more voters must retain a majority. Two-voter Development deployments
// retain their unavoidable write outage during replacement. An empty target checks
// every voter for completion. Read replicas do not count.
func CheckVoterDisruption(configuration *RaftConfigurationResponse, state *RaftAutopilotStateResponse, pods []string, target string, settings AutopilotConfig) (VoterRolloutHealth, error) {
	result := VoterRolloutHealth{Indices: make(map[string]uint64, len(pods)), Followers: make(map[string]RaftAutopilotServerState)}
	if configuration == nil || state == nil || len(pods) == 0 || state.Leader == "" {
		return result, fmt.Errorf("waiting for Raft membership and a known leader")
	}
	leader, found := state.Servers[state.Leader]
	if !found || leader.ID != state.Leader || !leader.Healthy || leader.NodeStatus != "alive" || leader.LastTerm == 0 || leader.LastIndex == 0 {
		return result, fmt.Errorf("waiting for Raft leader health and replication position")
	}
	result.LeaderID, result.LeaderTerm, result.LeaderIndex = state.Leader, leader.LastTerm, leader.LastIndex
	contactLimit, valid := parseAutopilotDuration(settings.LastContactThreshold)
	if !valid || contactLimit <= 0 || settings.MaxTrailingLogs < 0 {
		return result, fmt.Errorf("configured Raft health thresholds are invalid")
	}
	voters, err := checkRolloutMembership(configuration, state, len(pods))
	if err != nil {
		return result, err
	}
	healthyRemaining := 0
	seen := make(map[string]bool, len(voters))
	targetFound := target == ""
	for _, pod := range pods {
		server, found := findRaftServerForPod(configuration, pod)
		if !found || !voters[server.NodeID] || seen[server.NodeID] {
			return result, fmt.Errorf("voter Pod %s does not have unique voting membership", pod)
		}
		seen[server.NodeID] = true
		if pod == target {
			targetFound = true
			continue
		}
		peer, err := checkRolloutPeer(state, server.NodeID, pod, leader, contactLimit, settings.MaxTrailingLogs)
		if err != nil {
			return result, err
		}
		result.Indices[pod] = peer.LastIndex
		if server.NodeID != state.Leader {
			result.Followers[pod] = peer
		}
		healthyRemaining++
	}
	required := len(voters)/2 + 1
	if len(voters) == 2 && target != "" {
		required = 1
	}
	if !targetFound || healthyRemaining < required {
		return result, fmt.Errorf("replacing voter %s would leave insufficient healthy voters for quorum", target)
	}
	return result, nil
}

func checkRolloutMembership(configuration *RaftConfigurationResponse, state *RaftAutopilotStateResponse, expected int) (map[string]bool, error) {
	voters := make(map[string]bool)
	for _, server := range configuration.Config.Servers {
		if !server.Voter {
			continue
		}
		if server.NodeID == "" || voters[server.NodeID] {
			return nil, fmt.Errorf("waiting: Raft voter identities are missing or duplicated")
		}
		voters[server.NodeID] = true
		if server.Leader != (server.NodeID == state.Leader) {
			return nil, fmt.Errorf("waiting: Raft configuration and Autopilot disagree about the leader")
		}
	}
	if !voters[state.Leader] || len(voters) != expected || len(state.Voters) != len(voters) {
		return nil, fmt.Errorf("waiting: Raft voting membership has not converged to the workload")
	}
	seen := make(map[string]bool, len(voters))
	for _, id := range state.Voters {
		if !voters[id] || seen[id] {
			return nil, fmt.Errorf("waiting: Raft configuration and Autopilot disagree about voting membership")
		}
		seen[id] = true
	}
	return voters, nil
}
func checkRolloutPeer(state *RaftAutopilotStateResponse, id, pod string, leader RaftAutopilotServerState, contactLimit time.Duration, maxTrailingLogs int) (RaftAutopilotServerState, error) {
	peer, found := state.Servers[id]
	if !found || peer.ID != id || !peer.Healthy || peer.NodeStatus != "alive" {
		return RaftAutopilotServerState{}, fmt.Errorf("waiting for Raft voter %s to become healthy", pod)
	}
	contact, err := time.ParseDuration(peer.LastContact)
	if err != nil || contact < 0 || contact > contactLimit || peer.LastTerm != leader.LastTerm {
		return RaftAutopilotServerState{}, fmt.Errorf("waiting for current-term Raft contact from voter %s", pod)
	}
	// Autopilot samples peers separately. A follower can already be ahead of
	// the cached leader sample without indicating an inconsistent Raft term.
	if peer.LastIndex == 0 || (leader.LastIndex > peer.LastIndex && leader.LastIndex-peer.LastIndex > uint64(maxTrailingLogs)) {
		return RaftAutopilotServerState{}, fmt.Errorf("waiting for Raft voter %s to catch up", pod)
	}
	return peer, nil
}
