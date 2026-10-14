package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/kube"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

func (m *Manager) reconcileRetention(ctx context.Context, logger logr.Logger, cluster *api.OpenBaoCluster, now time.Time) (recon.Result, error) {
	if !retentionActive(cluster) {
		return recon.Result{}, nil
	}
	run := cluster.Status.Backup.Retention.Active.DeepCopy()
	job := &batchv1.Job{}
	err := m.reader.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: run.Name}, job)
	if apierrors.IsNotFound(err) {
		if run.SubmittedAt != nil {
			return retentionPollResult(), m.blockRetention(ctx, cluster, "ExecutionUnknown",
				"The submitted retention Job is absent; inspect executor termination before clearing the reservation")
		}
		return m.submitRetention(ctx, logger, cluster, run, now)
	}
	if err != nil {
		return retentionPollResult(), err
	}
	if err := opslifecycle.RequireManagedJobOwner("observe retention", job, cluster,
		api.GroupVersion.WithKind("OpenBaoCluster")); err != nil {
		return retentionPollResult(), m.blockRetention(ctx, cluster, "JobIdentityChanged",
			"Retention Job ownership changed; inspect the previous executor before clearing the reservation")
	}
	if (run.UID != "" && run.UID != job.UID) || job.UID == "" ||
		job.Labels[labelRetentionRun] != string(run.SourceJobUID) || job.Labels[constants.LabelOpenBaoBackupType] != retentionType {
		return retentionPollResult(), m.blockRetention(ctx, cluster, "JobIdentityChanged",
			"Retention Job identity changed; inspect the previous executor before clearing the reservation")
	}
	if run.UID == "" {
		if run.SubmittedAt == nil {
			return retentionPollResult(), m.blockRetention(ctx, cluster, "UnexpectedJob",
				"A retention Job exists without a submitted reservation; inspect it before clearing the reservation")
		}
		expected := cluster.Status.Backup.Retention.DeepCopy()
		return retentionPollResult(), m.mutateRetention(ctx, cluster, expected,
			func(_ *api.OpenBaoCluster, status *api.BackupRetentionStatus) error {
				status.Active.UID = job.UID
				return nil
			})
	}
	if cluster.DeletionTimestamp != nil || !backupOperationLock.IsHeldBy(cluster.Status.OperationLock) {
		if err := m.cancelRetentionJob(ctx, job); err != nil {
			return retentionPollResult(), err
		}
	}
	stopped, report, observationErr := m.observeRetentionPods(ctx, job, run)
	if observationErr != nil {
		if !stopped {
			return retentionPollResult(), observationErr
		}
		report = nil
	}
	if !stopped {
		return retentionPollResult(), m.blockRetention(ctx, cluster, "AwaitingTermination",
			"Waiting for all retention containers to terminate; missing Pod evidence requires administrator inspection")
	}
	if !kube.JobSucceeded(job) && !kube.JobFailed(job) {
		return retentionPollResult(), nil
	}
	outcome, reason, message := api.BackupRetentionFailed, "ExecutorFailed",
		"Retention failed after a successful snapshot; inspect the retention Job; the next successful backup can retry"
	if report == nil {
		reason, message = "OutcomeUnverified", "Retention executors stopped without one consistent outcome report; snapshot success is preserved"
	} else if kube.JobSucceeded(job) && report.Succeeded {
		outcome, reason, message = api.BackupRetentionSucceeded, RetentionReasonCompleted,
			"Retention completed after a successful snapshot"
	} else if !report.Succeeded {
		switch report.Reason {
		case RetentionReasonStorageAccessFailed, RetentionReasonStorageListFailed,
			RetentionReasonStorageDeleteFailed, RetentionReasonDeadlineExceeded:
			reason = report.Reason
		}
	}
	if err := m.finishRetention(ctx, cluster, outcome, reason, message, report, now); err != nil {
		return retentionPollResult(), err
	}
	before := job.DeepCopy()
	ttl := int32(backupJobTTLSeconds)
	job.Spec.TTLSecondsAfterFinished = &ttl
	if err := m.client.Patch(ctx, job, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		logger.Error(err, "Failed to configure completed retention Job cleanup")
	}
	if backupOperationLock.IsHeldBy(cluster.Status.OperationLock) {
		if err := m.releaseRetentionLock(ctx, cluster, run.SourceJobName); err != nil {
			return retentionPollResult(), err
		}
	}
	return recon.Result{RequeueAfter: constants.RequeueShort}, nil
}

