//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	"github.com/kubebao/openbao-operator/internal/service/backup"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

func TestScheduledBackupPreservesManualAcknowledgement(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-receipt", true: "legacy-job"}[legacy], func(t *testing.T) {
			cluster := newMinimalClusterObj(newTestNamespace(t), "scheduled-receipt")
			cluster.Spec.Backup = &api.BackupSchedule{
				Schedule:    "0 * * * *",
				Target:      api.BackupTarget{Endpoint: "https://minio.example", Bucket: testBackupBucket},
				JWTAuthRole: "backup", Image: "openbao-backup:dev",
			}
			require.NoError(t, k8sClient.Create(ctx, cluster))
			updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) {
				status.Initialized = true
				status.Phase = api.ClusterPhaseRunning
				status.CurrentVersion = cluster.Spec.Version
			})
			now := time.Now().UTC()
			due := now.Truncate(time.Hour).Add(-time.Hour)
			persist := adminopsstatus.NewMutator(k8sClient, k8sClient)
			require.NoError(t, persist(ctx, cluster, func(obj *api.OpenBaoCluster) error {
				obj.Status.Backup = &api.BackupStatus{
					LastHandledManualTrigger: "previous-request",
					NextScheduledBackup:      &metav1.Time{Time: due},
				}
				return nil
			}, adminops.ForceOwnershipOnConflict))
			key := client.ObjectKeyFromObject(cluster)
			require.NoError(t, k8sClient.Get(ctx, key, cluster))
			require.Equal(t, "previous-request", cluster.Status.Backup.LastHandledManualTrigger)
			owned := false
			for _, field := range cluster.ManagedFields {
				if field.Manager == constants.FieldOwnerAdminOpsStatus && field.Subresource == "status" {
					require.Contains(t, string(field.FieldsV1.Raw), `"f:lastHandledManualTrigger"`)
					owned = true
				}
			}
			require.True(t, owned, "the prior acknowledgement must be owned by the real AdminOps field manager")

			if legacy {
				name := fmt.Sprintf("backup-%s-%s", cluster.Name, due.Format("20060102-150405"))
				job, err := backup.BuildJob(cluster, backup.JobOptions{
					JobName: name, JobType: backup.JobTypeScheduled, BackupKey: "legacy-snapshot",
				})
				require.NoError(t, err)
				require.NoError(t, opslifecycle.PrepareManagedJobOwner(job, cluster, k8sScheme))
				require.NoError(t, k8sClient.Create(ctx, job))
				job.Status.Succeeded = 1
				require.NoError(t, newControllerClient(t).Status().Update(ctx, job))
			}

			_, err := newIntegrationBackupManager(t).Reconcile(ctx, logr.Discard(), cluster)
			require.NoError(t, err)
			var stored api.OpenBaoCluster
			require.NoError(t, k8sClient.Get(ctx, key, &stored))
			require.Equal(t, "previous-request", stored.Status.Backup.LastHandledManualTrigger)
			require.NotNil(t, stored.Status.Backup.LastAttemptScheduledTime)
			require.True(t, due.Equal(stored.Status.Backup.LastAttemptScheduledTime.Time))
			require.True(t, stored.Status.Backup.NextScheduledBackup.After(now))
			var jobs batchv1.JobList
			require.NoError(t, k8sClient.List(ctx, &jobs, client.InNamespace(cluster.Namespace)))
			require.Len(t, jobs.Items, 1)
		})
	}
}

