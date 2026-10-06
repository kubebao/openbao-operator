package openbao

import (
	"context"
	"io"
)

// RestoreOptions controls snapshot restore behavior.
type RestoreOptions struct {
	// Force bypasses OpenBao's seal-consistency verification for the snapshot.
	Force bool
}

// ClusterActions defines the contract for performing cluster-level OpenBao operations.
type ClusterActions interface {
	IsSealed(ctx context.Context) (bool, error)
	IsHealthy(ctx context.Context) (bool, error)
	IsLeader(ctx context.Context) (bool, error)
	StepDownLeader(ctx context.Context) error
	Snapshot(ctx context.Context, writer io.Writer) error
	LoginJWT(ctx context.Context, role, jwtToken string) (string, int, error)
	// Restore submits once without following redirects. Success means request
	// acceptance; an error does not prove that the server did not apply the data.
	Restore(ctx context.Context, reader io.Reader, options RestoreOptions) error
}

// RaftActions extends ClusterActions with Raft-specific operations.
type RaftActions interface {
	ClusterActions
	JoinRaftCluster(ctx context.Context, leaderAPIAddr string, retry bool, nonVoter bool) error
	ReadRaftConfiguration(ctx context.Context) (*RaftConfigurationResponse, error)
	RemoveRaftPeer(ctx context.Context, serverID string) error
	UpdateRaftConfiguration(ctx context.Context, servers []RaftServer) error
}
