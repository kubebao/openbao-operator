package backup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
)

const testEveryMinuteBackupSchedule = "* * * * *"

func TestApplyBackupDecision_PersistsInitialSchedule(t *testing.T) {
	tests := []struct {
		name    string
		kind    backupDecisionKind
		blocker backupBlockerKind
	}{
		{name: "idle", kind: backupDecisionIdle},
		{name: "restore blocked", kind: backupDecisionBlocked, blocker: backupBlockedByRestore},
		{name: "precondition blocked", kind: backupDecisionBlocked, blocker: backupBlockedByPrecondition},
		{name: "active job", kind: backupDecisionObserve},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cluster := newTestClusterWithBackup("schedule", "default")
			cluster.Spec.Backup.Schedule = testEveryMinuteBackupSchedule
			cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
			k8sClient := newTestClient(t, cluster)
			manager := newBackupManager(k8sClient)
			now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
			next := now.Add(time.Minute)
			decision := backupDecision{
				kind: test.kind,
				observation: backupObservation{
					configured:          true,
					initialNextSchedule: next,
					scheduledTime:       next,
					now:                 now,
					blocker:             test.blocker,
					precondition:        &backupPreconditionError{message: "cluster is not initialized"},
				},
			}

			_, err := manager.applyBackupDecision(context.Background(), logr.Discard(), cluster,
				NewMetrics(cluster.Namespace, cluster.Name), decision)
			require.NoError(t, err)

			var stored openbaov1alpha1.OpenBaoCluster
			require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), &stored))
			require.NotNil(t, stored.Status.Backup.NextScheduledBackup)
			require.True(t, stored.Status.Backup.NextScheduledBackup.Time.Equal(next))

			// A later reconcile must use the persisted deadline, even after it becomes due.
			stored.Status.Initialized = true
			stored.Status.Phase = openbaov1alpha1.ClusterPhaseRunning
			stored.Status.CurrentVersion = stored.Spec.Version
			observation, err := manager.observeBackup(context.Background(), logr.Discard(), &stored, next)
			require.NoError(t, err)
			require.True(t, observation.due)
			require.True(t, observation.scheduledTime.Equal(next))
			require.Equal(t, backupDecisionCreate, decideBackup(observation).kind)
		})
	}
}

func TestApplyBackupDecision_RefreshesScheduleWithoutReplacingHistory(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 15, 0, 0, time.UTC)
	for _, schedule := range []string{"0 * * * *", "0 18 * * *"} {
		t.Run(schedule, func(t *testing.T) {
			cluster := newTestClusterWithBackup("refresh-schedule", "default")
			cluster.Spec.Backup.Schedule = schedule
			cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
			cluster.Status.Backup.NextScheduledBackup = ptrToTime(now.Add(3 * time.Hour))
			cluster.Status.Backup.LastBackupName = "old-observation.snap"
			previous := cluster.DeepCopy()
			previous.Status.Backup.LastBackupName = "live-history.snap"
			previous.Status.Backup.RestoreTest = &openbaov1alpha1.RestoreTestStatus{
				LastSuccessTime: ptrToTime(now.Add(-time.Hour)),
			}
			previous.Status.Backup.Retention = &openbaov1alpha1.BackupRetentionStatus{
				Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Preserved"}},
			}
			previous.Status.UpgradeRequests = &openbaov1alpha1.UpgradeRequestStatus{LastHandledRetry: "preserved"}
			k8sClient := newTestClient(t, previous)
			manager := newBackupManager(k8sClient)
			observation, err := manager.observeBackup(context.Background(), logr.Discard(), cluster, now)
			require.NoError(t, err)
			_, err = manager.applyBackupDecision(context.Background(), logr.Discard(), cluster,
				NewMetrics(cluster.Namespace, cluster.Name), decideBackup(observation))
			require.NoError(t, err)
			var stored openbaov1alpha1.OpenBaoCluster
			require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), &stored))
			parsed, err := ParseSchedule(schedule)
			require.NoError(t, err)
			require.True(t, stored.Status.Backup.NextScheduledBackup.Time.Equal(parsed.Next(now)))
			previous.Status.Backup.NextScheduledBackup = stored.Status.Backup.NextScheduledBackup
			require.True(t, apiequality.Semantic.DeepEqual(previous.Status.Backup, stored.Status.Backup))
			require.Equal(t, previous.Status.UpgradeRequests, stored.Status.UpgradeRequests)
		})
	}
}

