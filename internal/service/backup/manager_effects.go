package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/logging"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

func (m *Manager) applyBackupDecision(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	metrics *Metrics,
	decision backupDecision,
) (recon.Result, error) {
	observation := decision.observation
	if err := m.applyInitialBackupSchedule(ctx, cluster, observation); err != nil {
		return recon.Result{}, fmt.Errorf("failed to initialize backup schedule: %w", err)
	}

	switch decision.kind {
	case backupDecisionIdle:
		return applyIdleBackup(logger, observation)
	case backupDecisionBlocked:
		return m.applyBlockedBackup(logger, cluster, observation)
	case backupDecisionCreate:
		return m.applyCreateBackup(ctx, logger, cluster, metrics, observation)
	case backupDecisionObserve:
		return m.applyObserveBackup(ctx, logger, cluster, metrics, observation)
	case backupDecisionFinalize:
		return m.applyFinalizeBackup(ctx, logger, cluster, metrics, observation)
	default:
		return recon.Result{}, fmt.Errorf("unsupported backup decision %d", decision.kind)
	}
}

func (m *Manager) applyInitialBackupSchedule(
	ctx context.Context,
	cluster *openbaov1alpha1.OpenBaoCluster,
	observation backupObservation,
) error {
	if !observation.configured || observation.initialNextSchedule.IsZero() || cluster.Status.Backup == nil || cluster.Status.Backup.NextScheduledBackup != nil {
		return nil
	}
	if m.adminOpsMutator == nil {
		return fmt.Errorf("adminops status mutator is required")
	}

	return m.adminOpsMutator(ctx, cluster, func(obj *openbaov1alpha1.OpenBaoCluster) error {
		if obj.Status.Backup == nil {
			obj.Status.Backup = &openbaov1alpha1.BackupStatus{}
		}
		// Preserve a deadline or backup history persisted after our observation.
		if obj.Status.Backup.NextScheduledBackup == nil {
			next := metav1.NewTime(observation.initialNextSchedule)
			obj.Status.Backup.NextScheduledBackup = &next
		}
		return nil
	}, adminops.ForceOwnershipOnConflict)
}

func applyIdleBackup(logger logr.Logger, observation backupObservation) (recon.Result, error) {
	if !observation.configured {
		return recon.Result{}, nil
	}

	requeueAfter := observation.scheduledTime.Sub(observation.now)
	logger.V(1).Info(
		"Backup not due yet",
		"scheduledTime", observation.scheduledTime,
		"now", observation.now,
		"timeUntilDue", requeueAfter,
	)
	return recon.Result{RequeueAfter: requeueAfter}, nil
}

func (m *Manager) applyBlockedBackup(
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	observation backupObservation,
) (recon.Result, error) {
	if observation.manualTriggerToken != "" {
		m.recordManualTriggerAccepted(logger, cluster, observation.manualTriggerToken)
	}

	switch observation.blocker {
	case backupBlockedByRestore:
		if observation.due {
			m.emitNormalEvent(cluster, ReasonBackupSkipped, "Skipping backup because a restore is in progress for cluster %s", cluster.Name)
		}
		logger.Info("Restore in progress; skipping backup reconciliation")
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	case backupBlockedByPrecondition:
		if observation.precondition == nil {
			return recon.Result{}, fmt.Errorf("backup precondition blocker is missing its reason")
		}
		logger.Info("Backup preconditions not met", "reason", observation.precondition.Error())
		if observation.due {
			m.emitPreconditionEvent(cluster, observation.precondition)
		}
		return recon.Result{RequeueAfter: constants.RequeueStandard}, nil
	default:
		return recon.Result{}, fmt.Errorf("unsupported backup blocker %d", observation.blocker)
	}
}

func (m *Manager) applyCreateBackup(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	metrics *Metrics,
	observation backupObservation,
) (recon.Result, error) {
	manualTrigger := observation.manualTriggerToken != ""
	jobName := backupJobName(cluster, observation.scheduledTime)
	if manualTrigger {
		m.recordManualTriggerAccepted(logger, cluster, observation.manualTriggerToken)
		logger.Info("Manual backup triggered, ensuring backup Job", "job", jobName)
	} else {
		logger.Info("Backup is due, ensuring backup Job", "job", jobName)
	}
	metrics.SetInProgress(true)
	lockHeldByUs := backupOperationLock.IsHeldBy(cluster.Status.OperationLock)

	if blockedResult, err := m.acquireBackupLock(ctx, logger, cluster, jobName); err != nil {
		return recon.Result{}, err
	} else if blockedResult != nil {
		return *blockedResult, nil
	}
	if !lockHeldByUs {
		logging.LogAuditEvent(logger, logging.EventOperationLockAcquired, map[string]string{
			"cluster_namespace": cluster.Namespace,
			"cluster_name":      cluster.Name,
			"operation":         string(openbaov1alpha1.ClusterOperationBackup),
			"holder":            backupOperationLockHolder,
		})
		m.emitNormalEvent(cluster, ReasonBackupStarted, "Backup started for schedule %s", observation.scheduledTime.UTC().Format(time.RFC3339))
	}

	if _, err := m.ensureBackupJob(ctx, logger, cluster, jobName, observation.now); err != nil {
		ensureErr := fmt.Errorf("failed to ensure backup Job: %w", err)
		if releaseErr := m.releaseBackupLock(ctx, logger, cluster, "after job ensure failure"); releaseErr != nil {
			return recon.Result{}, errors.Join(ensureErr, releaseErr)
		}
		return recon.Result{}, ensureErr
	}

	if manualTrigger {
		if err := m.clearManualTriggerAnnotation(ctx, logger, cluster); err != nil {
			return recon.Result{}, err
		}
	}
	if err := m.recordBackupAttempt(
		ctx,
		cluster,
		observation.now,
		observation.scheduledTime,
		observation.nextSchedule,
		observation.manualTriggerToken,
	); err != nil {
		logger.Error(err, "Failed to record backup attempt")
	}

	return recon.Result{RequeueAfter: constants.RequeueShort}, nil
}

