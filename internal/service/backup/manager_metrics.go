package backup

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/kube"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

func (m *Manager) syncBackupMetrics(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, metrics *Metrics) error {
	if metrics == nil {
		return nil
	}

	m.syncBackupStatusMetrics(cluster, metrics)

	snapshot, err := m.collectBackupJobMetricsSnapshot(ctx, cluster, metrics)
	if err != nil {
		return err
	}
	m.applyBackupJobSnapshotToMetrics(cluster, metrics, snapshot)

	if err := m.backfillBackupGaugesFromLatestSuccess(ctx, logger, cluster, metrics, snapshot.newestSucceeded); err != nil {
		return err
	}

	return nil
}

type backupJobMetricsSnapshot struct {
	inProgress      bool
	newestSucceeded *batchv1.Job
	newestFailed    *batchv1.Job
}

func (m *Manager) syncBackupStatusMetrics(cluster *openbaov1alpha1.OpenBaoCluster, metrics *Metrics) {
	// Reflect last known status values (best effort).
	if cluster == nil || metrics == nil || cluster.Status.Backup == nil {
		return
	}

	metrics.SetConsecutiveFailures(cluster.Status.Backup.ConsecutiveFailures)

	if cluster.Status.Backup.LastAttemptTime != nil {
		metrics.SetLastAttemptTimestamp(float64(cluster.Status.Backup.LastAttemptTime.Unix()))
	}

	if cluster.Status.Backup.LastBackupTime != nil {
		metrics.SetLastSuccessTimestamp(float64(cluster.Status.Backup.LastBackupTime.Unix()))
	}

	if cluster.Status.Backup.LastBackupSize > 0 {
		metrics.SetLastSize(cluster.Status.Backup.LastBackupSize)
	}

	if cluster.Status.Backup.LastBackupDuration != "" {
		if d, err := time.ParseDuration(cluster.Status.Backup.LastBackupDuration); err == nil {
			metrics.SetLastDuration(d.Seconds())
		}
	}
}

func (m *Manager) collectBackupJobMetricsSnapshot(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, metrics *Metrics) (backupJobMetricsSnapshot, error) {
	jobList := &batchv1.JobList{}
	labelSelector := labels.SelectorFromSet(map[string]string{
		constants.LabelAppInstance:       cluster.Name,
		constants.LabelAppManagedBy:      constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoCluster:    cluster.Name,
		constants.LabelOpenBaoComponent:  ComponentBackup,
		constants.LabelOpenBaoBackupType: constants.BackupTypeScheduled,
	})

	if err := m.reader.List(ctx, jobList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabelsSelector{Selector: labelSelector},
	); err != nil {
		return backupJobMetricsSnapshot{}, fmt.Errorf("failed to list backup jobs for metrics sync: %w", err)
	}

	var snapshot backupJobMetricsSnapshot
	for i := range jobList.Items {
		job := &jobList.Items[i]
		if err := opslifecycle.RequireManagedJobOwner(
			"collect backup metrics from",
			job,
			cluster,
			openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
		); err != nil {
			return backupJobMetricsSnapshot{}, err
		}

		switch {
		case kube.JobSucceeded(job):
			if markBackupJobMetricsSeen(cluster.Namespace, cluster.Name, job.UID, "success") {
				metrics.IncrementSuccessTotal()
			}
			snapshot.newestSucceeded = newestBackupJob(snapshot.newestSucceeded, job)
		case kube.JobFailed(job):
			if markBackupJobMetricsSeen(cluster.Namespace, cluster.Name, job.UID, "failure") {
				metrics.IncrementFailureTotal()
			}
			snapshot.newestFailed = newestBackupJob(snapshot.newestFailed, job)
		default:
			snapshot.inProgress = true
		}
	}

	return snapshot, nil
}

func newestBackupJob(a, b *batchv1.Job) *batchv1.Job {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if backupJobTimestamp(b).After(backupJobTimestamp(a)) {
		return b
	}
	return a
}

func backupJobTimestamp(job *batchv1.Job) time.Time {
	if job == nil {
		return time.Time{}
	}
	if job.Status.CompletionTime != nil {
		return job.Status.CompletionTime.Time
	}
	return job.CreationTimestamp.Time
}