func TestApplyBackupDecision_ConcurrentScheduleChangePreventsStaleCreate(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 15, 0, 0, time.UTC)
	for _, change := range []string{"cron", "deadline", "backup removed", "cluster replaced"} {
		t.Run(change, func(t *testing.T) {
			observed := newTestClusterWithBackup("concurrent-schedule", "default")
			observed.Spec.Backup.Schedule = testEveryMinuteBackupSchedule
			observed.Status.Backup.NextScheduledBackup = ptrToTime(now.Add(-time.Minute))
			stored := observed.DeepCopy()
			stored.Status.Backup.LastBackupName = "concurrent.snap"
			stored.Status.Backup.RestoreTest = &openbaov1alpha1.RestoreTestStatus{LastSuccessTime: ptrToTime(now)}
			stored.Status.Backup.Retention = &openbaov1alpha1.BackupRetentionStatus{
				Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Preserved"}},
			}
			switch change {
			case "cron":
				stored.Spec.Backup.Schedule = "0 18 * * *"
			case "deadline":
				stored.Status.Backup.NextScheduledBackup = ptrToTime(now.Add(time.Hour))
			case "backup removed":
				stored.Spec.Backup = nil
			case "cluster replaced":
				stored.UID = "replacement-uid"
			}
			k8sClient := newTestClient(t, stored)
			manager := newBackupManager(k8sClient)
			result, err := manager.applyBackupDecision(context.Background(), logr.Discard(), observed,
				NewMetrics(observed.Namespace, observed.Name), decideBackup(backupObservation{
					configured: true, due: true, now: now,
					initialNextSchedule: observed.Status.Backup.NextScheduledBackup.Time,
					scheduledTime:       observed.Status.Backup.NextScheduledBackup.Time,
					nextSchedule:        now.Add(time.Minute),
				}))
			require.NoError(t, err)
			require.Equal(t, constants.RequeueShort, result.RequeueAfter)
			var after openbaov1alpha1.OpenBaoCluster
			require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(stored), &after))
			require.Equal(t, stored.UID, after.UID)
			require.True(t, apiequality.Semantic.DeepEqual(stored.Status.Backup, after.Status.Backup))
			require.Nil(t, after.Status.OperationLock)
			var jobs batchv1.JobList
			require.NoError(t, k8sClient.List(context.Background(), &jobs, client.InNamespace(stored.Namespace)))
			require.Empty(t, jobs.Items, "a stale decision must not create a backup Job")
		})
	}
}

