package openbao

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckVoterDisruption(t *testing.T) {
	const readReplicaName = "read-0"
	for _, tc := range []struct {
		name   string
		mutate func(*RaftConfigurationResponse, *RaftAutopilotStateResponse)
		want   string
	}{
		{name: "healthy remaining voters"},
		{name: "follower ahead of cached leader sample", mutate: func(_ *RaftConfigurationResponse, s *RaftAutopilotStateResponse) {
			p := s.Servers["bao-2"]
			p.LastIndex++
			s.Servers["bao-2"] = p
		}},
		{name: "returning voter unhealthy", mutate: func(_ *RaftConfigurationResponse, s *RaftAutopilotStateResponse) {
			p := s.Servers["bao-2"]
			p.Healthy = false
			s.Servers["bao-2"] = p
		}, want: "bao-2 to become healthy"},
		{name: "lag despite healthy flag", mutate: func(_ *RaftConfigurationResponse, s *RaftAutopilotStateResponse) {
			p := s.Servers["bao-2"]
			p.LastIndex = 10
			s.Servers["bao-2"] = p
		}, want: "bao-2 to catch up"},
		{name: "stale contact despite healthy flag", mutate: func(_ *RaftConfigurationResponse, s *RaftAutopilotStateResponse) {
			p := s.Servers["bao-2"]
			p.LastContact = "12s"
			s.Servers["bao-2"] = p
		}, want: "current-term Raft contact"},
		{name: "old term", mutate: func(_ *RaftConfigurationResponse, s *RaftAutopilotStateResponse) {
			p := s.Servers["bao-2"]
			p.LastTerm--
			s.Servers["bao-2"] = p
		}, want: "current-term Raft contact"},
		{name: "unknown leader", mutate: func(_ *RaftConfigurationResponse, s *RaftAutopilotStateResponse) { s.Leader = "" }, want: "known leader"},
		{name: "leader absent from voting membership", mutate: func(c *RaftConfigurationResponse, s *RaftAutopilotStateResponse) {
			leader := s.Servers[s.Leader]
			leader.ID = readReplicaName
			s.Leader = readReplicaName
			s.Servers[s.Leader] = leader
			c.Config.Servers[0].Leader = false
		}, want: "membership has not converged"},
		{name: "membership disagreement", mutate: func(_ *RaftConfigurationResponse, s *RaftAutopilotStateResponse) { s.Voters[2] = readReplicaName }, want: "disagree about voting membership"},
		{name: "read replica cannot replace voter", mutate: func(c *RaftConfigurationResponse, _ *RaftAutopilotStateResponse) { c.Config.Servers[2].Voter = false }, want: "membership has not converged"},
		{name: "target can be unhealthy", mutate: func(_ *RaftConfigurationResponse, s *RaftAutopilotStateResponse) {
			p := s.Servers["bao-1"]
			p.Healthy = false
			s.Servers["bao-1"] = p
		}},
		{name: "unhealthy read replica does not count as a voter", mutate: func(c *RaftConfigurationResponse, s *RaftAutopilotStateResponse) {
			c.Config.Servers = append(c.Config.Servers, RaftServer{NodeID: readReplicaName})
			s.NonVoters = []string{readReplicaName}
			s.Servers[readReplicaName] = RaftAutopilotServerState{ID: readReplicaName}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, s := rolloutHealthFixture()
			if tc.mutate != nil {
				tc.mutate(c, s)
			}
			health, err := CheckVoterDisruption(c, s, []string{"bao-0", "bao-1", "bao-2"}, "bao-1", AutopilotConfig{LastContactThreshold: "10s", MaxTrailingLogs: 1000})
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				return
			}
			require.NoError(t, err)
			require.EqualValues(t, 2000, health.LeaderIndex)
			require.Len(t, health.Indices, 2)
		})
	}
}

func TestCheckVoterDisruptionPreservesTwoVoterDevelopmentRollouts(t *testing.T) {
	c, s := rolloutHealthFixture()
	c.Config.Servers = c.Config.Servers[:2]
	s.Voters = s.Voters[:2]
	delete(s.Servers, "bao-2")
	_, err := CheckVoterDisruption(c, s, []string{"bao-0", "bao-1"}, "bao-1", AutopilotConfig{LastContactThreshold: "10s", MaxTrailingLogs: 1000})
	require.NoError(t, err, "two-voter Development deployments already require a write outage for replacement")
}

func rolloutHealthFixture() (*RaftConfigurationResponse, *RaftAutopilotStateResponse) {
	c := &RaftConfigurationResponse{}
	s := &RaftAutopilotStateResponse{Leader: "bao-0", Servers: map[string]RaftAutopilotServerState{}, Voters: []string{"bao-0", "bao-1", "bao-2"}}
	for _, name := range s.Voters {
		c.Config.Servers = append(c.Config.Servers, RaftServer{NodeID: name, Leader: name == s.Leader, Voter: true})
		s.Servers[name] = RaftAutopilotServerState{ID: name, Name: name, Healthy: true, NodeStatus: "alive", LastContact: "1ms", LastTerm: 3, LastIndex: 2000}
	}
	return c, s
}
