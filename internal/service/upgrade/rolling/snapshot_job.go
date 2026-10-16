package rolling

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/kube"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
	snapshothelpers "github.com/kubebao/openbao-operator/internal/service/upgrade/snapshot"
)

// findExistingPreUpgradeBackupJob finds an existing pre-upgrade backup job for this cluster.
// It returns the job name and classified state when present, or an empty name when no current-attempt Job exists.
func (m *Manager) findExistingPreUpgradeBackupJob(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (string, snapshothelpers.JobState, error) {
	expectedJobName := m.backupJobName(cluster)
	job, err := opslifecycle.ReadManagedJob(ctx, m.jobReader(), types.NamespacedName{
		Name:      expectedJobName,
		Namespace: cluster.Namespace,
	}, cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"), "observe pre-upgrade backup")
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", snapshothelpers.JobStateNone, nil
		}
		return "", snapshothelpers.JobStateNone, fmt.Errorf("failed to get backup job %s: %w", expectedJobName, err)
	}

	return job.Name, snapshothelpers.JobStateFromBatchJob(job), nil
}

// backupJobName generates a deterministic name for a pre-upgrade backup job.
// The name is based on cluster generation to ensure idempotency per upgrade operation.
func (m *Manager) backupJobName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return fmt.Sprintf("pre-upgrade-backup-%s-gen%d", cluster.Name, cluster.Generation)
}

// countFailedPreUpgradeBackupJobs counts failed pre-upgrade backup jobs that belong
// to the current upgrade attempt name family (expected name and optional suffixes).
func (m *Manager) countFailedPreUpgradeBackupJobs(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, expectedJobName string) (int, error) {
	jobList := &batchv1.JobList{}
	labelSelector := labels.SelectorFromSet(map[string]string{
		constants.LabelAppInstance:       cluster.Name,
		constants.LabelAppManagedBy:      constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoCluster:    cluster.Name,
		constants.LabelOpenBaoComponent:  constants.ComponentBackup,
		constants.LabelOpenBaoBackupType: constants.BackupTypePreUpgrade,
	})

	if err := m.jobReader().List(ctx, jobList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabelsSelector{Selector: labelSelector},
	); err != nil {
		return 0, fmt.Errorf("failed to list backup jobs: %w", err)
	}

	count := 0
	for i := range jobList.Items {
		job := &jobList.Items[i]
		if !isCurrentAttemptPreUpgradeBackupJobName(job.Name, expectedJobName) {
			continue
		}
		if err := opslifecycle.RequireManagedJobOwner(
			"count failed pre-upgrade backup",
			job,
			cluster,
			openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
		); err != nil {
			return 0, err
		}
		if kube.JobFailed(job) {
			count++
		}
	}
	return count, nil
}

func isCurrentAttemptPreUpgradeBackupJobName(name string, expected string) bool {
	name = strings.TrimSpace(name)
	expected = strings.TrimSpace(expected)
	if name == "" || expected == "" {
		return false
	}
	if name == expected {
		return true
	}
	return strings.HasPrefix(name, expected+"-")
}

// deletePreUpgradeBackupJob deletes a specific pre-upgrade backup job.
func (m *Manager) deletePreUpgradeBackupJob(
	ctx context.Context,
	cluster *openbaov1alpha1.OpenBaoCluster,
	jobName string,
) error {
	job, err := opslifecycle.ReadManagedJob(ctx, m.jobReader(), types.NamespacedName{
		Name:      jobName,
		Namespace: cluster.Namespace,
	}, cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"), "delete pre-upgrade backup")
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to get job: %w", err)
	}

	// Use propagation policy to delete pods as well
	propagation := metav1.DeletePropagationBackground
	if err := m.client.Delete(ctx, job, &client.DeleteOptions{
		PropagationPolicy: &propagation,
		Preconditions:     &metav1.Preconditions{UID: &job.UID},
	}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete job: %w", err)
	}

	return nil
}
