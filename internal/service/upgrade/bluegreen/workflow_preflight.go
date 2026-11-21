package bluegreen

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/core"
)

func (m *Manager) shouldReconcileBlueGreen(logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) bool {
	if upgrade.EffectiveStrategy(cluster) != openbaov1alpha1.UpdateStrategyBlueGreen {
		logger.V(1).Info("UpdateStrategy is not BlueGreen; skipping blue/green upgrade reconciliation",
			"requestedStrategy", upgrade.DesiredStrategy(cluster),
			"acceptedStrategy", cluster.Status.AcceptedUpgradeStrategy)
		return false
	}
	return true
}

func (m *Manager) ensureBlueGreenStatus(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) {
	if m.workloadRuntime == nil {
		return
	}
	m.workloadRuntime.EnsureBlueGreenStatus(ctx, logger, cluster)
}

func (m *Manager) releaseUpgradeLockIfHeld(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	return core.ReleaseUpgradeLockIfHeldWithReader(ctx, m.reader, m.client, logger, cluster)
}

func (m *Manager) finalizeUpgradeTerminalState(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	promoteGreenToBlue bool,
) error {
	core.FinalizeBlueGreenTerminalState(cluster, promoteGreenToBlue)
	return m.releaseUpgradeLockIfHeld(ctx, logger, cluster)
}

func (m *Manager) prepareBlueGreenReconcile(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	acknowledgements *upgrade.RequestAcknowledgements,
) (recon.Result, bool, error) {
	if err := upgrade.EnsureUpgradeServiceAccount(ctx, m.client, cluster, constants.FieldOwnerOpenBaoOperator); err != nil {
		return recon.Result{}, false, fmt.Errorf("failed to ensure upgrade ServiceAccount: %w", err)
	}

	m.ensureBlueGreenStatus(ctx, logger, cluster)

	if handled, result, err := m.reconcileValidationHookOutsideSyncing(ctx, logger, cluster); handled || err != nil {
		return result, true, err
	}

	if m.shouldHaltForBreakGlass(logger, cluster) {
		return requeueStandard(), true, nil
	}

	m.handleUnexpectedPromoteRequest(logger, cluster, acknowledgements)

	upgradeActive, upgradeNeeded := core.BlueGreenUpgradeState(cluster)

	if handled, res, err := m.maybeAcquireUpgradeLock(ctx, logger, cluster, upgradeActive, upgradeNeeded); handled || err != nil {
		return res, true, err
	}

	if handled, res, err := m.handleNoUpgradeNeeded(ctx, logger, cluster); handled || err != nil {
		return res, true, err
	}

	if handled, res, err := m.maybeHandleTargetRevisionDrift(ctx, logger, cluster); handled || err != nil {
		return res, true, err
	}

	if err := m.validateIdleUpgradeInputs(ctx, logger, cluster); err != nil {
		return recon.Result{}, true, err
	}

	if handled, res, err := m.maybeAbortUpgrade(ctx, logger, cluster); handled || err != nil {
		return res, true, err
	}

	if shouldWaitForSteadyReadReplicaDrain(cluster) {
		if result, waiting, err := m.ensureSteadyReadReplicasScaledDown(ctx, logger, cluster); waiting || err != nil {
			return result, true, err
		}
	}

	return recon.Result{}, false, nil
}

func shouldWaitForSteadyReadReplicaDrain(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return core.CurrentBlueGreenPhase(cluster) != openbaov1alpha1.PhaseRestoringReadReplicas
}

func (m *Manager) handleUnexpectedPromoteRequest(logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, acknowledgements *upgrade.RequestAcknowledgements) {
	if !upgrade.PromoteRequestPending(cluster) {
		return
	}
	if core.CurrentBlueGreenPhase(cluster) == openbaov1alpha1.PhaseSyncing &&
		cluster.Status.BlueGreen != nil &&
		cluster.Status.BlueGreen.ManualPromotionRequired {
		return
	}

	promoteRequest := upgrade.PromoteRequestValue(cluster)
	acknowledgements.Promote = promoteRequest
	logger.Info("Ignoring promote request because no held blue/green upgrade is waiting for approval",
		"promoteRequest", promoteRequest,
		"promoteRequestField", upgrade.RequestPromoteFieldPath)
}

func (m *Manager) validateIdleUpgradeInputs(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if core.CurrentBlueGreenPhase(cluster) != openbaov1alpha1.PhaseIdle {
		return nil
	}

	if err := upgrade.ValidateUpgradeTargetVersion(logger, cluster.Status.CurrentVersion, cluster.Spec.Version); err != nil {
		return core.ReleaseUpgradeLockOnErrorIfHeldWithReader(ctx, m.reader, m.client, logger, cluster, true, err, "")
	}
	if err := upgrade.ValidateImageRefMatchesVersion(cluster.Spec.Version, cluster.Spec.Image); err != nil {
		return core.ReleaseUpgradeLockOnErrorIfHeldWithReader(ctx, m.reader, m.client, logger, cluster, true, err, "")
	}

	return nil
}

