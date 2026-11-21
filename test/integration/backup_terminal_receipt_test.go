//go:build integration

package integration

import (
	"context"
	"encoding/json"
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
	"github.com/kubebao/openbao-operator/internal/service/backup"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

func TestTerminalSiblingReceiptRecoveryAfterNewerBackup(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful-sibling", true: "failed-sibling"}[failed], func(t *testing.T) {
			cluster := newMinimalClusterObj(newTestNamespace(t), "terminal-receipt")
			const siblingRequest = "pending-terminal-request"
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: siblingRequest}
			cluster.Spec.Backup = &api.BackupSchedule{
				Schedule: "0 * * * *", JWTAuthRole: "backup", Image: "openbao-backup:dev",
				Target: api.BackupTarget{Endpoint: "https://minio.example", Bucket: testBackupBucket},
			}
			require.NoError(t, k8sClient.Create(ctx, cluster))
			updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) {
				status.Initialized = true
				status.Phase = api.ClusterPhaseRunning
				status.CurrentVersion = cluster.Spec.Version
			})
			now := time.Now().UTC().Truncate(time.Second)
			controller := newControllerClient(t)
			makeJob := func(at time.Time, request, key string) *batchv1.Job {
				t.Helper()
				job, err := backup.BuildJob(cluster, backup.JobOptions{
					JobName: fmt.Sprintf("backup-%s-%s", cluster.Name, at.Format("20060102-150405")),
					JobType: backup.JobTypeScheduled, BackupKey: key,
				})
				require.NoError(t, err)
				require.NoError(t, opslifecycle.PrepareManagedJobOwner(job, cluster, k8sScheme))
				receipt, err := json.Marshal(struct {
					Time          time.Time `json:"time"`
					ScheduledTime time.Time `json:"scheduledTime"`
					NextScheduled time.Time `json:"nextScheduled"`
					ManualTrigger string    `json:"manualTrigger"`
				}{at, at, at.Add(time.Hour), request})
				require.NoError(t, err)
				job.Annotations["kubebao.org/backup-attempt"] = string(receipt)
				require.NoError(t, controller.Create(ctx, job))
				return job
			}
			sibling := makeJob(now.Add(-3*time.Minute), siblingRequest, "sibling-terminal-key")
			owned := makeJob(now.Add(-2*time.Minute), "owned-terminal-request", "owned-terminal-key")
			require.NoError(t, opslifecycle.AcquireWithReader(ctx, k8sClient, controller, cluster,
				opslifecycle.OperationLock{Holder: constants.ControllerNameOpenBaoCluster + "/backup", Operation: api.ClusterOperationBackup},
				opslifecycle.AcquireOptions{Message: "backup job " + owned.Name}))
			_, err := newIntegrationBackupManager(t).Reconcile(ctx, logr.Discard(), cluster)
			require.NoError(t, err)
			if failed {
				sibling.Status.Failed = 1
			} else {
				sibling.Status.Succeeded = 1
			}
			require.NoError(t, controller.Status().Update(ctx, sibling))
			_, err = newIntegrationBackupManager(t).Reconcile(ctx, logr.Discard(), cluster)
			require.NoError(t, err)
			// LastBackupTime must be strictly later than B's server creation time,
			// so the existing stale-result guard applies to this receipt.
			require.Eventually(t, func() bool {
				return time.Now().UTC().Truncate(time.Second).After(sibling.CreationTimestamp.Time)
			}, 2*time.Second, 10*time.Millisecond)
			owned.Status.Succeeded = 1
			require.NoError(t, controller.Status().Update(ctx, owned))
			_, err = newIntegrationBackupManager(t).Reconcile(ctx, logr.Discard(), cluster)
			require.NoError(t, err)
			key := client.ObjectKeyFromObject(cluster)
			require.NoError(t, k8sClient.Get(ctx, key, cluster))
			require.Equal(t, siblingRequest, cluster.Annotations[constants.AnnotationTriggerBackup])
			require.Nil(t, cluster.Status.OperationLock)
			creates := 0
			counting := interceptor.NewClient(controller, interceptor.Funcs{
				Create: func(callCtx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*batchv1.Job); ok {
						creates++
					}
					return c.Create(callCtx, obj, opts...)
				},
			})
			_, err = newIntegrationBackupManagerWithClient(t, counting).Reconcile(ctx, logr.Discard(), cluster)
			require.NoError(t, err)
			require.NoError(t, k8sClient.Get(ctx, key, cluster))
			require.Equal(t, siblingRequest, cluster.Status.Backup.LastHandledManualTrigger)
			require.Empty(t, cluster.Annotations[constants.AnnotationTriggerBackup])
			require.Equal(t, "owned-terminal-key", cluster.Status.Backup.LastBackupName)
			require.EqualValues(t, 1, cluster.Status.Backup.SuccessfulBackups)
			require.Equal(t, 0, creates)
			var jobs batchv1.JobList
			require.NoError(t, k8sClient.List(ctx, &jobs, client.InNamespace(cluster.Namespace)))
			require.Equal(t, 2, len(jobs.Items))
		})
	}
}
