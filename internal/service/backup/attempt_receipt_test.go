package backup

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestBackupAttemptReceiptSurvivesControllerRestart(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "completed"}[completed], func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Second)
			cluster := newTestClusterWithBackup("receipt-restart", "backup-ns")
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "recovery-request"}
			cluster.Status.Backup.LastHandledManualTrigger = "previous-failed-request"
			job := newBackupJobForCluster(cluster, backupJobName(cluster, now), now)
			job.Annotations["openbao.org/backup-key"] = "recovery-snapshot"
			receipt, err := json.Marshal(map[string]any{
				"time": now, "scheduledTime": now, "nextScheduled": now.Add(24 * time.Hour),
				"manualTrigger": "recovery-request",
			})
			require.NoError(t, err)
			job.Annotations["kubebao.org/backup-attempt"] = string(receipt)
			if completed {
				job.Status.Succeeded = 1
			}
			cluster.Status.OperationLock = &api.OperationLockStatus{
				Operation: api.ClusterOperationBackup, Holder: backupOperationLockHolder,
				Message: backupJobLockMessage(job.Name), AcquiredAt: ptrToTime(now),
			}
			c := newTestClient(t, cluster, job)

			// The Job exists, but its creator stopped before persisting the receipt.
			manager := newBackupManager(c)
			_, err = manager.Reconcile(ctx, logr.Discard(), cluster.DeepCopy())
			require.NoError(t, err)
			var stored api.OpenBaoCluster
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), &stored))
			require.Equal(t, "recovery-request", stored.Status.Backup.LastHandledManualTrigger)
			require.NotNil(t, stored.Status.Backup.LastAttemptTime)
			require.True(t, stored.Status.Backup.LastAttemptTime.Time.Equal(now))
			require.Empty(t, stored.Annotations[constants.AnnotationTriggerBackup])
			var jobs batchv1.JobList
			require.NoError(t, c.List(ctx, &jobs, client.InNamespace(cluster.Namespace)))
			require.Len(t, jobs.Items, 1)
			require.Equal(t, job.UID, jobs.Items[0].UID)
		})
	}
}

func TestBackupAttemptReceiptRejectsInvalidOrUnrelatedEvidence(t *testing.T) {
	for _, scenario := range []string{"malformed", "wrong-schedule", "foreign-owner", "different-operation"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Second)
			cluster := newTestClusterWithBackup("receipt-evidence", "backup-ns")
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "pending"}
			cluster.Status.OperationLock = &api.OperationLockStatus{
				Operation: api.ClusterOperationBackup, Holder: backupOperationLockHolder,
				Message: backupJobLockMessage(backupJobName(cluster, now)),
			}
			job := newBackupJobForCluster(cluster, backupJobName(cluster, now), now)
			attempt := testBackupAttempt(now)
			attempt.ManualTrigger = "pending"
			if scenario == "wrong-schedule" {
				attempt.ScheduledTime = now.Add(-time.Hour)
			}
			raw, err := json.Marshal(attempt)
			require.NoError(t, err)
			job.Annotations[backupAttemptAnnotation] = string(raw)
			switch scenario {
			case "malformed":
				job.Annotations[backupAttemptAnnotation] = "invalid-json"
			case "foreign-owner":
				job.OwnerReferences[0].UID = "different-source"
				job.Annotations[constants.AnnotationOpenBaoOwnerUID] = "different-source"
			case "different-operation":
				cluster.Status.OperationLock.Message = "backup job another-job"
			}
			c := newTestClient(t, cluster, job)
			manager := newBackupManager(c)
			if scenario == "different-operation" {
				_, err = manager.recoverBackupAttempt(ctx, cluster.DeepCopy(), job)
			} else {
				_, err = manager.Reconcile(ctx, logr.Discard(), cluster.DeepCopy())
			}
			require.Error(t, err)
			var stored api.OpenBaoCluster
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), &stored))
			require.Empty(t, stored.Status.Backup.LastHandledManualTrigger)
			require.Equal(t, "pending", stored.Annotations[constants.AnnotationTriggerBackup])
			require.NotNil(t, stored.Status.OperationLock)
		})
	}
}

func TestRecordBackupAttemptRejectsStaleSourceOrOperation(t *testing.T) {
	for _, scenario := range []string{"replacement-source", "different-holder", "newer-backup"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Second)
			observed := newTestClusterWithBackup("receipt-cas", "backup-ns")
			observed.Status.OperationLock = &api.OperationLockStatus{
				Operation: api.ClusterOperationBackup, Holder: backupOperationLockHolder,
				Message: backupJobLockMessage(backupJobName(observed, now)),
			}
			stored := observed.DeepCopy()
			stored.Status.Backup.LastHandledManualTrigger = "newer-request"
			switch scenario {
			case "replacement-source":
				stored.UID = "replacement-source"
			case "different-holder":
				stored.Status.OperationLock.Operation = api.ClusterOperationRestore
				stored.Status.OperationLock.Holder = "restore-controller"
			case "newer-backup":
				stored.Status.OperationLock.Message = "backup job a-newer-backup"
			}
			c := newTestClient(t, stored)
			manager := newBackupManager(c)
			err := manager.recordBackupAttempt(ctx, observed, now, now, now.Add(time.Hour), "stale-request")
			require.Error(t, err)
			var actual api.OpenBaoCluster
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(stored), &actual))
			require.Equal(t, "newer-request", actual.Status.Backup.LastHandledManualTrigger)
		})
	}
}
