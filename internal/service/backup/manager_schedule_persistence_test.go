package backup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
)

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
