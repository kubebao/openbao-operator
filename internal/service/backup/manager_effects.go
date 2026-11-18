package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
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
	current, err := m.applyBackupSchedule(ctx, cluster, observation)
	if err != nil {
		return recon.Result{}, fmt.Errorf("failed to persist backup schedule: %w", err)
	}
	if !current {
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
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
		return m.applyFinalizeBackup(ctx, logger, cluster, observation)
	default:
		return recon.Result{}, fmt.Errorf("unsupported backup decision %d", decision.kind)
	}
}

func (m *Manager) applyBackupSchedule(
	ctx context.Context,
	cluster *openbaov1alpha1.OpenBaoCluster,
	observation backupObservation,
) (bool, error) {
	if !observation.configured || observation.initialNextSchedule.IsZero() || cluster.Status.Backup == nil {
		return true, nil
	}
	next := metav1.NewTime(observation.initialNextSchedule)
	previous := cluster.Status.Backup.NextScheduledBackup.DeepCopy()
	if previous.Equal(&next) && !observation.due {
		return true, nil
	}
	schedule := cluster.Spec.Backup.Schedule
	uid := cluster.UID
	if m.adminOpsMutator == nil {
		return false, fmt.Errorf("adminops status mutator is required")
	}

	current := false
	err := m.adminOpsMutator(ctx, cluster, func(obj *openbaov1alpha1.OpenBaoCluster) error {
		current = false
		if obj.UID != uid || obj.Spec.Backup == nil || obj.Spec.Backup.Schedule != schedule {
			return nil
		}
		var liveNext *metav1.Time
		if obj.Status.Backup != nil {
			liveNext = obj.Status.Backup.NextScheduledBackup
		}
		// A newer spec or deadline invalidates the decision as well as the write.
		// The caller must re-observe before it can create a backup Job.
		if !previous.Equal(liveNext) {
			return nil
		}
		if obj.Status.Backup == nil {
			obj.Status.Backup = &openbaov1alpha1.BackupStatus{}
		}
		obj.Status.Backup.NextScheduledBackup = &next
		current = true
		return nil
	}, adminops.ForceOwnershipOnConflict)
	if err != nil {
		return false, err
	}
	return current && cluster.Status.Backup != nil && next.Equal(cluster.Status.Backup.NextScheduledBackup), nil
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

	attempt := backupAttemptReceipt{
		Time: observation.now, ScheduledTime: observation.scheduledTime,
		NextScheduled: observation.nextSchedule, ManualTrigger: observation.manualTriggerToken,
	}
	if err := m.markBackupSubmission(ctx, cluster, jobName); err != nil {
		return recon.Result{}, fmt.Errorf("mark backup Job submission: %w", err)
	}
	if _, err := m.ensureBackupJob(ctx, logger, cluster, jobName, attempt); err != nil {
		ensureErr := fmt.Errorf("failed to ensure backup Job: %w", err)
		if backupSubmissionUncertain(err) {
			return recon.Result{}, ensureErr
		}
		if releaseErr := m.releaseBackupLock(ctx, logger, cluster, "after job ensure failure"); releaseErr != nil {
			return recon.Result{}, errors.Join(ensureErr, releaseErr)
		}
		return recon.Result{}, ensureErr
	}

	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: cluster.Namespace}}
	receipt, err := m.recoverBackupAttempt(ctx, cluster, job)
	if err != nil {
		return recon.Result{}, err
	}
	if receipt == nil && !manualTrigger {
		// A legacy Job proves this scheduled occurrence already exists, but
		// cannot prove which manual request its creator observed.
		if err := m.recordBackupAttempt(ctx, cluster, attempt.Time, attempt.ScheduledTime, attempt.NextScheduled, ""); err != nil {
			return recon.Result{}, err
		}
	}
	if receipt != nil && receipt.ManualTrigger != "" {
		if err := m.clearManualTriggerAnnotation(ctx, logger, cluster, receipt.ManualTrigger); err != nil {
			return recon.Result{}, err
		}
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
	job := observation.jobs.operationJob
	if !observation.ownsLock {
		job = observation.jobs.activeJob
		if job != nil {
			if blocked, err := m.acquireBackupLock(ctx, logger, cluster, job.Name); err != nil {
				return recon.Result{}, err
			} else if blocked != nil {
				return *blocked, nil
			}
		}
	}
	receipt, err := m.recoverBackupAttempt(ctx, cluster, job)
	if err != nil {
		return recon.Result{}, err
	}
	if receipt == nil && job != nil {
		if err := m.recoverLegacyBackupSchedule(ctx, cluster, job); err != nil {
			return recon.Result{}, err
		}
	}
	if receipt != nil && receipt.ManualTrigger != "" && receipt.ManualTrigger == observation.manualTriggerToken {
		if err := m.skipManualTriggerForActiveJob(ctx, logger, cluster, receipt.ManualTrigger); err != nil {
			return recon.Result{}, err
		}
	}
	m.applyBackupJobSnapshotToMetrics(cluster, metrics, backupJobMetricsSnapshot{inProgress: true})
	logger.V(1).Info("Backup Job in progress; requeueing to observe completion")
	return recon.Result{RequeueAfter: constants.RequeueShort}, nil
}