func TestApplyBackupDecision_NewerReadBackDeadlinePreventsStaleCreate(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 15, 0, 0, time.UTC)
	cluster := newTestClusterWithBackup("readback-schedule", "default")
	cluster.Spec.Backup.Schedule = testEveryMinuteBackupSchedule
	cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "request-1"}
	cluster.Status.Backup.NextScheduledBackup = ptrToTime(now.Add(time.Hour))
	next := now.Add(time.Minute)
	newer := now.Add(2 * time.Hour)
	k8sClient := newTestClient(t, cluster)
	manager := newBackupManager(k8sClient)
	mutator := manager.adminOpsMutator
	manager.WithAdminOpsStatusMutator(func(ctx context.Context, obj *openbaov1alpha1.OpenBaoCluster,
		mutate func(*openbaov1alpha1.OpenBaoCluster) error, ownership adminops.OwnershipPolicy,
	) error {
		// Apply the callback successfully, then model a concurrent write that
		// becomes visible in the mutator's final read-back.
		if err := mutator(ctx, obj, mutate, ownership); err != nil {
			return err
		}
		require.True(t, obj.Status.Backup.NextScheduledBackup.Time.Equal(next))
		live := &openbaov1alpha1.OpenBaoCluster{}
		key := client.ObjectKeyFromObject(obj)
		if err := k8sClient.Get(ctx, key, live); err != nil {
			return err
		}
		live.Status.Backup.NextScheduledBackup = ptrToTime(newer)
		live.Status.Backup.LastBackupName = "newer-history.snap"
		live.Status.Backup.RestoreTest = &openbaov1alpha1.RestoreTestStatus{LastSuccessTime: ptrToTime(now)}
		if err := k8sClient.Status().Update(ctx, live); err != nil {
			return err
		}
		return k8sClient.Get(ctx, key, obj)
	})
	decision := decideBackup(backupObservation{
		configured: true, due: true, now: now, manualTriggerToken: "request-1",
		initialNextSchedule: next, scheduledTime: now, nextSchedule: next,
	})
	require.Equal(t, backupDecisionCreate, decision.kind)
	result, err := manager.applyBackupDecision(context.Background(), logr.Discard(), cluster,
		NewMetrics(cluster.Namespace, cluster.Name), decision)
	require.NoError(t, err)
	require.Equal(t, constants.RequeueShort, result.RequeueAfter)
	var stored openbaov1alpha1.OpenBaoCluster
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), &stored))
	require.True(t, stored.Status.Backup.NextScheduledBackup.Time.Equal(newer))
	require.Equal(t, "newer-history.snap", stored.Status.Backup.LastBackupName)
	require.NotNil(t, stored.Status.Backup.RestoreTest.LastSuccessTime)
	require.Equal(t, stored.Status.Backup, cluster.Status.Backup)
	require.Equal(t, "request-1", stored.Annotations[constants.AnnotationTriggerBackup])
	require.Nil(t, stored.Status.OperationLock)
	var jobs batchv1.JobList
	require.NoError(t, k8sClient.List(context.Background(), &jobs, client.InNamespace(cluster.Namespace)))
	require.Empty(t, jobs.Items, "newer read-back status must prevent a stale backup Job")
}

func TestApplyBackupDecision_InitialSchedulePreservesLiveStatus(t *testing.T) {
	cluster := newTestClusterWithBackup("live-schedule", "default")
	stored := cluster.DeepCopy()
	next := time.Date(2026, 10, 13, 11, 0, 0, 0, time.UTC)
	stored.Status.Backup.NextScheduledBackup = ptrToTime(next)
	stored.Status.Backup.LastBackupName = "existing.snap"
	stored.Status.UpgradeRequests = &openbaov1alpha1.UpgradeRequestStatus{LastHandledRetry: "keep-me"}
	k8sClient := newTestClient(t, stored)
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(stored), stored))
	manager := newBackupManager(k8sClient)

	_, err := manager.applyBackupDecision(context.Background(), logr.Discard(), cluster,
		NewMetrics(cluster.Namespace, cluster.Name), backupDecision{
			kind: backupDecisionIdle,
			observation: backupObservation{
				configured:          true,
				initialNextSchedule: next.Add(time.Hour),
				scheduledTime:       next.Add(time.Hour),
				now:                 next.Add(-time.Hour),
			},
		})
	require.NoError(t, err)

	var after openbaov1alpha1.OpenBaoCluster
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), &after))
	require.Equal(t, stored.Status.Backup, after.Status.Backup)
	require.Equal(t, stored.Status.UpgradeRequests, after.Status.UpgradeRequests)
}

func TestApplyBackupDecision_InitialScheduleWriteFailure(t *testing.T) {
	cluster := newTestClusterWithBackup("schedule-error", "default")
	writeErr := errors.New("status write failed")
	manager := newBackupManager(newTestClient(t, cluster)).WithAdminOpsStatusMutator(
		func(context.Context, *openbaov1alpha1.OpenBaoCluster,
			func(*openbaov1alpha1.OpenBaoCluster) error, adminops.OwnershipPolicy) error {
			return writeErr
		})
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	result, err := manager.applyBackupDecision(context.Background(), logr.Discard(), cluster,
		NewMetrics(cluster.Namespace, cluster.Name), backupDecision{
			kind: backupDecisionIdle,
			observation: backupObservation{
				configured:          true,
				initialNextSchedule: now.Add(time.Minute),
				scheduledTime:       now.Add(time.Minute),
				now:                 now,
			},
		})
	require.ErrorIs(t, err, writeErr)
	require.Zero(t, result.RequeueAfter)
	require.Nil(t, cluster.Status.Backup.NextScheduledBackup)
}
