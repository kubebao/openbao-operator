package restore

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/logging"
	observability "github.com/kubebao/openbao-operator/internal/platform/observability"
	portrestore "github.com/kubebao/openbao-operator/internal/port/restore"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

// Reconcile processes an OpenBaoRestore resource through its lifecycle.
// Returns (result, error) where result.RequeueAfter indicates if reconciliation should be rescheduled.
func (m *Manager) Reconcile(ctx context.Context, logger logr.Logger, restore *openbaov1alpha1.OpenBaoRestore) (ctrl.Result, error) {
	if restore.DeletionTimestamp != nil {
		return m.handleDeletion(ctx, logger, restore)
	}
	if err := m.ensureFinalizer(ctx, restore); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure restore finalizer: %w", err)
	}

	// Initialize status if not set
	if restore.Status.Phase == "" {
		restore.Status.Phase = openbaov1alpha1.RestorePhasePending
	}

	switch restore.Status.Phase {
	case openbaov1alpha1.RestorePhasePending:
		return m.handlePending(ctx, logger, restore)
	case openbaov1alpha1.RestorePhaseValidating:
		return m.handleValidating(ctx, logger, restore)
	case openbaov1alpha1.RestorePhaseRunning:
		return m.handleRunning(ctx, logger, restore)
	case openbaov1alpha1.RestorePhaseCompleted, openbaov1alpha1.RestorePhaseFailed:
		// Terminal states: remove the retained Job and ensure lock cleanup eventually succeeds.
		return m.ensureTerminalCleanup(ctx, logger, restore)
	case openbaov1alpha1.RestorePhaseUnknown:
		return m.reconcileTargetRecovery(ctx, logger, restore)
	default:
		logger.Info("Unknown restore phase", "phase", restore.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (m *Manager) ensureTerminalCleanup(ctx context.Context, logger logr.Logger, restore *openbaov1alpha1.OpenBaoRestore) (ctrl.Result, error) {
	if needsTargetCleanup(restore) {
		return m.cleanupTarget(ctx, logger, restore)
	}
	if err := m.releaseUnsubmittedTarget(ctx, restore); err != nil {
		return ctrl.Result{}, err
	}
	if restore.Status.Execution != nil && restore.Status.Execution.JobName != "" {
		jobDeleted, err := m.deleteRestoreJob(ctx, logger, restore)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove terminal restore Job: %w", err)
		}
		if !jobDeleted {
			return ctrl.Result{RequeueAfter: restoreRequeueImmediately}, nil
		}
	}
	if err := m.releaseClusterLock(ctx, logger, restore); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to release cluster operation lock for terminal restore %s/%s: %w", restore.Namespace, restore.Name, err)
	}
	return ctrl.Result{}, nil
}

// handlePending transitions from Pending to Validating phase.
func (m *Manager) handlePending(ctx context.Context, logger logr.Logger, restore *openbaov1alpha1.OpenBaoRestore) (ctrl.Result, error) {
	logger.Info("Starting restore validation", "cluster", restore.Spec.Cluster)

	// Record start time
	original := restore.DeepCopy()
	now := metav1.Now()
	restore.Status.StartTime = &now
	restore.Status.Phase = openbaov1alpha1.RestorePhaseValidating
	restore.Status.SnapshotKey = restore.Spec.Source.Key
	restore.Status.Message = "Validating restore preconditions"

	if err := m.patchStatus(ctx, restore, original); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch restore status: %w", err)
	}
	opslifecycle.LogPhaseTransition(logger, logging.EventRestorePhaseTransition, string(openbaov1alpha1.RestorePhasePending), string(openbaov1alpha1.RestorePhaseValidating), map[string]string{
		"cluster_namespace": restore.Namespace,
		"cluster_name":      restore.Spec.Cluster,
		"restore_name":      restore.Name,
	})
	m.emitNormalEvent(restore, ReasonRestoreValidationStarted, "Restore validation started for cluster %s", restore.Spec.Cluster)

	observability.NewRestoreMetrics(restore.Namespace, restore.Spec.Cluster).RecordStarted()

	return ctrl.Result{RequeueAfter: restoreRequeueImmediately}, nil
}

// handleValidating validates preconditions and transitions to Running.
func (m *Manager) handleValidating(ctx context.Context, logger logr.Logger, restore *openbaov1alpha1.OpenBaoRestore) (ctrl.Result, error) {
	budget, err := portrestore.ResolveSnapshotBudget(restore.Spec.Source.MaxSnapshotSizeBytes)
	if err != nil {
		return m.failRestore(ctx, logger, restore, fmt.Sprintf("Invalid restore snapshot budget: %v", err))
	}
	if restore.Spec.Source.ExpectedSize > budget.MaxBytes {
		return m.failRestore(ctx, logger, restore, "Expected snapshot size exceeds maxSnapshotSizeBytes")
	}
	if ready, result, err := m.prepareTarget(ctx, logger, restore); !ready || err != nil {
		return result, err
	}

	// Validate and get target cluster
	cluster, result, err := m.validateCluster(ctx, logger, restore)
	if result != nil || err != nil {
		if result != nil {
			return *result, err
		}
		return ctrl.Result{}, err
	}

	// Acquire operation lock
	lockBefore, forceAcquired, result, err := m.acquireOperationLock(ctx, logger, restore, cluster)
	if result != nil || err != nil {
		if result != nil {
			return *result, err
		}
		return ctrl.Result{}, err
	}

	// Handle lock override event if needed
	if forceAcquired && lockBefore != nil {
		original := restore.DeepCopy()
		m.handleLockOverride(restore, lockBefore)
		if err := m.patchStatus(ctx, restore, original); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch restore status after lock override: %w", err)
		}
	}

	// Validate cluster state
	if result, err := m.validateClusterState(ctx, logger, restore, cluster); result != nil || err != nil {
		if result != nil {
			return *result, err
		}
		return ctrl.Result{}, err
	}

	// Validate restore auth/storage/egress assumptions the operator can verify.
	if result, err := m.validateExecutionReadiness(ctx, logger, restore, cluster); result != nil || err != nil {
		if result != nil {
			return *result, err
		}
		return ctrl.Result{}, err
	}

	// Ensure restore resources (ServiceAccount and RBAC)
	if err := m.ensureRestoreResources(ctx, logger, restore, cluster); err != nil {
		return ctrl.Result{}, err
	}

	// Persist the execution identity before entering Running. The Prepared stage
	// remains cancelable and does not assert that Job creation was attempted.
	original := restore.DeepCopy()
	restore.Status.Phase = openbaov1alpha1.RestorePhaseRunning
	restore.Status.Execution = newRestoreExecutionStatus(restore)
	restore.Status.Execution.TargetUID = cluster.UID
	restore.Status.Message = fmt.Sprintf("Restore execution %s prepared; waiting to commit Job %s.", restore.Status.Execution.OperationID, restore.Status.Execution.JobName)

	if err := m.patchStatus(ctx, restore, original); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch restore status: %w", err)
	}
	opslifecycle.LogPhaseTransition(logger, logging.EventRestorePhaseTransition, string(openbaov1alpha1.RestorePhaseValidating), string(openbaov1alpha1.RestorePhaseRunning), map[string]string{
		"cluster_namespace": restore.Namespace,
		"cluster_name":      restore.Spec.Cluster,
		"restore_name":      restore.Name,
	})
	m.emitNormalEvent(restore, ReasonRestoreStarted, "Restore started for cluster %s", restore.Spec.Cluster)

	logger.Info("Restore validation passed, transitioning to Running phase")
	return ctrl.Result{RequeueAfter: restoreRequeueImmediately}, nil
}
