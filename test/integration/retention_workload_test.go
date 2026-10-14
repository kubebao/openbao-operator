//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/backup"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

func TestRetentionWorkload_AzureIdentityAdmissionAndTerminalEvidence(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)
	controllerClient := newControllerClient(t)
	jobController := newImpersonatedClientWithGroups(t,
		"system:serviceaccount:kube-system:job-controller", "system:masters")
	cluster := newMinimalClusterObj(namespace, "retention")
	cluster.Spec.Backup = &api.BackupSchedule{Schedule: "0 0 * * *", Image: "openbao-backup:dev", JWTAuthRole: "backup",
		Retention: &api.BackupRetention{MaxCount: 1}, Target: api.BackupTarget{Provider: "azure", Bucket: "snapshots", PathPrefix: "backups",
			Azure: &api.AzureTargetConfig{StorageAccount: "account"},
			WorkloadIdentity: &api.WorkloadIdentityConfig{ServiceAccountAnnotations: map[string]string{
				"azure.workload.identity/client-id": "11111111-1111-1111-1111-111111111111"},
				PodLabels: map[string]string{"azure.workload.identity/use": "true"}},
		}}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	require.NoError(t, backup.EnsureBackupServiceAccount(ctx, controllerClient, k8sScheme, cluster))
	key := backup.GetBackupListPrefix("backups", namespace, cluster.Name) + "2026-10-14T10-00-00Z-aaaaaaaa.snap"
	source, err := backup.BuildJob(cluster, backup.JobOptions{JobName: "source-backup", JobType: backup.JobTypeScheduled, BackupKey: key})
	require.NoError(t, err)
	require.NoError(t, opslifecycle.PrepareManagedJobOwner(source, cluster, k8sScheme))
	require.NoError(t, controllerClient.Create(ctx, source))
	source.Status.Succeeded = 1
	require.NoError(t, jobController.Status().Update(ctx, source))
	updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) {
		status.Backup = &api.BackupStatus{LastBackupName: key, SuccessfulBackups: 1}
		status.OperationLock = &api.OperationLockStatus{Holder: constants.ControllerNameOpenBaoCluster + "/backup",
			Operation: api.ClusterOperationBackup, Message: fmt.Sprintf("backup job %s", source.Name)}
	})
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	manager := newIntegrationBackupManager(t)
	_, err = manager.Reconcile(ctx, testr.New(t), cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Status.Backup.Retention, "backup status: %+v", cluster.Status.Backup)
	require.NotNil(t, cluster.Status.Backup.Retention.Active, "retention status: %+v", cluster.Status.Backup.Retention)
	require.NotNil(t, cluster.Status.Backup.Retention.Active.SubmittedAt)
	worker := &batchv1.Job{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: cluster.Status.Backup.Retention.Active.Name}, worker))
	require.Equal(t, cluster.Name+constants.SuffixBackupServiceAccount, worker.Spec.Template.Spec.ServiceAccountName)
	require.Equal(t, "true", worker.Spec.Template.Labels["azure.workload.identity/use"])
	require.Empty(t, worker.Spec.Template.Spec.Volumes, "Azure token injection belongs to the external webhook; OpenBao volumes are absent")
	_, err = newIntegrationBackupManager(t).Reconcile(ctx, logr.Discard(), cluster)
	require.NoError(t, err)
	require.Equal(t, worker.UID, cluster.Status.Backup.Retention.Active.UID)
	report, err := json.Marshal(backup.RetentionReport{SourceJobUID: string(source.UID), Key: key, Succeeded: true, Reason: "Completed"})
	require.NoError(t, err)
	makePod := func(name string, running bool) *corev1.Pod {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace,
			Labels: worker.Spec.Template.Labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job",
				Name: worker.Name, UID: worker.UID, Controller: ptr.To(true)}}}, Spec: *worker.Spec.Template.Spec.DeepCopy()}
		require.NoError(t, jobController.Create(ctx, pod))
		state := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: string(report), ExitCode: 0}}
		if running {
			state = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		}
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "retention", Image: "executor", ImageID: "executor", State: state}}
		require.NoError(t, k8sClient.Status().Update(ctx, pod))
		return pod
	}
	makePod("completed-worker", false)
	running := makePod("unlabelled-worker", true)
	before := running.DeepCopy()
	running.Labels = nil
	require.NoError(t, controllerClient.Patch(ctx, running, client.MergeFrom(before)))
	worker.Status.Succeeded = 1
	finished := metav1.Now()
	worker.Status.StartTime = &finished
	worker.Status.CompletionTime = &finished
	worker.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: finished},
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: finished},
	}
	require.NoError(t, jobController.Status().Update(ctx, worker))
	_, err = manager.Reconcile(ctx, logr.Discard(), cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Status.Backup.Retention.Active, "terminal Job status cannot hide an unlabelled running executor")
	// Finish both observed processes. A controller restart still preserves snapshot success.
	running.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: string(report)}}
	require.NoError(t, k8sClient.Status().Update(ctx, running))
	_, err = newIntegrationBackupManager(t).Reconcile(ctx, logr.Discard(), cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Status.Backup.Retention.Active)
	require.Nil(t, cluster.Status.OperationLock)
	require.Equal(t, api.BackupRetentionSucceeded, cluster.Status.Backup.Retention.Last.Outcome)
	require.Equal(t, key, cluster.Status.Backup.LastBackupName)
	require.EqualValues(t, 1, cluster.Status.Backup.SuccessfulBackups)
	require.Zero(t, cluster.Status.Backup.ConsecutiveFailures)
	require.WithinDuration(t, time.Now(), cluster.Status.Backup.Retention.Last.FinishedAt.Time, time.Minute)
}