func (m *Manager) applyBackupJobSnapshotToMetrics(cluster *openbaov1alpha1.OpenBaoCluster, metrics *Metrics, snapshot backupJobMetricsSnapshot) {
	if metrics == nil {
		return
	}

	metrics.SetInProgress(snapshot.inProgress)

	// Set state for dashboards (state timeline panels).
	// Priority: InProgress > most recent completed job > status-derived last known state > None.
	if snapshot.inProgress {
		metrics.SetState(3)
		return
	}

	if outcome, at, ok := latestBackupOutcome(snapshot); ok {
		if !at.IsZero() {
			metrics.SetLastAttemptTimestamp(float64(at.Unix()))
		}
		metrics.SetState(outcome)
		return
	}

	if cluster != nil && cluster.Status.Backup != nil {
		if cluster.Status.Backup.LastBackupTime != nil {
			metrics.SetState(1)
			return
		}
		if cluster.Status.Backup.ConsecutiveFailures > 0 {
			metrics.SetState(2)
			return
		}
	}

	metrics.SetState(0)
}

func latestBackupOutcome(snapshot backupJobMetricsSnapshot) (outcome float64, at time.Time, ok bool) {
	if snapshot.newestSucceeded == nil && snapshot.newestFailed == nil {
		return 0, time.Time{}, false
	}

	if snapshot.newestSucceeded != nil {
		ok = true
		outcome = 1
		at = backupJobTimestamp(snapshot.newestSucceeded)
	}
	if snapshot.newestFailed != nil {
		timestamp := backupJobTimestamp(snapshot.newestFailed)
		if !ok || timestamp.After(at) {
			ok = true
			outcome = 2
			at = timestamp
		}
	}

	return outcome, at, ok
}

func (m *Manager) backfillBackupGaugesFromLatestSuccess(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, metrics *Metrics, newestSucceeded *batchv1.Job) error {
	if metrics == nil || newestSucceeded == nil || cluster == nil || cluster.Status.Backup == nil {
		return nil
	}

	statusChanged := false

	if cluster.Status.Backup.LastBackupTime == nil {
		metrics.SetLastSuccessTimestamp(float64(backupJobTimestamp(newestSucceeded).Unix()))
	}

	if duration, ok := jobDuration(newestSucceeded); ok {
		metrics.SetLastDuration(duration.Seconds())
		if cluster.Status.Backup.LastBackupDuration == "" {
			cluster.Status.Backup.LastBackupDuration = duration.String()
			statusChanged = true
		}
	}

	if key := backupJobKey(newestSucceeded); key != "" && shouldReadBackupSizeFromObjectStorage(cluster) {
		size, err := m.readBackupSizeFromObjectStorage(ctx, cluster, key)
		if err != nil {
			logger.V(1).Info("Failed to read backup size from object storage", "backupKey", key, "error", err.Error())
		} else if size > 0 {
			metrics.SetLastSize(size)
			if cluster.Status.Backup.LastBackupSize == 0 {
				cluster.Status.Backup.LastBackupSize = size
				statusChanged = true
			}
		}
	}

	if statusChanged {
		if err := m.patchStatusSSA(ctx, cluster); err != nil {
			return fmt.Errorf("failed to patch backup status after metrics backfill: %w", err)
		}
	}

	return nil
}

func jobDuration(job *batchv1.Job) (time.Duration, bool) {
	if job == nil || job.Status.StartTime == nil || job.Status.CompletionTime == nil {
		return 0, false
	}
	duration := job.Status.CompletionTime.Sub(job.Status.StartTime.Time)
	if duration <= 0 {
		return 0, false
	}
	return duration, true
}

func backupJobKey(job *batchv1.Job) string {
	if job == nil || job.Annotations == nil {
		return ""
	}
	return job.Annotations["openbao.org/backup-key"]
}

func shouldReadBackupSizeFromObjectStorage(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster != nil &&
		cluster.Spec.Backup != nil &&
		cluster.Spec.Backup.Target.EffectiveS3RoleARN() == "" &&
		cluster.Spec.Backup.Target.CredentialsSecretRef != nil &&
		cluster.Spec.Backup.Target.CredentialsSecretRef.Name != ""
}

func (m *Manager) readBackupSizeFromObjectStorage(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, backupKey string) (int64, error) {
	if cluster == nil || cluster.Spec.Backup == nil || backupKey == "" {
		return 0, nil
	}

	storageClient, err := m.openBackupStorageClient(ctx, cluster, false)
	if err != nil {
		return 0, fmt.Errorf("failed to create storage client: %w", err)
	}
	defer func() { _ = storageClient.Close() }()

	info, err := storageClient.Head(ctx, backupKey)
	if err != nil {
		return 0, fmt.Errorf("failed to head object %q: %w", backupKey, err)
	}
	if info == nil {
		return 0, nil
	}
	return info.Size, nil
}
