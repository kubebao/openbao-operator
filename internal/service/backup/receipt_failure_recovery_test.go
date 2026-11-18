package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
)

func TestInvalidBackupScheduleStillObservesBoundRestoreTest(t *testing.T) {
	cluster := newTestClusterWithBackup("invalid-schedule-active-test", "default")
	cluster.Spec.Backup.Schedule = "0 0 31 2 *"
	run := &api.RestoreTestRun{Name: "missing-bound-child", Namespace: "recovery", UID: "bound-child-uid"}
	c := newTestClient(t, cluster)
	manager := newBackupManager(c)
	require.NoError(t, manager.adminOpsMutator(t.Context(), cluster, func(current *api.OpenBaoCluster) error {
		current.Status.Backup.RestoreTest = &api.RestoreTestStatus{Active: run}
		return nil
	}, adminops.ForceOwnership))
	_, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.ErrorContains(t, err, "no future occurrence")
	var stored api.OpenBaoCluster
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
	require.Nil(t, stored.Status.Backup.RestoreTest.Active)
	require.NotNil(t, stored.Status.Backup.RestoreTest.Last)
	require.Equal(t, api.RestoreTestFailed, stored.Status.Backup.RestoreTest.Last.Outcome)
}

func TestManualBackupTriggerByteBudget(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		allowed bool
	}{
		{name: "maximum ASCII", token: strings.Repeat("x", 1024), allowed: true},
		{name: "maximum JSON escaping", token: strings.Repeat(`"`, 1024), allowed: true},
		{name: "Unicode within byte budget", token: strings.Repeat("界", 341), allowed: true},
		{name: "over ASCII budget", token: strings.Repeat("x", 1025)},
		{name: "over Unicode byte budget", token: strings.Repeat("界", 342)},
		{name: "fits Cluster annotation but not receipt", token: strings.Repeat("x", 262100)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newTestClusterWithBackup("trigger-budget", "default")
			cluster.Spec.Backup.Schedule = "0 * * * *"
			cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
			cluster.Status.Initialized = true
			cluster.Status.CurrentVersion = cluster.Spec.Version
			cluster.Status.Backup.LastHandledManualTrigger = "acknowledged-earlier"
			due := metav1.NewTime(time.Now().UTC().Truncate(time.Hour).Add(-time.Hour))
			cluster.Status.Backup.NextScheduledBackup = &due
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: tc.token}
			c := newTestClient(t, cluster)
			_, err := newBackupManager(c).Reconcile(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			var stored api.OpenBaoCluster
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			require.Empty(t, stored.Annotations[constants.AnnotationTriggerBackup])
			var jobs batchv1.JobList
			require.NoError(t, c.List(t.Context(), &jobs, client.InNamespace(cluster.Namespace)))
			if tc.allowed {
				require.Equal(t, tc.token, stored.Status.Backup.LastHandledManualTrigger)
				require.Equal(t, 1, len(jobs.Items))
				return
			}
			require.Equal(t, len("acknowledged-earlier"), len(stored.Status.Backup.LastHandledManualTrigger))
			require.Empty(t, jobs.Items)
			_, err = newBackupManager(c).Reconcile(t.Context(), logr.Discard(), &stored)
			require.NoError(t, err)
			require.NoError(t, c.List(t.Context(), &jobs, client.InNamespace(cluster.Namespace)))
			require.Equal(t, 1, len(jobs.Items), "a rejected request must not block the due scheduled backup")
		})
	}
}