func (m *Manager) applyObserveBackup(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	metrics *Metrics,
	observation backupObservation,
) (recon.Result, error) {
	if observation.ownsLock {
		if observation.manualTriggerToken != "" {
			if err := m.clearManualTriggerAnnotation(ctx, logger, cluster); err != nil {
				return recon.Result{}, fmt.Errorf("failed to clear manual backup trigger while finishing owned operation: %w", err)
			}
		}
		m.applyBackupJobSnapshotToMetrics(cluster, metrics, backupJobMetricsSnapshot{inProgress: true})
		logger.V(1).Info("Owned backup Job is in progress; requeueing to observe completion")
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	}

	if observation.manualTriggerToken != "" {
		if err := m.skipManualTriggerForActiveJob(ctx, logger, cluster, observation.manualTriggerToken); err != nil {
			return recon.Result{}, err
		}
	}
	logger.V(1).Info("Backup Job in progress; requeueing to observe completion")
	return recon.Result{RequeueAfter: constants.RequeueShort}, nil
}

func (m *Manager) applyFinalizeBackup(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	metrics *Metrics,
	observation backupObservation,
) (recon.Result, error) {
	if observation.ownsLock && observation.manualTriggerToken != "" && observation.jobs.mostRecentTerminal != nil {
		if err := m.clearManualTriggerAnnotation(ctx, logger, cluster); err != nil {
			return recon.Result{}, fmt.Errorf("failed to clear manual backup trigger while finishing owned operation: %w", err)
		}
	}

	jobResult := backupJobProcessResult{}
	if observation.jobs.mostRecentTerminal != nil {
		var err error
		jobResult, err = m.processBackupJob(ctx, logger, cluster, observation.jobs.mostRecentTerminal)
		if err != nil {
			return recon.Result{}, fmt.Errorf("failed to process backup Job: %w", err)
		}
	}
	if jobResult.successfulCompletion && jobResult.statusUpdated {
		m.applyRetentionAfterSuccess(ctx, logger, cluster, metrics)
	}

	if observation.ownsLock {
		if !jobResult.completed {
			logger.Info("Releasing backup operation lock because no active or completed backup Job exists")
		}
		if err := m.releaseBackupLock(ctx, logger, cluster, "after owned Job observation"); err != nil {
			return recon.Result{RequeueAfter: constants.RequeueShort}, nil
		}
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	}

	if jobResult.statusUpdated {
		logger.Info("Found completed backup job, requesting requeue to persist status")
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	}
	return recon.Result{RequeueAfter: observation.scheduledTime.Sub(observation.now)}, nil
}

func (m *Manager) applyRetentionAfterSuccess(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	metrics *Metrics,
) {
	if cluster.Spec.Backup == nil || cluster.Spec.Backup.Retention == nil {
		return
	}
	if err := m.applyRetention(ctx, logger, cluster, metrics); err != nil {
		logger.Error(err, "Failed to apply retention policy")
	}
}

func (m *Manager) acquireBackupLock(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	jobName string,
) (*recon.Result, error) {
	if err := opslifecycle.AcquireWithReader(ctx, m.reader, m.client, cluster, backupOperationLock, opslifecycle.AcquireOptions{
		Message: fmt.Sprintf("backup job %s", jobName),
	}); err != nil {
		if opslifecycle.IsLockHeld(err) {
			fields := map[string]string{
				"cluster_namespace": cluster.Namespace,
				"cluster_name":      cluster.Name,
				"operation":         string(openbaov1alpha1.ClusterOperationBackup),
				"holder":            backupOperationLockHolder,
			}
			opslifecycle.AddHeldAuditFields(fields, err)
			logging.LogAuditEvent(logger, logging.EventOperationLockBlocked, fields)
			m.emitWarningEvent(cluster, ReasonOperationLockBlocked, "Backup blocked by operation lock: %v", err)
			logger.Info("Backup blocked by operation lock", "error", err.Error())
			result := recon.Result{RequeueAfter: opslifecycle.RequeueDelay(opslifecycle.RetryClassStandard)}
			return &result, nil
		}
		return nil, fmt.Errorf("failed to acquire backup operation lock: %w", err)
	}

	return nil, nil
}
