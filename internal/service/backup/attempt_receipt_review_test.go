package backup

import (
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

func TestBackupRejectsScheduleWithoutFutureOccurrence(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "scheduled", true: "manual"}[manual], func(t *testing.T) {
			cluster := newTestClusterWithBackup("impossible-schedule", "default")
			cluster.Spec.Backup.Schedule = "0 0 31 2 *"
			cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
			cluster.Status.Initialized = true
			cluster.Status.CurrentVersion = cluster.Spec.Version
			if manual {
				cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "pending"}
			}
			c := newTestClient(t, cluster)
			_, err := newBackupManager(c).Reconcile(t.Context(), logr.Discard(), cluster)
			require.ErrorContains(t, err, "no future occurrence")
			var stored api.OpenBaoCluster
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			require.Nil(t, stored.Status.OperationLock)
			var jobs batchv1.JobList
			require.NoError(t, c.List(t.Context(), &jobs, client.InNamespace(cluster.Namespace)))
			require.Empty(t, jobs.Items)
			if manual {
				require.Equal(t, "pending", stored.Annotations[constants.AnnotationTriggerBackup])
			}
		})
	}
}

func TestTerminalBackupPreservesUnacknowledgedRequest(t *testing.T) {
	for _, kind := range []string{"manual-receipt", "scheduled-receipt", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			cluster := newTestClusterWithBackup("terminal-request", "default")
			cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
			cluster.Status.Initialized = true
			cluster.Status.CurrentVersion = cluster.Spec.Version
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "request-B"}
			attempt := testBackupAttempt(now.Add(-time.Minute))
			if kind == "manual-receipt" {
				attempt.ManualTrigger = "request-A"
			}
			job := newBackupJobForCluster(cluster, backupJobName(cluster, attempt.ScheduledTime), attempt.Time)
			job.Status.Succeeded = 1
			job.Annotations["openbao.org/backup-key"] = "snapshot-A"
			if kind != "legacy" {
				stampTestBackupReceipt(t, job, attempt)
			}
			cluster.Status.OperationLock = &api.OperationLockStatus{
				Operation: api.ClusterOperationBackup, Holder: backupOperationLockHolder,
				Message: backupJobLockMessage(job.Name),
			}
			c := newTestClient(t, cluster, job)
			_, err := newBackupManager(c).Reconcile(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			var stored api.OpenBaoCluster
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			require.Equal(t, "request-B", stored.Annotations[constants.AnnotationTriggerBackup])
			require.Equal(t, attempt.ManualTrigger, stored.Status.Backup.LastHandledManualTrigger)
			require.Nil(t, stored.Status.OperationLock)

			// B remains actionable after A finishes; it is not attributed to A.
			_, err = newBackupManager(c).Reconcile(t.Context(), logr.Discard(), &stored)
			require.NoError(t, err)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			require.Equal(t, "request-B", stored.Status.Backup.LastHandledManualTrigger)
			var jobs batchv1.JobList
			require.NoError(t, c.List(t.Context(), &jobs, client.InNamespace(cluster.Namespace)))
			require.Len(t, jobs.Items, 2)
		})
	}
}