func TestBackupUncertainCreateKeepsLockAndRequest(t *testing.T) {
	for _, outcome := range []string{"committed", "not-committed", "forbidden"} {
		t.Run(outcome, func(t *testing.T) {
			cluster := newTestClusterWithBackup("uncertain-create", "default")
			cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
			cluster.Status.Initialized = true
			cluster.Status.CurrentVersion = cluster.Spec.Version
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "pending-request"}
			base := newTestClient(t, cluster)
			creates := 0
			c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*batchv1.Job); !ok {
						return c.Create(ctx, obj, opts...)
					}
					creates++
					if outcome == "forbidden" {
						return apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, obj.GetName(), nil)
					}
					if outcome == "committed" {
						obj.SetUID(types.UID("committed-job-uid"))
						if err := c.Create(ctx, obj, opts...); err != nil {
							return err
						}
					}
					return apierrors.NewTimeoutError("create response lost", 1)
				},
			})
			_, err := newBackupManager(c).Reconcile(t.Context(), logr.Discard(), cluster)
			require.Error(t, err)
			var stored api.OpenBaoCluster
			require.NoError(t, base.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			require.Equal(t, "pending-request", stored.Annotations[constants.AnnotationTriggerBackup])
			require.Empty(t, stored.Status.Backup.LastHandledManualTrigger)
			if outcome == "forbidden" {
				require.Nil(t, stored.Status.OperationLock)
				return
			}
			require.NotNil(t, stored.Status.OperationLock)
			_, err = newBackupManager(c).Reconcile(t.Context(), logr.Discard(), &stored)
			if outcome == "committed" {
				require.NoError(t, err)
				require.NoError(t, base.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
				require.Equal(t, "pending-request", stored.Status.Backup.LastHandledManualTrigger)
				require.Empty(t, stored.Annotations[constants.AnnotationTriggerBackup])
			} else {
				require.Error(t, err)
				require.NoError(t, base.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
				require.NotNil(t, stored.Status.OperationLock)
				require.Equal(t, "pending-request", stored.Annotations[constants.AnnotationTriggerBackup])
			}
			require.Equal(t, 1, creates, "an uncertain submission must not authorize another Create")
		})
	}
}

func TestActiveBackupRecoveryUsesLockedJobRegardlessOfListOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal-order", true: "reverse-order"}[reverse], func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			cluster := newTestClusterWithBackup("active-order", "default")
			ownedAttempt := testBackupAttempt(now.Add(-2 * time.Minute))
			ownedAttempt.ManualTrigger = "owned-request"
			owned := newBackupJobForCluster(cluster, backupJobName(cluster, ownedAttempt.ScheduledTime), ownedAttempt.Time)
			owned.Annotations["openbao.org/backup-key"] = "owned-snapshot"
			stampTestBackupReceipt(t, owned, ownedAttempt)
			otherAttempt := testBackupAttempt(now.Add(-time.Minute))
			otherAttempt.ManualTrigger = "other-request"
			other := newBackupJobForCluster(cluster, backupJobName(cluster, otherAttempt.ScheduledTime), otherAttempt.Time)
			stampTestBackupReceipt(t, other, otherAttempt)
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "owned-request"}
			cluster.Status.OperationLock = &api.OperationLockStatus{
				Operation: api.ClusterOperationBackup, Holder: backupOperationLockHolder,
				Message: backupJobLockMessage(owned.Name),
			}
			c := newTestClient(t, cluster, owned, other)
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
			manager := newBackupManager(c).WithReader(reader)
			_, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			var stored api.OpenBaoCluster
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			require.Equal(t, "owned-request", stored.Status.Backup.LastHandledManualTrigger)
			require.Empty(t, stored.Annotations[constants.AnnotationTriggerBackup])

			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(owned), owned))
			owned.Status.Succeeded = 1
			require.NoError(t, c.Status().Update(t.Context(), owned))
			_, err = manager.Reconcile(t.Context(), logr.Discard(), &stored)
			require.NoError(t, err)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			require.Nil(t, stored.Status.OperationLock, "another active Job must not prevent the named Job from finishing")
			stored.Annotations = map[string]string{constants.AnnotationTriggerBackup: "other-request"}
			require.NoError(t, c.Update(t.Context(), &stored))
			_, err = newBackupManager(c).WithReader(reader).Reconcile(t.Context(), logr.Discard(), &stored)
			require.NoError(t, err)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			require.Equal(t, "other-request", stored.Status.Backup.LastHandledManualTrigger)
			var jobs batchv1.JobList
			require.NoError(t, c.List(t.Context(), &jobs, client.InNamespace(cluster.Namespace)))
			require.Equal(t, 2, len(jobs.Items))
		})
	}
}

