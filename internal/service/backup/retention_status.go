package backup

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
)

func retentionActive(cluster *api.OpenBaoCluster) bool {
	return cluster.Status.Backup != nil && cluster.Status.Backup.Retention != nil &&
		cluster.Status.Backup.Retention.Active != nil
}

func (m *Manager) mutateRetention(ctx context.Context, cluster *api.OpenBaoCluster,
	expected *api.BackupRetentionStatus, mutate func(*api.OpenBaoCluster, *api.BackupRetentionStatus) error) error {
	if m.adminOpsMutator == nil {
		return fmt.Errorf("adminops status mutator is required")
	}
	return m.adminOpsMutator(ctx, cluster, func(current *api.OpenBaoCluster) error {
		if current.UID != cluster.UID || current.Status.Backup == nil ||
			!equality.Semantic.DeepEqual(current.Status.Backup.Retention, expected) {
			return fmt.Errorf("retention authority changed; reconcile again")
		}
		if current.Status.Backup.Retention == nil {
			current.Status.Backup.Retention = &api.BackupRetentionStatus{}
		}
		return mutate(current, current.Status.Backup.Retention)
	}, adminops.ForceOwnershipOnConflict)
}

func retentionCondition(status *api.BackupRetentionStatus, generation int64, value metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: api.BackupRetentionSucceededConditionType,
		Status: value, Reason: reason, Message: message, ObservedGeneration: generation})
}

func (m *Manager) finishRetention(ctx context.Context, cluster *api.OpenBaoCluster,
	outcome api.BackupRetentionOutcome, reason, message string, report *RetentionReport, now time.Time) error {
	expected := cluster.Status.Backup.Retention.DeepCopy()
	if expected == nil || expected.Active == nil {
		return fmt.Errorf("active retention reservation is required")
	}
	run := expected.Active
	err := m.mutateRetention(ctx, cluster, expected, func(_ *api.OpenBaoCluster, status *api.BackupRetentionStatus) error {
		status.Last = &api.BackupRetentionResult{Name: run.Name, SourceJobUID: run.SourceJobUID, Key: run.Key,
			StartedAt: run.StartedAt, FinishedAt: metav1.NewTime(now), Outcome: outcome, Reason: reason, Message: message}
		if report != nil && report.Succeeded {
			status.Last.DeletedByCount, status.Last.DeletedByAge = report.DeletedByCount, report.DeletedByAge
		}
		status.Active = nil
		value := metav1.ConditionFalse
		if outcome == api.BackupRetentionSucceeded {
			value = metav1.ConditionTrue
		}
		retentionCondition(status, run.Generation, value, reason, message)
		return nil
	})
	if err != nil {
		return err
	}
	if outcome == api.BackupRetentionSucceeded {
		m.emitNormalEvent(cluster, "BackupRetentionCompleted", "%s", message)
		if report != nil {
			NewMetrics(cluster.Namespace, cluster.Name).IncrementRetentionDeleted(int(report.DeletedByCount) + int(report.DeletedByAge))
		}
	} else {
		m.emitWarningEvent(cluster, "BackupRetention"+string(outcome), "%s", message)
	}
	return nil
}

func (m *Manager) blockRetention(ctx context.Context, cluster *api.OpenBaoCluster, reason, message string) error {
	expected := cluster.Status.Backup.Retention.DeepCopy()
	return m.mutateRetention(ctx, cluster, expected, func(_ *api.OpenBaoCluster, status *api.BackupRetentionStatus) error {
		retentionCondition(status, expected.Active.Generation, metav1.ConditionUnknown, reason, message)
		return nil
	})
}
