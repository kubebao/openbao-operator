package backup

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestTerminalSiblingReceiptRecovery(t *testing.T) {
	for _, schedule := range []struct{ name, value string }{
		{"monthly", "0 0 1 * *"}, {"frequent", "*/5 * * * *"},
	} {
		for _, older := range []bool{false, true} {
			for _, failed := range []bool{false, true} {
				for _, reverse := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/older=%t/failed=%t/reverse=%t", schedule.name, older, failed, reverse), func(t *testing.T) {
						testTerminalSiblingReceipt(t, schedule.value, older, failed, reverse)
					})
				}
			}
		}
	}
}

func testTerminalSiblingReceipt(t *testing.T, scheduleValue string, older, failed, reverse bool) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	cluster := newTestClusterWithBackup("terminal-sibling", "default")
	cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
	cluster.Status.Initialized = true
	cluster.Status.CurrentVersion = cluster.Spec.Version
	const ownedRequest = "terminal-owned-request"
	const siblingRequest = "terminal-sibling-request"
	ownedAttempt := testBackupAttempt(now.Add(-2 * time.Minute))
	ownedAttempt.ManualTrigger = ownedRequest
	siblingTime := now.Add(-time.Minute)
	if older {
		siblingTime = now.Add(-3 * time.Minute)
	}
	siblingAttempt := testBackupAttempt(siblingTime)
	siblingAttempt.ManualTrigger = siblingRequest
	owned := newBackupJobForCluster(cluster, backupJobName(cluster, ownedAttempt.ScheduledTime), ownedAttempt.Time)
	owned.Annotations["openbao.org/backup-key"] = "terminal-owned-snapshot"
	stampTestBackupReceipt(t, owned, ownedAttempt)
	sibling := newBackupJobForCluster(cluster, backupJobName(cluster, siblingAttempt.ScheduledTime), siblingAttempt.Time)
	sibling.Annotations["openbao.org/backup-key"] = "terminal-sibling-snapshot"
	stampTestBackupReceipt(t, sibling, siblingAttempt)
	cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: siblingRequest}
	cluster.Status.OperationLock = &api.OperationLockStatus{
		Operation: api.ClusterOperationBackup, Holder: backupOperationLockHolder,
		Message: backupJobLockMessage(owned.Name),
	}
	c := newTestClient(t, cluster, owned, sibling)
	reader := interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := c.List(ctx, list, opts...); err != nil {
				return err
			}
			if jobs, ok := list.(*batchv1.JobList); ok && reverse {
				for i, j := 0, len(jobs.Items)-1; i < j; i, j = i+1, j-1 {
					jobs.Items[i], jobs.Items[j] = jobs.Items[j], jobs.Items[i]
				}
			}
			return nil
		},
	})
	reconcile := func() {
		t.Helper()
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
		_, err := newBackupManager(c).WithReader(reader).Reconcile(t.Context(), logr.Discard(), cluster)
		require.NoError(t, err)
	}
	reconcile() // A is active; B's pending request must survive A's receipt.
	if failed {
		sibling.Status.Failed = 1
	} else {
		sibling.Status.Succeeded = 1
	}
	require.NoError(t, c.Status().Update(t.Context(), sibling))
	reconcile() // B finishes first, while A still owns the operation lock.
	owned.Status.Succeeded = 1
	require.NoError(t, c.Status().Update(t.Context(), owned))
	reconcile()
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.Equal(t, owned.Annotations["openbao.org/backup-key"], cluster.Status.Backup.LastBackupName)
	require.Equal(t, siblingRequest, cluster.Annotations[constants.AnnotationTriggerBackup])
	require.Nil(t, cluster.Status.OperationLock)

	// A schedule edit after A's completion must survive B's older receipt too.
	cluster.Spec.Backup.Schedule = scheduleValue
	require.NoError(t, c.Update(t.Context(), cluster))
	schedule, err := ParseSchedule(cluster.Spec.Backup.Schedule)
	require.NoError(t, err)
	deadline := ptrToTime(schedule.Next(now))
	cluster.Status.Backup.NextScheduledBackup = deadline
	require.NoError(t, c.Status().Update(t.Context(), cluster))
	reconcile()
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.Equal(t, siblingRequest, cluster.Status.Backup.LastHandledManualTrigger)
	require.Empty(t, cluster.Annotations[constants.AnnotationTriggerBackup])
	require.True(t, deadline.Equal(cluster.Status.Backup.NextScheduledBackup))
	require.Equal(t, owned.Annotations["openbao.org/backup-key"], cluster.Status.Backup.LastBackupName)
	require.EqualValues(t, 1, cluster.Status.Backup.SuccessfulBackups)
	reconcile() // Receipt recovery must be idempotent and must not create B again.
	var jobs batchv1.JobList
	require.NoError(t, c.List(t.Context(), &jobs, client.InNamespace(cluster.Namespace)))
	require.Equal(t, 2, len(jobs.Items))
}
