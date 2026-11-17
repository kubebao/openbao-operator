package rolling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/logging"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/core"
)

func (m *Manager) reconcileUpgradeExecution(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	metrics *upgrade.Metrics,
	strategy string,
) (recon.Result, error) {
	completed, err := m.performPodByPodUpgrade(ctx, logger, cluster, metrics)
	if err != nil {
		if errors.Is(err, errPartitionRetry) {
			logger.Info("Holding rolling upgrade until partition and Raft checks pass", "reason", err.Error(), "retryAfter", constants.RequeueShort)
			m.emitWarningEvent(cluster, upgrade.ReasonRollingPartitionRetry, "Holding rolling upgrade: %s", err.Error())
			return recon.Result{RequeueAfter: constants.RequeueShort}, nil
		}
		return m.handleUpgradeExecutionFailure(ctx, logger, cluster, metrics, strategy, err)
	}
	if !completed {
		return m.requeueAfterProgressPatch(ctx, cluster, "failed to update upgrade progress")
	}

	return m.finalizeConvergedUpgrade(ctx, logger, cluster, metrics, strategy)
}

func (m *Manager) finalizeConvergedUpgrade(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	metrics *upgrade.Metrics,
	strategy string,
) (recon.Result, error) {
	converged, err := m.waitForFinalizationConverged(ctx, logger, cluster)
	if err != nil {
		return recon.Result{}, err
	}
	if !converged {
		return m.requeueAfterProgressPatch(ctx, cluster, "failed to persist upgrade progress while waiting for convergence")
	}

	// Capture completion details before clearing progress or reading status back.
	fromVersion, toVersion := "", cluster.Spec.Version
	var duration time.Duration
	if cluster.Status.Upgrade != nil {
		fromVersion = cluster.Status.Upgrade.FromVersion
		if cluster.Status.Upgrade.StartedAt != nil {
			duration = time.Since(cluster.Status.Upgrade.StartedAt.Time)
		}
	}
	core.SetUpgradeComplete(&cluster.Status, toVersion)
	if err := m.patchFinalizedUpgradeStatus(ctx, cluster); err != nil {
		return recon.Result{}, fmt.Errorf("failed to update status after completing upgrade: %w", err)
	}

	if metrics != nil {
		if duration > 0 {
			metrics.RecordDuration(duration.Seconds(), fromVersion, toVersion)
		}
		upgrade.SetTerminalProgressMetrics(metrics, upgrade.UpgradeStatusSuccess)
		metrics.IncrementSuccess(strategy)
	}
	logging.LogAuditEvent(logger, logging.EventUpgradeCompleted, upgrade.UpgradeCompletedAuditFields(cluster, strategy, toVersion))
	m.emitNormalEvent(cluster, upgrade.ReasonUpgradeComplete, upgrade.MessageUpgradeComplete, fromVersion, toVersion)
	logger.Info("Upgrade completed successfully", "version", toVersion, "duration", duration.Seconds())
	return recon.Result{}, nil
}

func (m *Manager) handleUpgradeExecutionFailure(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	metrics *upgrade.Metrics,
	strategy string,
	cause error,
) (recon.Result, error) {
	m.recordUpgradeFailure(
		ctx,
		logger,
		cluster,
		metrics,
		strategy,
		upgrade.ReasonUpgradeFailed,
		cause.Error(),
		"Failed to update status after upgrade failure",
	)
	return recon.Result{}, cause
}

func (m *Manager) requeueAfterProgressPatch(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, errorMessage string) (recon.Result, error) {
	if err := m.patchUpgradeStatus(ctx, cluster); err != nil {
		return recon.Result{}, fmt.Errorf("%s: %w", errorMessage, err)
	}
	return recon.Result{RequeueAfter: constants.RequeueShort}, nil
}