func TestPendingSuccessfulBackupPrecedesNewManualRequest(t *testing.T) {
	for _, newerFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "only-success", true: "newer-failure"}[newerFailure], func(t *testing.T) {
			cluster, source := newRetentionSource(t)
			cluster.Status.Initialized = true
			cluster.Status.CurrentVersion = cluster.Spec.Version
			cluster.Status.Backup.LastBackupName = ""
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "next-request"}
			cluster.Status.OperationLock.Message = backupJobLockMessage(backupJobName(cluster, time.Now().UTC()))
			source.CreationTimestamp = *ptrToTime(time.Now().UTC().Add(-time.Minute))
			var c client.Client
			if newerFailure {
				failed := newBackupJobForCluster(cluster, "newer-failed-job", source.CreationTimestamp.Add(30*time.Second))
				failed.Status.Failed = 1
				c = newTestClient(t, cluster, source, failed)
			} else {
				c = newTestClient(t, cluster, source)
			}
			for i := 0; i < 2; i++ {
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
				_, err := newBackupManager(c).Reconcile(t.Context(), logr.Discard(), cluster)
				require.NoError(t, err)
			}
			var stored api.OpenBaoCluster
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			require.Equal(t, source.Annotations["openbao.org/backup-key"], stored.Status.Backup.LastBackupName)
			require.Equal(t, "next-request", stored.Annotations[constants.AnnotationTriggerBackup])
			require.NotNil(t, stored.Status.Backup.Retention)
			require.NotNil(t, stored.Status.Backup.Retention.Active)
			require.Equal(t, source.UID, stored.Status.Backup.Retention.Active.SourceJobUID)
			var jobs batchv1.JobList
			require.NoError(t, c.List(t.Context(), &jobs, client.MatchingLabels{
				constants.LabelOpenBaoBackupType: constants.BackupTypeScheduled,
			}))
			expectedJobs := 1
			if newerFailure {
				expectedJobs++
			}
			require.Equal(t, expectedJobs, len(jobs.Items), "record the successful snapshot and reserve retention before another backup")
		})
	}
}

func TestOlderRecoveredReceiptPreservesNewerAttempt(t *testing.T) {
	for _, pending := range []string{"newer-request", "older-request"} {
		t.Run(pending, func(t *testing.T) {
			cluster := newTestClusterWithBackup("older-receipt", "default")
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: pending}
			older := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
			newer := older.Add(30 * time.Minute)
			newerNext := newer.Add(time.Hour)
			cluster.Status.OperationLock = &api.OperationLockStatus{
				Operation: api.ClusterOperationBackup, Holder: backupOperationLockHolder,
				Message: backupJobLockMessage(backupJobName(cluster, older)),
			}
			cluster.Status.Backup.LastAttemptTime = &metav1.Time{Time: newer}
			cluster.Status.Backup.LastAttemptScheduledTime = &metav1.Time{Time: newer}
			cluster.Status.Backup.NextScheduledBackup = &metav1.Time{Time: newerNext}
			cluster.Status.Backup.LastHandledManualTrigger = "newer-request"
			c := newTestClient(t, cluster)
			require.NoError(t, newBackupManager(c).recordBackupAttempt(t.Context(), cluster,
				older, older, older.Add(time.Hour), "older-request"))
			var stored api.OpenBaoCluster
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), &stored))
			require.True(t, newer.Equal(stored.Status.Backup.LastAttemptTime.Time))
			require.True(t, newer.Equal(stored.Status.Backup.LastAttemptScheduledTime.Time))
			require.True(t, newerNext.Equal(stored.Status.Backup.NextScheduledBackup.Time))
			require.Equal(t, pending, stored.Status.Backup.LastHandledManualTrigger)
		})
	}
}