func TestBackupAttemptReceiptRecoveryAfterInterruptedStatus(t *testing.T) {
	cases := []struct {
		name       string
		completed  bool
		applied    bool
		newRequest bool
		legacyJob  bool
	}{
		{name: "running-before-apply"},
		{name: "completed-before-apply", completed: true},
		{name: "running-after-apply", applied: true},
		{name: "completed-after-apply", completed: true, applied: true},
		{name: "running-with-newer-request", newRequest: true},
		{name: "legacy-running-with-newer-request", newRequest: true, legacyJob: true},
		{name: "completed-with-newer-request", completed: true, newRequest: true},
		{name: "legacy-completed-with-newer-request", completed: true, newRequest: true, legacyJob: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(newTestNamespace(t), "backup-receipt")
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "receipt-request"}
			cluster.Spec.Backup = &api.BackupSchedule{
				Schedule: "0 0 * * *",
				Target: api.BackupTarget{
					Endpoint: "https://minio.example", Bucket: testBackupBucket,
				},
				JWTAuthRole: "backup", Image: "openbao-backup:dev",
			}
			require.NoError(t, k8sClient.Create(ctx, cluster))
			updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) {
				status.Initialized = true
				status.Phase = api.ClusterPhaseRunning
				status.CurrentVersion = cluster.Spec.Version
				status.UpgradeRequests = &api.UpgradeRequestStatus{LastHandledRetry: "preserve-sibling"}
			})
			key := client.ObjectKeyFromObject(cluster)
			require.NoError(t, k8sClient.Get(ctx, key, cluster))
			interrupted := errors.New("controller stopped before attempt acknowledgement")
			persist := adminopsstatus.NewMutator(k8sClient, k8sClient)
			manager := newIntegrationBackupManager(t).WithAdminOpsStatusMutator(func(
				callCtx context.Context, target *api.OpenBaoCluster,
				mutate func(*api.OpenBaoCluster) error, ownership adminops.OwnershipPolicy,
			) error {
				probe := target.DeepCopy()
				if err := mutate(probe); err != nil {
					return err
				}
				if probe.Status.Backup != nil && probe.Status.Backup.LastHandledManualTrigger == "receipt-request" {
					if testCase.applied {
						// The API accepted the write, but the controller lost its
						// response or read-back before it could clear the request.
						if err := persist(callCtx, target.DeepCopy(), mutate, ownership); err != nil {
							return err
						}
					}
					return interrupted
				}
				return persist(callCtx, target, mutate, ownership)
			})
			_, err := manager.Reconcile(ctx, logr.Discard(), cluster)
			require.ErrorIs(t, err, interrupted)

			var stored api.OpenBaoCluster
			require.NoError(t, k8sClient.Get(ctx, key, &stored))
			require.Equal(t, "receipt-request", stored.Annotations[constants.AnnotationTriggerBackup])
			if testCase.applied {
				require.Equal(t, "receipt-request", stored.Status.Backup.LastHandledManualTrigger)
			} else {
				require.Empty(t, stored.Status.Backup.LastHandledManualTrigger)
			}
			require.NotNil(t, stored.Status.OperationLock)
			var jobs batchv1.JobList
			require.NoError(t, k8sClient.List(ctx, &jobs, client.InNamespace(cluster.Namespace)))
			require.Len(t, jobs.Items, 1)
			job := jobs.Items[0].DeepCopy()
			require.NotEmpty(t, job.UID)
			var receipt map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(job.Annotations["kubebao.org/backup-attempt"]), &receipt))
			require.JSONEq(t, `"receipt-request"`, string(receipt["manualTrigger"]))
			if testCase.completed {
				job.Status.Succeeded = 1
				require.NoError(t, newControllerClient(t).Status().Update(ctx, job))
			}
			if testCase.legacyJob {
				require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job))
				delete(job.Annotations, "kubebao.org/backup-attempt")
				require.NoError(t, k8sClient.Update(ctx, job))
			}
			if testCase.newRequest {
				require.NoError(t, k8sClient.Get(ctx, key, &stored))
				stored.Annotations[constants.AnnotationTriggerBackup] = "newer-request"
				require.NoError(t, k8sClient.Update(ctx, &stored))
			}

			// A new manager has no memory of the interrupted request.
			manager = newIntegrationBackupManager(t)
			_, err = manager.Reconcile(ctx, logr.Discard(), &stored)
			require.NoError(t, err)
			require.NoError(t, k8sClient.Get(ctx, key, &stored))
			if testCase.legacyJob {
				require.Empty(t, stored.Status.Backup.LastHandledManualTrigger)
			} else {
				require.Equal(t, "receipt-request", stored.Status.Backup.LastHandledManualTrigger)
				require.NotNil(t, stored.Status.Backup.LastAttemptTime)
			}
			if testCase.newRequest {
				require.Equal(t, "newer-request", stored.Annotations[constants.AnnotationTriggerBackup])
			} else {
				require.Empty(t, stored.Annotations[constants.AnnotationTriggerBackup])
			}
			require.Equal(t, "preserve-sibling", stored.Status.UpgradeRequests.LastHandledRetry)
			require.NoError(t, k8sClient.List(ctx, &jobs, client.InNamespace(cluster.Namespace)))
			require.Len(t, jobs.Items, 1)
			require.Equal(t, job.UID, jobs.Items[0].UID)
			if testCase.completed {
				require.Equal(t, job.Annotations["openbao.org/backup-key"], stored.Status.Backup.LastBackupName)
				require.EqualValues(t, 1, stored.Status.Backup.SuccessfulBackups)
			}
			if testCase.newRequest {
				if !testCase.completed {
					// The newer request stays queued while the original Job runs.
					job.Status.Succeeded = 1
					require.NoError(t, newControllerClient(t).Status().Update(ctx, job))
					_, err = newIntegrationBackupManager(t).Reconcile(ctx, logr.Discard(), &stored)
					require.NoError(t, err)
					require.NoError(t, k8sClient.Get(ctx, key, &stored))
					require.Equal(t, "newer-request", stored.Annotations[constants.AnnotationTriggerBackup])
				}
				// Job names use whole seconds. Give the distinct request a
				// distinct attempt time before checking that it gets a new Job.
				var attemptTime time.Time
				require.NoError(t, json.Unmarshal(receipt["time"], &attemptTime))
				require.Eventually(t, func() bool {
					return time.Now().Truncate(time.Second).After(attemptTime.Truncate(time.Second))
				}, 2*time.Second, 10*time.Millisecond)
				_, err = newIntegrationBackupManager(t).Reconcile(ctx, logr.Discard(), &stored)
				require.NoError(t, err)
				require.NoError(t, k8sClient.Get(ctx, key, &stored))
				require.Equal(t, "newer-request", stored.Status.Backup.LastHandledManualTrigger)
				require.Empty(t, stored.Annotations[constants.AnnotationTriggerBackup])
				require.NoError(t, k8sClient.List(ctx, &jobs, client.InNamespace(cluster.Namespace)))
				require.Len(t, jobs.Items, 2)
			}
		})
	}
}