func (m *Manager) submitRetention(ctx context.Context, logger logr.Logger, cluster *api.OpenBaoCluster, run *api.BackupRetentionRun, now time.Time) (recon.Result, error) {
	if cluster.DeletionTimestamp != nil || cluster.Generation != run.Generation || !now.Before(run.DeadlineAt.Time) ||
		!backupOperationLock.IsHeldBy(cluster.Status.OperationLock) {
		err := m.finishRetention(ctx, cluster, api.BackupRetentionSkipped, "SubmissionCanceled",
			"Retention was not submitted because its source configuration, deadline, or operation authority changed", nil, now)
		return retentionPollResult(), err
	}
	source := &batchv1.Job{}
	if err := m.reader.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: run.SourceJobName}, source); err != nil {
		if apierrors.IsNotFound(err) {
			return retentionPollResult(), m.finishRetention(ctx, cluster, api.BackupRetentionSkipped, "SourceJobMissing",
				"Retention was not submitted because its successful source Job is absent", nil, now)
		}
		return retentionPollResult(), err
	}
	if source.UID != run.SourceJobUID {
		return retentionPollResult(), m.finishRetention(ctx, cluster, api.BackupRetentionSkipped, "SourceJobChanged",
			"Retention was not submitted because the successful source Job changed", nil, now)
	}
	if err := opslifecycle.RequireManagedJobOwner("build retention from", source, cluster,
		api.GroupVersion.WithKind("OpenBaoCluster")); err != nil {
		return retentionPollResult(), err
	}
	job, err := buildRetentionJob(source, run)
	if err != nil {
		return retentionPollResult(), m.finishRetention(ctx, cluster, api.BackupRetentionSkipped, "UnsupportedExecutor",
			"Retention needs a supported backup executor template; update the backup executor before the next backup", nil, now)
	}
	if err := opslifecycle.PrepareManagedJobOwner(job, cluster, m.scheme); err != nil {
		return retentionPollResult(), err
	}
	expected := cluster.Status.Backup.Retention.DeepCopy()
	if err := m.mutateRetention(ctx, cluster, expected, func(current *api.OpenBaoCluster, status *api.BackupRetentionStatus) error {
		if current.Generation != run.Generation || current.DeletionTimestamp != nil ||
			!backupOperationLock.IsHeldBy(current.Status.OperationLock) || current.Status.Backup.LastBackupName != run.Key {
			return fmt.Errorf("retention submission authority changed; reconcile again")
		}
		stamp := metav1.NewTime(now)
		status.Active.SubmittedAt = &stamp
		retentionCondition(status, run.Generation, metav1.ConditionUnknown, "Submitted", "Retention has one submitted workload attempt")
		return nil
	}); err != nil {
		// A status write with a lost read-back must never authorize Create.
		return retentionPollResult(), err
	}
	if err := m.client.Create(ctx, job); err != nil {
		logger.Error(err, "Retention Job submission failed", "cluster_namespace", cluster.Namespace,
			"cluster_name", cluster.Name, "job", job.Name)
		if apierrors.IsForbidden(err) || apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) || apierrors.IsUnauthorized(err) {
			return retentionPollResult(), m.finishRetention(ctx, cluster, api.BackupRetentionFailed, "SubmissionRejected",
				"Retention admission rejected the workload; inspect namespace policy and executor configuration before the next backup", nil, now)
		}
		return retentionPollResult(), m.blockRetention(ctx, cluster, "SubmissionUncertain",
			"Retention submission acknowledgement was lost; the operator observes the deterministic Job and never repeats Create")
	}
	// Bind only through a fresh observation; a restart uses the same identity.
	return retentionPollResult(), nil
}

func (m *Manager) cancelRetentionJob(ctx context.Context, job *batchv1.Job) error {
	if kube.JobSucceeded(job) || kube.JobFailed(job) || (job.Spec.ActiveDeadlineSeconds != nil && *job.Spec.ActiveDeadlineSeconds == 1) {
		return nil
	}
	before := job.DeepCopy()
	deadline := int64(1)
	job.Spec.ActiveDeadlineSeconds = &deadline
	if err := m.client.Patch(ctx, job, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("cancel retention Job: %w", err)
	}
	return nil
}

func (m *Manager) releaseRetentionLock(ctx context.Context, cluster *api.OpenBaoCluster, sourceJobName string) error {
	err := opslifecycle.ReleaseWithReader(ctx, retentionGuardReader{Reader: m.reader, uid: cluster.UID,
		lockMessage: fmt.Sprintf("backup job %s", sourceJobName)}, m.client, cluster, backupOperationLock)
	if errors.Is(err, errBackupOperationChanged) || opslifecycle.IsLockHeld(err) {
		// The stopped retention run cannot release another operation's lock.
		return nil
	}
	return err
}

func retentionPollResult() recon.Result {
	return recon.Result{RequeueAfter: constants.RequeueShort}
}
