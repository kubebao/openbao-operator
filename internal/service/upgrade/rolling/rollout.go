package rolling

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/core"
)

// performPodByPodUpgrade executes the rolling update, one pod at a time.
// Returns true when all pods have been upgraded.
// Returns false with nil error when waiting for a condition (caller should requeue).
func (m *Manager) performPodByPodUpgrade(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, metrics *upgrade.Metrics) (bool, error) {
	target, completed, err := nextRolloutTargetPod(cluster)
	if err != nil {
		return false, err
	}
	if completed {
		logger.Info("All pods have been updated")
		return true, nil
	}

	logger.Info("Processing pod for upgrade",
		"pod", target.Name,
		"ordinal", target.Ordinal,
		"partition", target.CurrentPartition)

	podStartTime := time.Now()

	alreadyRolledOut, err := m.targetPodAlreadyRolledOut(ctx, logger, cluster, target)
	if err != nil {
		return false, err
	}
	if alreadyRolledOut {
		if err := m.setStatefulSetPartition(ctx, cluster, target.NextPartition); err != nil {
			return false, fmt.Errorf("failed to advance partition while resuming rolled-out target: %w", err)
		}
		recordCompletedTargetPodUpgrade(logger, cluster, metrics, target, podStartTime)
		return target.NextPartition == 0, nil
	}

	stepDownComplete, err := m.ensureTargetPodLeadershipTransferred(ctx, logger, cluster, target, metrics)
	if err != nil {
		return false, err
	}
	if !stepDownComplete {
		return false, nil
	}

	if err := m.setStatefulSetPartition(ctx, cluster, target.NextPartition); err != nil {
		return false, fmt.Errorf("failed to update partition: %w", err)
	}

	rolloutComplete, err := m.waitForTargetPodRollout(ctx, logger, cluster, target)
	if err != nil {
		return false, err
	}
	if !rolloutComplete {
		return false, nil
	}

	recordCompletedTargetPodUpgrade(logger, cluster, metrics, target, podStartTime)
	return target.NextPartition == 0, nil
}

func (m *Manager) targetPodAlreadyRolledOut(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	target rolloutTargetPod,
) (bool, error) {
	revisionUpdated, err := m.podRevisionAlreadyUpdated(ctx, logger, cluster, target.Name)
	if err != nil || !revisionUpdated {
		return false, err
	}

	podReady, err := m.waitForPodReady(ctx, logger, cluster, target.Name)
	if err != nil || !podReady {
		return false, err
	}

	if err := m.checkRaftVoterRollout(ctx, logger, cluster, target.Name, false); err != nil {
		return false, err
	}
	logger.Info("Target pod is already updated, ready, and caught up; resuming rolling-upgrade progress",
		"pod", target.Name,
		"partition", target.CurrentPartition,
		"nextPartition", target.NextPartition)
	return true, nil
}

type rolloutTargetPod struct {
	CurrentPartition int32
	NextPartition    int32
	Ordinal          int32
	Name             string
}

func nextRolloutTargetPod(cluster *openbaov1alpha1.OpenBaoCluster) (rolloutTargetPod, bool, error) {
	if cluster == nil || cluster.Status.Upgrade == nil {
		return rolloutTargetPod{}, false, fmt.Errorf("upgrade state is nil")
	}

	currentPartition := cluster.Status.Upgrade.CurrentPartition
	if currentPartition == 0 {
		return rolloutTargetPod{}, true, nil
	}

	targetOrdinal := currentPartition - 1
	return rolloutTargetPod{
		CurrentPartition: currentPartition,
		NextPartition:    currentPartition - 1,
		Ordinal:          targetOrdinal,
		Name:             upgrade.StableVoterPodName(cluster, targetOrdinal),
	}, false, nil
}

func (m *Manager) ensureTargetPodLeadershipTransferred(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	target rolloutTargetPod,
	metrics *upgrade.Metrics,
) (bool, error) {
	if cluster.Spec.Replicas <= 1 {
		logger.Info("Skipping leader step-down for single-replica rolling upgrade", "pod", target.Name)
		return true, nil
	}

	leaderPodName, err := m.currentLeaderPodByLabel(ctx, cluster)
	if err != nil {
		logger.Info("Unable to determine current leader from pod labels; attempting safe step-down", "error", err)
	}

	if leaderPodName != "" && leaderPodName != target.Name {
		return true, nil
	}

	logger.Info("Initiating leader step-down before updating pod", "pod", target.Name, "currentLeader", leaderPodName)
	return m.stepDownLeader(ctx, logger, cluster, target.Name, metrics)
}

func (m *Manager) waitForTargetPodRollout(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	target rolloutTargetPod,
) (bool, error) {
	revisionUpdated, err := m.waitForPodRevisionUpdated(ctx, logger, cluster, target.Name)
	if err != nil || !revisionUpdated {
		return revisionUpdated, err
	}

	podReady, err := m.waitForPodReady(ctx, logger, cluster, target.Name)
	if err != nil || !podReady {
		return podReady, err
	}

	podHealthy, err := m.waitForPodHealthy(ctx, logger, cluster, target.Name)
	if err != nil || !podHealthy {
		return podHealthy, err
	}
	if err := m.checkRaftVoterRollout(ctx, logger, cluster, target.Name, false); err != nil {
		return false, err
	}

	return true, nil
}

func recordCompletedTargetPodUpgrade(
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	metrics *upgrade.Metrics,
	target rolloutTargetPod,
	podStartTime time.Time,
) {
	core.SetUpgradeProgress(&cluster.Status, target.NextPartition, target.Ordinal)

	podDuration := time.Since(podStartTime).Seconds()
	metrics.RecordPodDuration(podDuration, target.Name)
	metrics.SetPodsCompleted(len(cluster.Status.Upgrade.CompletedPods))
	metrics.SetPartition(target.NextPartition)

	logger.Info("Pod upgrade completed",
		"pod", target.Name,
		"duration", podDuration,
		"remainingPartition", target.NextPartition)
}
