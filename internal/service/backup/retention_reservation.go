package backup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

func (m *Manager) reserveRetention(ctx context.Context, cluster *api.OpenBaoCluster, source *batchv1.Job, now time.Time) error {
	if cluster.Spec.Backup == nil || cluster.Spec.Backup.Retention == nil || source == nil || retentionActive(cluster) {
		return nil
	}
	policy := cluster.Spec.Backup.Retention
	if policy.MaxCount == 0 && policy.MaxAge == "" {
		return nil
	}
	if last := cluster.Status.Backup.Retention; last != nil && last.Last != nil && last.Last.SourceJobUID == source.UID {
		return nil
	}
	key := source.Annotations["openbao.org/backup-key"]
	if source.UID == "" || cluster.Status.Backup.LastBackupName != key {
		return nil
	}
	expectedBackup := cluster.Status.Backup.DeepCopy()
	expectedGeneration := cluster.Generation
	generation, parseErr := strconv.ParseInt(source.Annotations[annotationBackupGeneration], 10, 64)
	prefix := GetBackupListPrefix(cluster.Spec.Backup.Target.PathPrefix, cluster.Namespace, cluster.Name)
	digest := sha256.Sum256([]byte(string(cluster.UID) + "/" + string(source.UID)))
	run := &api.BackupRetentionRun{Name: fmt.Sprintf("retention-%x", digest[:12]),
		SourceJobName: source.Name, SourceJobUID: source.UID, Generation: expectedGeneration, Key: key, Prefix: prefix,
		Policy: *policy.DeepCopy(), StartedAt: metav1.NewTime(now), DeadlineAt: metav1.NewTime(now.Add(retentionTimeout))}
	reason, message := "", ""
	if parseErr != nil || generation != expectedGeneration {
		reason, message = "SourceConfigurationChanged", "Retention skipped because the successful backup uses a different or unknown configuration generation; the next successful backup can retry"
	} else if !IsScheduledBackupKey(key, prefix) {
		reason, message = "UnsupportedSnapshotKey", "Retention skipped because the successful backup key is outside the scheduled snapshot boundary"
	} else if _, err := ParseRetentionMaxAge(policy.MaxAge); err != nil {
		reason, message = "InvalidPolicy", "Retention skipped because maxAge is invalid; correct the policy before the next backup"
	}
	if m.adminOpsMutator == nil {
		return fmt.Errorf("adminops status mutator is required")
	}
	if reason == "" && !backupOperationLock.IsHeldBy(cluster.Status.OperationLock) {
		if _, err := opslifecycle.AcquireWithReaderResult(ctx,
			retentionGuardReader{Reader: m.reader, uid: cluster.UID}, m.client, cluster, backupOperationLock,
			opslifecycle.AcquireOptions{Message: fmt.Sprintf("backup job %s", source.Name)}); err != nil {
			return err
		}
	}
	err := m.adminOpsMutator(ctx, cluster, func(current *api.OpenBaoCluster) error {
		if err := validateRetentionReservationAuthority(
			current, cluster.UID, expectedBackup, run, reason == "",
		); err != nil {
			return err
		}
		status := current.Status.Backup.Retention
		if status == nil {
			status = &api.BackupRetentionStatus{}
			current.Status.Backup.Retention = status
		}
		if reason != "" {
			status.Last = &api.BackupRetentionResult{Name: run.Name, SourceJobUID: source.UID, Key: key,
				StartedAt: run.StartedAt, FinishedAt: metav1.NewTime(now), Outcome: api.BackupRetentionSkipped,
				Reason: reason, Message: message}
			retentionCondition(status, expectedGeneration, metav1.ConditionFalse, reason, message)
			return nil
		}
		if active := current.Status.Backup.RestoreTest; active != nil && active.Active != nil {
			run.RestoreTestKey = active.Active.Key
		}
		status.Active = run.DeepCopy()
		retentionCondition(status, expectedGeneration, metav1.ConditionUnknown, "Reserved", "Retention is reserved after a successful snapshot")
		return nil
	}, adminops.ForceOwnershipOnConflict)
	if err == nil && reason != "" {
		m.emitWarningEvent(cluster, "BackupRetentionSkipped", "%s", message)
	}
	return err
}

func validateRetentionReservationAuthority(
	current *api.OpenBaoCluster,
	clusterUID types.UID,
	expectedBackup *api.BackupStatus,
	run *api.BackupRetentionRun,
	requireLock bool,
) error {
	if current.UID != clusterUID || current.Generation != run.Generation || current.DeletionTimestamp != nil ||
		current.Status.Backup == nil || current.Status.Backup.LastBackupName != run.Key ||
		!equality.Semantic.DeepEqual(current.Status.Backup.Retention, expectedBackup.Retention) ||
		!equality.Semantic.DeepEqual(current.Status.Backup.RestoreTest, expectedBackup.RestoreTest) {
		return fmt.Errorf("backup changed before retention reservation; reconcile again")
	}
	if requireLock && (!backupOperationLock.IsHeldBy(current.Status.OperationLock) ||
		current.Status.OperationLock.Message != fmt.Sprintf("backup job %s", run.SourceJobName)) {
		return fmt.Errorf("backup operation changed before retention reservation; reconcile again")
	}
	return nil
}

// retentionGuardReader pins same-name cluster reads used by the operation-lock
// CAS. Active retention blocks another backup acquisition even after a forced lock change.
var errBackupOperationChanged = errors.New("backup operation changed")

type retentionGuardReader struct {
	client.Reader
	uid         types.UID
	lockMessage string
}

func (r retentionGuardReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := r.Reader.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if cluster, ok := obj.(*api.OpenBaoCluster); ok {
		if cluster.UID != r.uid || retentionActive(cluster) {
			return fmt.Errorf("backup authority changed or retention is active")
		}
		if r.lockMessage != "" && backupOperationLock.IsHeldBy(cluster.Status.OperationLock) &&
			cluster.Status.OperationLock.Message != r.lockMessage {
			return errBackupOperationChanged
		}
	}
	return nil
}