func (m *Manager) maybeAcquireUpgradeLock(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, upgradeActive, upgradeNeeded bool) (bool, recon.Result, error) {
	if !upgradeActive && !upgradeNeeded {
		return false, recon.Result{}, nil
	}
	lockResult, err := core.AcquireUpgradeLockWithReader(ctx, m.reader, m.client, logger, cluster, fmt.Sprintf("blue/green upgrade phase %s", core.CurrentBlueGreenPhase(cluster)))
	if err != nil {
		return true, recon.Result{}, fmt.Errorf("failed to acquire upgrade operation lock: %w", err)
	}
	if lockResult.Blocked {
		err := lockResult.LockErr
		m.emitWarningEvent(cluster, upgrade.ReasonOperationLockBlocked, "Blue/green upgrade blocked by operation lock: %v", err)
		if upgradeActive {
			return true, recon.Result{}, fmt.Errorf("blue/green upgrade in progress but operation lock is held by another operation: %w", err)
		}
		logger.Info("Blue/green upgrade blocked by operation lock", "error", err.Error())
		return true, recon.Result{RequeueAfter: opslifecycle.RequeueDelay(opslifecycle.RetryClassLockContention)}, nil
	}
	return false, recon.Result{}, nil
}

func (m *Manager) handleNoUpgradeNeeded(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (bool, recon.Result, error) {
	if core.CurrentBlueGreenPhase(cluster) != openbaov1alpha1.PhaseIdle {
		return false, recon.Result{}, nil
	}

	if cluster.Status.CurrentVersion == "" {
		logger.Info("CurrentVersion not yet set; waiting for initial version to be established")
		if err := m.ensureIdleAndCleanupGreen(ctx, logger, cluster); err != nil {
			return true, recon.Result{}, err
		}
		return true, requeueStandard(), nil
	}

	if cluster.Status.CurrentVersion == cluster.Spec.Version {
		logger.V(1).Info("No upgrade needed; CurrentVersion matches Spec.Version",
			"currentVersion", cluster.Status.CurrentVersion,
			"specVersion", cluster.Spec.Version)
		if err := m.ensureIdleAndCleanupGreen(ctx, logger, cluster); err != nil {
			return true, recon.Result{}, err
		}
		return true, recon.Result{}, nil
	}

	return false, recon.Result{}, nil
}

func (m *Manager) ensureIdleAndCleanupGreen(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if cluster.Status.BlueGreen == nil {
		return nil
	}

	if cluster.Status.BlueGreen.GreenRevision != "" {
		if err := m.cleanupGreenStatefulSet(ctx, logger, cluster); err != nil {
			return fmt.Errorf("failed to cleanup Green StatefulSet: %w", err)
		}
	}

	core.ResetBlueGreenTransientState(cluster.Status.BlueGreen)
	return m.releaseUpgradeLockIfHeld(ctx, logger, cluster)
}

// maybeHandleTargetRevisionDrift unwinds an in-flight blue/green upgrade when
// the desired Green revision changes mid-upgrade. This prevents the operator
// from silently continuing an outdated target after spec.version/image/replicas
// were changed by the user.
func (m *Manager) maybeHandleTargetRevisionDrift(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (bool, recon.Result, error) {
	if cluster.Status.BlueGreen == nil {
		return false, recon.Result{}, nil
	}

	switch cluster.Status.BlueGreen.Phase {
	case openbaov1alpha1.PhaseIdle, openbaov1alpha1.PhaseRollingBack, openbaov1alpha1.PhaseRollbackCleanup:
		return false, recon.Result{}, nil
	}

	if cluster.Status.BlueGreen.GreenRevision == "" {
		return false, recon.Result{}, nil
	}

	desiredGreenRevision := m.calculateRevision(cluster)
	if cluster.Status.BlueGreen.GreenRevision == desiredGreenRevision {
		return false, recon.Result{}, nil
	}

	if isPastPointOfNoReturn(cluster.Status.BlueGreen.Phase) {
		logger.Info("Spec drift detected after Blue peer removal started; completing current target before re-evaluating",
			"phase", cluster.Status.BlueGreen.Phase,
			"activeGreenRevision", cluster.Status.BlueGreen.GreenRevision,
			"desiredGreenRevision", desiredGreenRevision)
		return false, recon.Result{}, nil
	}

	logger.Info("Spec drift detected during blue/green upgrade; unwinding current target before re-evaluating",
		"phase", cluster.Status.BlueGreen.Phase,
		"activeGreenRevision", cluster.Status.BlueGreen.GreenRevision,
		"desiredGreenRevision", desiredGreenRevision,
		"currentVersion", cluster.Status.CurrentVersion,
		"targetVersion", cluster.Spec.Version)

	result, err := m.triggerRollbackOrAbort(ctx, logger, cluster, upgrade.ReasonVersionMismatch)
	if err != nil {
		return true, recon.Result{}, err
	}
	if result == (recon.Result{}) {
		return true, requeueShort(), nil
	}
	return true, result, nil
}

func (m *Manager) maybeAbortUpgrade(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (bool, recon.Result, error) {
	shouldAbort, err := m.checkAbortConditions(ctx, logger, cluster)
	if err != nil {
		return true, recon.Result{}, fmt.Errorf("failed to check abort conditions: %w", err)
	}
	if !shouldAbort {
		return false, recon.Result{}, nil
	}
	if err := m.abortUpgrade(ctx, logger, cluster); err != nil {
		return true, recon.Result{}, fmt.Errorf("failed to abort upgrade: %w", err)
	}
	return true, requeueShort(), nil
}
