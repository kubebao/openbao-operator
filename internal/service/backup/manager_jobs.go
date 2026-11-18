package backup

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/kube"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

// hasPreUpgradeBackupJob checks if there's a pre-upgrade backup job running or pending for this cluster.
// This is used to prevent regular scheduled backups from starting when an upgrade is initiating.
func (m *Manager) hasPreUpgradeBackupJob(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (bool, error) {
	jobList := &batchv1.JobList{}
	labelSelector := labels.SelectorFromSet(map[string]string{
		constants.LabelAppInstance:       cluster.Name,
		constants.LabelAppManagedBy:      constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoCluster:    cluster.Name,
		constants.LabelOpenBaoComponent:  ComponentBackup,
		constants.LabelOpenBaoBackupType: "pre-upgrade",
	})

	if err := m.reader.List(ctx, jobList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabelsSelector{Selector: labelSelector},
	); err != nil {
		return false, fmt.Errorf("failed to list pre-upgrade backup jobs: %w", err)
	}

	// Check if there's a running or pending job (not yet succeeded or failed).
	for i := range jobList.Items {
		job := &jobList.Items[i]
		if err := opslifecycle.RequireManagedJobOwner(
			"observe pre-upgrade backup",
			job,
			cluster,
			openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
		); err != nil {
			return false, err
		}
		// If job hasn't succeeded or failed, it's still running or pending.
		if !kube.JobSucceeded(job) && !kube.JobFailed(job) {
			return true, nil
		}
	}

	return false, nil
}

type backupJobObservation struct {
	hasActive          bool
	activeJob          *batchv1.Job
	operationJob       *batchv1.Job
	operationActive    bool
	mostRecentTerminal *batchv1.Job
	pendingSuccess     *batchv1.Job
}

// observeBackupJobs reads active and terminal Job state from one live API snapshot.
func (m *Manager) observeBackupJobs(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (backupJobObservation, error) {
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
		return backupJobObservation{}, fmt.Errorf("failed to list backup jobs: %w", err)
	}

	observation := backupJobObservation{}
	for i := range jobList.Items {
		job := &jobList.Items[i]
		if err := opslifecycle.RequireManagedJobOwner(
			"observe backup",
			job,
			cluster,
			openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
		); err != nil {
			return backupJobObservation{}, err
		}
		if backupLockNamesJob(cluster.Status.OperationLock, job.Name) {
			observation.operationJob = job.DeepCopy()
			observation.operationActive = !kube.JobSucceeded(job) && !kube.JobFailed(job)
		}
		if !kube.JobSucceeded(job) && !kube.JobFailed(job) {
			observation.hasActive = true
			if observation.activeJob == nil || job.CreationTimestamp.Before(&observation.activeJob.CreationTimestamp) ||
				(job.CreationTimestamp.Equal(&observation.activeJob.CreationTimestamp) && job.Name < observation.activeJob.Name) {
				observation.activeJob = job.DeepCopy()
			}
			continue
		}
		if backupSuccessNeedsProcessing(cluster, job) &&
			(observation.pendingSuccess == nil || job.CreationTimestamp.After(observation.pendingSuccess.CreationTimestamp.Time)) {
			observation.pendingSuccess = job.DeepCopy()
		}
		if observation.mostRecentTerminal == nil || job.CreationTimestamp.After(observation.mostRecentTerminal.CreationTimestamp.Time) {
			observation.mostRecentTerminal = job.DeepCopy()
		}
	}
	return observation, nil
}

func backupSuccessNeedsProcessing(cluster *openbaov1alpha1.OpenBaoCluster, job *batchv1.Job) bool {
	if job == nil || !kube.JobSucceeded(job) {
		return false
	}
	status := cluster.Status.Backup
	if status == nil {
		return true
	}
	if status.LastBackupName != job.Annotations["openbao.org/backup-key"] {
		return status.LastBackupTime == nil || !job.CreationTimestamp.Before(status.LastBackupTime)
	}
	if cluster.Spec.Backup == nil || cluster.Spec.Backup.Retention == nil || job.UID == "" {
		return false
	}
	policy := cluster.Spec.Backup.Retention
	if policy.MaxCount == 0 && policy.MaxAge == "" {
		return false
	}
	return status.Retention == nil || status.Retention.Last == nil || status.Retention.Last.SourceJobUID != job.UID
}