func (m *Manager) applyFinalizeBackup(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	observation backupObservation,
) (recon.Result, error) {
	job := observation.jobs.mostRecentTerminal
	if observation.ownsLock {
		// The controller can stop between acquiring the lock and creating its
		// Job. An unrelated terminal Job must not prevent lock release.
		job = observation.jobs.operationJob
		if job == nil && pendingBackupSubmission(cluster) != "" {
			return recon.Result{}, fmt.Errorf("backup Job %s/%s submission is uncertain and the Job is not observable; inspect its outcome before clearing the operation lock",
				cluster.Namespace, pendingBackupSubmission(cluster))
		}
	}
	if !observation.ownsLock && observation.jobs.pendingSuccess != nil {
		job = observation.jobs.pendingSuccess
		if blocked, err := m.acquireBackupLock(ctx, logger, cluster, job.Name); err != nil {
			return recon.Result{}, err
		} else if blocked != nil {
			return *blocked, nil
		}
		observation.ownsLock = true
	}
	if observation.ownsLock {
		receipt, err := m.recoverBackupAttempt(ctx, cluster, job)
		if err != nil {
			return recon.Result{}, err
		}
		if receipt == nil && job != nil {
			if err := m.recoverLegacyBackupSchedule(ctx, cluster, job); err != nil {
				return recon.Result{}, err
			}
		}
		if receipt != nil && receipt.ManualTrigger != "" {
			if err := m.clearManualTriggerAnnotation(ctx, logger, cluster, receipt.ManualTrigger); err != nil {
				return recon.Result{}, fmt.Errorf("failed to clear manual backup trigger while finishing owned operation: %w", err)
			}
		}
	}

	jobResult := backupJobProcessResult{}
	if job != nil {
		var err error
		jobResult, err = m.processBackupJob(ctx, logger, cluster, job)
		if err != nil {
			return recon.Result{}, fmt.Errorf("failed to process backup Job: %w", err)
		}
	}
	// Snapshot status can survive a lost read-back or a controller restart before
	// retention is reserved. The source Job identity, not statusUpdated, deduplicates cleanup.
	if jobResult.successfulCompletion && job != nil {
		if err := m.reserveRetention(ctx, cluster, job, observation.now); err != nil {
			return retentionPollResult(), err
		}
		if retentionActive(cluster) {
			return m.reconcileRetention(ctx, logger, cluster, observation.now)
		}
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

func (m *Manager) acquireBackupLock(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	jobName string,
) (*recon.Result, error) {
	if err := opslifecycle.AcquireWithReader(ctx,
		retentionGuardReader{Reader: m.reader, uid: cluster.UID, lockMessage: backupJobLockMessage(jobName)},
		m.client, cluster, backupOperationLock, opslifecycle.AcquireOptions{
			Message: backupJobLockMessage(jobName),
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
