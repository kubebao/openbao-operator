package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func (m *Manager) observeBackup(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	now time.Time,
) (backupObservation, error) {
	observation := backupObservation{
		configured:         cluster.Spec.Backup != nil,
		ownsLock:           backupOperationLock.IsHeldBy(cluster.Status.OperationLock),
		manualTriggerToken: manualTriggerToken(cluster),
		now:                now,
	}

	if observation.ownsLock {
		jobs, err := m.observeBackupJobs(ctx, cluster)
		if err != nil {
			return backupObservation{}, fmt.Errorf("failed to observe owned backup Jobs: %w", err)
		}
		observation.jobs = jobs
		return observation, nil
	}
	if !observation.configured {
		return observation, nil
	}

	schedule, err := ParseSchedule(cluster.Spec.Backup.Schedule)
	if err != nil {
		return backupObservation{}, fmt.Errorf("failed to parse backup schedule: %w", err)
	}

	observation.initialNextSchedule = schedule.Next(now)
	if cluster.Status.Backup != nil && cluster.Status.Backup.NextScheduledBackup != nil {
		persisted := cluster.Status.Backup.NextScheduledBackup.Time
		// Keep a valid overdue occurrence across restarts, but do not let an old
		// deadline hide an earlier occurrence or an updated cron expression.
		if schedule.Next(persisted.Add(-time.Second)).Equal(persisted) &&
			!persisted.After(observation.initialNextSchedule) {
			observation.initialNextSchedule = persisted
		}
	}

	observation.scheduledTime = observation.initialNextSchedule
	if observation.manualTriggerToken != "" {
		observation.scheduledTime = now
	}
	observation.due = observation.manualTriggerToken != "" || !now.Before(observation.scheduledTime)
	observation.nextSchedule = schedule.Next(observation.scheduledTime)
	if !observation.nextSchedule.After(now) {
		observation.nextSchedule = schedule.Next(now)
	}

	jobs, err := m.observeBackupJobs(ctx, cluster)
	if err != nil {
		return backupObservation{}, fmt.Errorf("failed to observe backup Jobs: %w", err)
	}
	observation.jobs = jobs
	if jobs.hasActive {
		return observation, nil
	}

	restoreInProgress, err := m.hasInProgressRestore(ctx, logger, cluster)
	if err != nil {
		return backupObservation{}, err
	}
	if restoreInProgress {
		observation.blocker = backupBlockedByRestore
		return observation, nil
	}

	if err := m.checkPreconditions(ctx, logger, cluster); err != nil {
		var preconditionErr *backupPreconditionError
		if !errors.As(err, &preconditionErr) {
			return backupObservation{}, err
		}
		observation.blocker = backupBlockedByPrecondition
		observation.precondition = preconditionErr
	}

	return observation, nil
}
