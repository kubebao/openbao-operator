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
	if cluster.Status.Backup == nil {
		cluster.Status.Backup = &openbaov1alpha1.BackupStatus{}
	}

	nowMeta := metav1.NewTime(now)
	cluster.Status.Backup.LastAttemptTime = &nowMeta

	scheduledMeta := metav1.NewTime(scheduledTime)
	cluster.Status.Backup.LastAttemptScheduledTime = &scheduledMeta
	cluster.Status.Backup.LastHandledManualTrigger = manualTriggerToken

	nextScheduledMeta := metav1.NewTime(nextScheduled)
	cluster.Status.Backup.NextScheduledBackup = &nextScheduledMeta

	return m.patchStatusSSA(ctx, cluster)
}
