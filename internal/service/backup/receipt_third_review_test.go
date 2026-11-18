package backup

import (
	"context"
	"encoding/json"
	"errors"
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
)

func TestThrottledBackupCreateCanRetry(t *testing.T) {
	cluster := newTestClusterWithBackup("throttled-create", "default")
	cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
	cluster.Status.Initialized = true
	cluster.Status.CurrentVersion = cluster.Spec.Version
	const request = "throttled-request"
	cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: request}
	base := newTestClient(t, cluster)
	creates := 0
	c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*batchv1.Job); !ok {
				return c.Create(ctx, obj, opts...)
			}
			creates++
			if creates == 1 {
				return apierrors.NewTooManyRequests("API priority and fairness rejected Create", 1)
			}
			return c.Create(ctx, obj, opts...)
		},
	})
	_, err := newBackupManager(c).Reconcile(t.Context(), logr.Discard(), cluster)
	require.True(t, apierrors.IsTooManyRequests(err))
	require.NoError(t, base.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.Nil(t, cluster.Status.OperationLock)
	require.Equal(t, request, cluster.Annotations[constants.AnnotationTriggerBackup])
	_, err = newBackupManager(c).Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Equal(t, 2, creates)
	require.NoError(t, base.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.Equal(t, request, cluster.Status.Backup.LastHandledManualTrigger)
	require.Empty(t, cluster.Annotations[constants.AnnotationTriggerBackup])
}

func TestSameReceiptPreservesEditedBackupDeadline(t *testing.T) {
	cluster := newTestClusterWithBackup("same-receipt", "default")
	attempt := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	liveDeadline := attempt.Add(75 * time.Minute)
	cluster.Status.OperationLock = &api.OperationLockStatus{
		Operation: api.ClusterOperationBackup, Holder: backupOperationLockHolder,
		Message: backupJobLockMessage(backupJobName(cluster, attempt)),
	}
	cluster.Status.Backup.LastAttemptTime = &metav1.Time{Time: attempt}
	cluster.Status.Backup.LastAttemptScheduledTime = &metav1.Time{Time: attempt}
	cluster.Status.Backup.NextScheduledBackup = &metav1.Time{Time: liveDeadline}
	const request = "same-attempt-request"
	cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: request}
	c := newTestClient(t, cluster)
	require.NoError(t, newBackupManager(c).recordBackupAttempt(t.Context(), cluster,
		attempt, attempt, attempt.Add(30*time.Minute), request))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.True(t, liveDeadline.Equal(cluster.Status.Backup.NextScheduledBackup.Time))
	require.Equal(t, request, cluster.Status.Backup.LastHandledManualTrigger)
}

func TestOversizedTriggerCleanupFailurePreservesBackupProgress(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "due-scheduled-backup", true: "owned-completed-job"}[completed], func(t *testing.T) {
			cluster := newTestClusterWithBackup("cleanup-failure", "default")
			cluster.Spec.Backup.Schedule = "0 * * * *"
			cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
			cluster.Status.Initialized = true
			cluster.Status.CurrentVersion = cluster.Spec.Version
			const previous = "cleanup-previous-ack"
			cluster.Status.Backup.LastHandledManualTrigger = previous
			token := strings.Repeat("x", maxManualBackupTriggerBytes+1)
			cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: token}
			due := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
			cluster.Status.Backup.NextScheduledBackup = &metav1.Time{Time: due}
			var objects []client.Object
			if completed {
				attempt := testBackupAttempt(due)
				job := newBackupJobForCluster(cluster, backupJobName(cluster, due), due)
				job.Status.Succeeded = 1
				job.Annotations["openbao.org/backup-key"] = "cleanup-completed-snapshot"
				stampTestBackupReceipt(t, job, attempt)
				cluster.Status.OperationLock = &api.OperationLockStatus{
					Operation: api.ClusterOperationBackup, Holder: backupOperationLockHolder,
					Message: backupJobLockMessage(job.Name),
				}
				objects = append(objects, job)
			}
			objects = append(objects, cluster)
			base := newTestClient(t, objects...)
			c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if _, ok := obj.(*api.OpenBaoCluster); ok && patch.Type() == types.JSONPatchType {
						return apierrors.NewForbidden(schema.GroupResource{Group: api.GroupVersion.Group, Resource: "openbaoclusters"},
							obj.GetName(), errors.New("trigger cleanup denied"))
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			})
			_, err := newBackupManager(c).Reconcile(t.Context(), logr.Discard(), cluster)
			require.ErrorContains(t, err, "trigger cleanup denied")
			require.NoError(t, base.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			require.Equal(t, token, cluster.Annotations[constants.AnnotationTriggerBackup])
			require.Equal(t, previous, cluster.Status.Backup.LastHandledManualTrigger)
			var jobs batchv1.JobList
			require.NoError(t, base.List(t.Context(), &jobs, client.InNamespace(cluster.Namespace)))
			require.Len(t, jobs.Items, 1)
			if completed {
				require.Equal(t, "cleanup-completed-snapshot", cluster.Status.Backup.LastBackupName)
				require.Nil(t, cluster.Status.OperationLock)
			} else {
				var receipt backupAttemptReceipt
				require.NoError(t, json.Unmarshal([]byte(jobs.Items[0].Annotations[backupAttemptAnnotation]), &receipt))
				require.Empty(t, receipt.ManualTrigger)
			}
		})
	}
}
