package openbaocluster

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/go-logr/logr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func (r *infraReconciler) confirmScaleDownResumeMembership(
	ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster,
	victim string, retained map[string]bool,
) error {
	config, err := r.deps.ScaleDown.Membership.ReadRaftConfiguration(ctx, logger, cluster)
	if err != nil {
		return fmt.Errorf("observe membership before scale-down resume: %w", err)
	}
	// Freeze copied values before another port read can mutate its response.
	config = snapshotScaleDownResumeConfiguration(config)
	identity, err := scaleDownResumeMembershipIdentity(config)
	if err != nil {
		return err
	}
	autopilot, err := r.deps.ScaleDown.Membership.ReadRaftAutopilotState(ctx, logger, cluster)
	if err != nil {
		return fmt.Errorf("observe retained member health before scale-down resume: %w", err)
	}
	if err := validateScaleDownResumeMembership(cluster, config, autopilot, victim, retained); err != nil {
		return err
	}
	confirmedHealth, err := r.deps.ScaleDown.Membership.ReadRaftAutopilotState(ctx, logger, cluster)
	if err != nil {
		return fmt.Errorf("confirm retained member health before scale-down resume: %w", err)
	}
	confirmedHealth = snapshotScaleDownResumeHealth(confirmedHealth)
	confirmed, err := r.deps.ScaleDown.Membership.ReadRaftConfiguration(ctx, logger, cluster)
	if err != nil {
		return fmt.Errorf("confirm membership before scale-down resume: %w", err)
	}
	confirmed = snapshotScaleDownResumeConfiguration(confirmed)
	confirmedIdentity, err := scaleDownResumeMembershipIdentity(confirmed)
	if err != nil {
		return err
	}
	if err := validateScaleDownResumeMembership(cluster, confirmed, confirmedHealth, victim, retained); err != nil {
		return err
	}
	// Zero means unavailable index metadata in OpenBao 2.6.4. Equal values
	// never establish a committed configuration epoch; compare the complete
	// observed identity and reject any change in supplied index metadata.
	if confirmed.Config.Index != config.Config.Index || !maps.Equal(identity, confirmedIdentity) {
		return fmt.Errorf("observed Raft membership changed during scale-down resume")
	}
	return nil
}

type scaleDownResumeMemberIdentity struct {
	host   string
	voter  bool
	leader bool
}

func snapshotScaleDownResumeHealth(health *portopenbao.RaftAutopilotStateResponse) *portopenbao.RaftAutopilotStateResponse {
	if health == nil {
		return nil
	}
	copied := *health
	copied.Servers = maps.Clone(health.Servers)
	return &copied
}

func snapshotScaleDownResumeConfiguration(config *portopenbao.RaftConfigurationResponse) *portopenbao.RaftConfigurationResponse {
	if config == nil {
		return nil
	}
	copied := *config
	copied.Config.Servers = slices.Clone(config.Config.Servers)
	return &copied
}

// Compare copied member values rather than order, response pointer identity, or
// a synthetic epoch. The caller separately validates each full health proof.
func scaleDownResumeMembershipIdentity(config *portopenbao.RaftConfigurationResponse) (map[string]scaleDownResumeMemberIdentity, error) {
	if config == nil || len(config.Config.Servers) == 0 {
		return nil, fmt.Errorf("authenticated Raft configuration is required for scale-down resume")
	}
	identity := make(map[string]scaleDownResumeMemberIdentity, len(config.Config.Servers))
	for _, server := range config.Config.Servers {
		host, err := scaleDownResumeRaftHost(server.Address)
		if _, duplicate := identity[server.NodeID]; err != nil || server.NodeID == "" || duplicate {
			return nil, fmt.Errorf("ambiguous Raft member identity prevents scale-down resume")
		}
		identity[server.NodeID] = scaleDownResumeMemberIdentity{host: host, voter: server.Voter, leader: server.Leader}
	}
	return identity, nil
}