func TestBackupFinalizationUsesLockedJob(t *testing.T) {
	for _, scenario := range []string{"missing", "missing-with-legacy-job", "older-than-unrelated-job"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			cluster := newTestClusterWithBackup("locked-job", "default")
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "locked-request"}
			cluster.Status.Backup.LastHandledManualTrigger = "previous-request"
			locked := testBackupAttempt(now.Add(-2 * time.Minute))
			locked.ManualTrigger = "locked-request"
			lockedName := backupJobName(cluster, locked.ScheduledTime)
			cluster.Status.OperationLock = &api.OperationLockStatus{
				Operation: api.ClusterOperationBackup, Holder: backupOperationLockHolder,
				Message: backupJobLockMessage(lockedName),
			}
			other := testBackupAttempt(now.Add(-time.Minute))
			other.ManualTrigger = "unrelated-request"
			job := newBackupJobForCluster(cluster, backupJobName(cluster, other.ScheduledTime), other.Time)
			job.Status.Succeeded = 1
			job.Annotations["openbao.org/backup-key"] = "unrelated-snapshot"
			if scenario != "missing-with-legacy-job" {
				stampTestBackupReceipt(t, job, other)
			}
			objects := []client.Object{cluster, job}
			if scenario == "older-than-unrelated-job" {
				owned := newBackupJobForCluster(cluster, lockedName, locked.Time)
				owned.Status.Succeeded = 1
				owned.Annotations["openbao.org/backup-key"] = "locked-snapshot"
				stampTestBackupReceipt(t, owned, locked)
				objects = append(objects, owned)
			}
			c := newTestClient(t, objects...)
			_, err := newBackupManager(c).Reconcile(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			var stored api.OpenBaoCluster
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			require.Nil(t, stored.Status.OperationLock)
			if scenario == "older-than-unrelated-job" {
				require.Equal(t, "locked-request", stored.Status.Backup.LastHandledManualTrigger)
				require.Equal(t, "locked-snapshot", stored.Status.Backup.LastBackupName)
				require.Empty(t, stored.Annotations[constants.AnnotationTriggerBackup])
			} else {
				require.Equal(t, "previous-request", stored.Status.Backup.LastHandledManualTrigger)
				require.Empty(t, stored.Status.Backup.LastBackupName)
				require.Equal(t, "locked-request", stored.Annotations[constants.AnnotationTriggerBackup])
			}
		})
	}
}

func TestLegacyBackupJobAdvancesOnlyScheduledAttempt(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "scheduled", true: "manual-collision"}[manual], func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			cluster := newTestClusterWithBackup("legacy-attempt", "default")
			cluster.Status.Backup.LastHandledManualTrigger = "previous-request"
			observation := backupObservation{
				configured: true, due: true, now: now,
				scheduledTime: now.Add(-time.Hour), nextSchedule: now.Add(time.Hour),
			}
			if manual {
				observation.manualTriggerToken = "new-request"
				cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "new-request"}
			}
			cluster.Status.Backup.NextScheduledBackup = ptrToTime(observation.scheduledTime)
			job := newBackupJobForCluster(cluster, backupJobName(cluster, observation.scheduledTime), observation.scheduledTime)
			job.Status.Succeeded = 1
			c := newTestClient(t, cluster, job)
			manager := newBackupManager(c)
			_, err := manager.applyBackupDecision(t.Context(), logr.Discard(), cluster,
				NewMetrics(cluster.Namespace, cluster.Name), decideBackup(observation))
			require.NoError(t, err)
			var stored api.OpenBaoCluster
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			if manual {
				require.Equal(t, "previous-request", stored.Status.Backup.LastHandledManualTrigger)
				require.Equal(t, "new-request", stored.Annotations[constants.AnnotationTriggerBackup])
			} else {
				require.True(t, observation.nextSchedule.Equal(stored.Status.Backup.NextScheduledBackup.Time))
				require.True(t, observation.scheduledTime.Equal(stored.Status.Backup.LastAttemptScheduledTime.Time))
				require.Equal(t, "previous-request", stored.Status.Backup.LastHandledManualTrigger)
			}
			var jobs batchv1.JobList
			require.NoError(t, c.List(t.Context(), &jobs, client.InNamespace(cluster.Namespace)))
			require.Len(t, jobs.Items, 1)
			require.Equal(t, job.UID, jobs.Items[0].UID)
		})
	}
}

func stampTestBackupReceipt(t *testing.T, job *batchv1.Job, receipt backupAttemptReceipt) {
	t.Helper()
	raw, err := json.Marshal(receipt)
	require.NoError(t, err)
	job.Annotations[backupAttemptAnnotation] = string(raw)
}
