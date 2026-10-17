package rolling

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/uuid"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/logging"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/core"
)

func (m *Manager) prepareUpgradeExecution(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	metrics *upgrade.Metrics,
	strategy string,
	decision upgradeDecision,
) (recon.Result, bool, error) {
	if err := m.applyUpgradeDecision(ctx, logger, cluster, decision); err != nil {
		return recon.Result{}, false, err
	}

	if err := m.ensureUpgradePrerequisites(ctx, logger, cluster); err != nil {
		return recon.Result{}, false, m.handlePreStartUpgradeFailure(ctx, logger, cluster, err)
	}

	if result, waiting, err := m.ensurePreUpgradeSnapshotComplete(ctx, logger, cluster); waiting || err != nil {
		return result, waiting, err
	}

	if err := m.startUpgradeExecutionIfNeeded(ctx, logger, cluster, metrics, strategy); err != nil {
		return recon.Result{}, false, err
	}

	if result, waiting, err := m.ensureReadReplicaPoolReadyForRollingUpgrade(ctx, logger, cluster); waiting || err != nil {
		if waiting && err == nil {
			m.recordInProgressMetrics(metrics, cluster)
		}
		return result, waiting, err
	}

	m.recordInProgressMetrics(metrics, cluster)
	return recon.Result{}, false, nil
}

func (m *Manager) ensureUpgradePrerequisites(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if err := upgrade.EnsureUpgradeServiceAccount(ctx, m.client, cluster, constants.FieldOwnerOpenBaoOperator); err != nil {
		return fmt.Errorf("failed to ensure upgrade ServiceAccount: %w", err)
	}
	if err := m.validateUpgrade(ctx, logger, cluster); err != nil {
		return err
	}
	return nil
}

func (m *Manager) handlePreStartUpgradeFailure(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	cause error,
) error {
	if persistErr := m.persistValidationFailure(ctx, cluster, cause); persistErr != nil {
		return persistErr
	}

	return core.ReleaseUpgradeLockOnErrorIfHeldWithReader(
		ctx,
		m.reader,
		m.client,
		logger,
		cluster,
		cluster.Status.Upgrade == nil,
		cause,
		"failed to release upgrade operation lock after pre-start failure",
	)
}

func (m *Manager) startUpgradeExecutionIfNeeded(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	metrics *upgrade.Metrics,
	strategy string,
) error {
	if cluster.Status.Upgrade != nil {
		return nil
	}
	fromVersion, toVersion, replicas := cluster.Status.CurrentVersion, cluster.Spec.Version, cluster.Spec.Replicas
	logger.Info("Initializing upgrade", "from", fromVersion, "to", toVersion, "replicas", replicas)
	core.SetUpgradeStarted(&cluster.Status, fromVersion, toVersion, replicas)
	executionID := string(uuid.NewUUID())
	cluster.Status.Upgrade.ExecutionID = executionID
	if err := m.setStatefulSetPartition(ctx, cluster, replicas); err != nil {
		return fmt.Errorf("failed to lock StatefulSet partition: %w", err)
	}
	if err := m.patchUpgradeStatus(ctx, cluster); err != nil {
		return fmt.Errorf("failed to update status after initializing upgrade: %w", err)
	}
	if cluster.Status.Upgrade == nil {
		return rollingAttemptChanged()
	}
	if cluster.Status.Upgrade.ExecutionID == "" {
		return m.failUnpersistedExecution(ctx, cluster)
	}
	if cluster.Status.Upgrade.ExecutionID != executionID {
		return rollingAttemptChanged()
	}

	if metrics != nil {
		metrics.IncrementTotal(strategy)
	}
	logging.LogAuditEvent(logger, logging.EventUpgradeStarted, upgrade.UpgradeStartedAuditFields(cluster, strategy, fromVersion, toVersion))
	m.emitNormalEvent(cluster, upgrade.ReasonUpgradeStarted, upgrade.MessageUpgradeStarted, fromVersion, toVersion)
	logger.Info("Upgrade initialized", "partition", replicas)
	return nil
}

func (m *Manager) applyUpgradeDecision(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, decision upgradeDecision) error {
	switch decision.action {
	case upgradeRetarget:
		logger.Info("Spec.Version changed during upgrade; clearing upgrade state and starting fresh",
			"previousTarget", cluster.Status.Upgrade.TargetVersion,
			"newTarget", cluster.Spec.Version)
		if err := m.retireStepDownJob(ctx, logger, cluster); err != nil {
			return err
		}
		return m.clearRetargetedUpgrade(ctx, cluster)
	case upgradeRetry:
		return m.prepareFailedUpgradeRetry(ctx, logger, cluster, decision.retryRequest)
	}
	return nil
}

func (m *Manager) persistValidationFailure(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, validationErr error) error {
	if cluster.Status.Upgrade == nil {
		return nil
	}
	if err := m.patchUpgradeStatus(ctx, cluster); err != nil {
		return fmt.Errorf("failed to persist rolling upgrade status after validation failure: %w (validation error: %w)", err, validationErr)
	}
	return nil
}

func (m *Manager) ensurePreUpgradeSnapshotComplete(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (recon.Result, bool, error) {
	if cluster.Spec.Upgrade == nil || !cluster.Spec.Upgrade.PreUpgradeSnapshot {
		return recon.Result{}, false, nil
	}

	snapshotComplete, err := m.handlePreUpgradeSnapshot(ctx, logger, cluster)
	if err != nil {
		return recon.Result{}, false, err
	}
	if snapshotComplete {
		return recon.Result{}, false, nil
	}

	logger.Info("Pre-upgrade snapshot in progress, waiting...")
	return recon.Result{RequeueAfter: constants.RequeueShort}, true, nil
}

func (m *Manager) recordInProgressMetrics(metrics *upgrade.Metrics, cluster *openbaov1alpha1.OpenBaoCluster) {
	if cluster.Status.Upgrade == nil {
		upgrade.SetRunningProgressMetrics(metrics, cluster.Spec.Replicas, 0, 0)
		return
	}

	upgrade.SetRunningProgressMetrics(
		metrics,
		cluster.Spec.Replicas,
		len(cluster.Status.Upgrade.CompletedPods),
		cluster.Status.Upgrade.CurrentPartition,
	)
}