// Model an API server that committed Create before its response was lost.
type lostBackupCreateResponseClient struct {
	client.Client
	jobCreates int
}

func (c *lostBackupCreateResponseClient) Create(callCtx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*batchv1.Job); !ok {
		return c.Client.Create(callCtx, obj, opts...)
	}
	c.jobCreates++
	if err := c.Client.Create(callCtx, obj, opts...); err != nil {
		return err
	}
	return apierrors.NewTimeoutError("backup Job Create response lost", 0)
}

func TestBackupRecoversCommittedJobAfterLostCreateResponse(t *testing.T) {
	cluster := newMinimalClusterObj(newTestNamespace(t), "lost-create-response")
	cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "lost-response-request"}
	cluster.Spec.Backup = &api.BackupSchedule{
		Schedule: "0 0 * * *", JWTAuthRole: "backup", Image: "openbao-backup:dev",
		Target: api.BackupTarget{Endpoint: "https://minio.example", Bucket: testBackupBucket},
	}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) {
		status.Initialized = true
		status.Phase = api.ClusterPhaseRunning
		status.CurrentVersion = cluster.Spec.Version
	})
	fault := &lostBackupCreateResponseClient{Client: newControllerClient(t)}
	_, err := newIntegrationBackupManagerWithClient(t, fault).Reconcile(ctx, logr.Discard(), cluster)
	require.ErrorContains(t, err, "response lost")
	key := client.ObjectKeyFromObject(cluster)
	require.NoError(t, k8sClient.Get(ctx, key, cluster))
	require.NotNil(t, cluster.Status.OperationLock)
	require.Equal(t, "lost-response-request", cluster.Annotations[constants.AnnotationTriggerBackup])
	var jobs batchv1.JobList
	require.NoError(t, k8sClient.List(ctx, &jobs, client.InNamespace(cluster.Namespace)))
	require.Len(t, jobs.Items, 1)
	jobUID := jobs.Items[0].UID

	_, err = newIntegrationBackupManager(t).Reconcile(ctx, logr.Discard(), cluster)
	require.NoError(t, err)
	require.NoError(t, k8sClient.Get(ctx, key, cluster))
	require.Equal(t, "lost-response-request", cluster.Status.Backup.LastHandledManualTrigger)
	require.Empty(t, cluster.Annotations[constants.AnnotationTriggerBackup])
	require.NotNil(t, cluster.Status.OperationLock)
	require.NoError(t, k8sClient.List(ctx, &jobs, client.InNamespace(cluster.Namespace)))
	require.Len(t, jobs.Items, 1)
	require.Equal(t, jobUID, jobs.Items[0].UID)
	require.Equal(t, 1, fault.jobCreates)
}
