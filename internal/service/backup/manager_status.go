package backup

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
)

// patchStatusSSA updates the backup status using Server-Side Apply.
func (m *Manager) patchStatusSSA(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if m.adminOpsMutator == nil {
		return fmt.Errorf("adminops status mutator is required")
	}

	return m.adminOpsMutator(ctx, cluster, func(obj *openbaov1alpha1.OpenBaoCluster) error {
		// RestoreTest has a separate compare-and-set writer. Ordinary backup
		// observations must preserve a reservation made by another reconcile.
		currentBackup := obj.Status.Backup
		obj.Status.Backup = cluster.Status.Backup.DeepCopy()
		if currentBackup != nil && obj.Status.Backup != nil {
			obj.Status.Backup.RestoreTest = currentBackup.RestoreTest.DeepCopy()
			obj.Status.Backup.Retention = currentBackup.Retention.DeepCopy()
		}
		return nil
	}, adminops.ForceOwnershipOnConflict)
}

func (m *Manager) recordBackupAttempt(
	ctx context.Context,
	cluster *openbaov1alpha1.OpenBaoCluster,
	now time.Time,
	scheduledTime time.Time,
	nextScheduled time.Time,
	manualTriggerToken string,
) error {
	if m.adminOpsMutator == nil {
		return fmt.Errorf("adminops status mutator is required")
	}
	uid := cluster.UID
	jobName := backupJobName(cluster, scheduledTime)
	// metav1.Time persists whole seconds. Keep recovered receipts canonical so
	// repeated observations do not rewrite an already recorded attempt.
	nowMeta := metav1.NewTime(now.UTC().Truncate(time.Second))
	scheduledMeta := metav1.NewTime(scheduledTime.UTC().Truncate(time.Second))
	nextMeta := metav1.NewTime(nextScheduled.UTC().Truncate(time.Second))
	return m.adminOpsMutator(ctx, cluster, func(obj *openbaov1alpha1.OpenBaoCluster) error {
		if obj.UID != uid {
			return fmt.Errorf("cluster identity changed while recording backup attempt")
		}
		if !backupOperationLock.IsHeldBy(obj.Status.OperationLock) ||
			obj.Status.OperationLock.Message != backupJobLockMessage(jobName) {
			return fmt.Errorf("backup operation changed while recording attempt for Job %s", jobName)
		}
		if obj.Status.Backup == nil {
			obj.Status.Backup = &openbaov1alpha1.BackupStatus{}
		}
		status := obj.Status.Backup
		status.LastAttemptTime = &nowMeta
		status.LastAttemptScheduledTime = &scheduledMeta
		status.LastHandledManualTrigger = manualTriggerToken
		status.NextScheduledBackup = &nextMeta
		return nil
	}, adminops.ForceOwnershipOnConflict)
}
