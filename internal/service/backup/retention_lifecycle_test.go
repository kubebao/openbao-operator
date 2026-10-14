package backup

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
)

func newRetentionSource(t *testing.T) (*api.OpenBaoCluster, *batchv1.Job) {
	t.Helper()
	cluster := newTestClusterWithBackup("retention-source", "default")
	cluster.Generation = 1
	cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
	cluster.Spec.Backup.Retention = &api.BackupRetention{MaxCount: 1}
	cluster.Spec.Backup.Target.RoleARN = "arn:aws:iam::123456789012:role/writer"
	key := GetBackupListPrefix(cluster.Spec.Backup.Target.PathPrefix, cluster.Namespace, cluster.Name) + "2026-10-14T10-00-00Z-aaaaaaaa.snap"
	job, err := BuildJob(cluster, JobOptions{JobName: "source-backup", JobType: JobTypeScheduled, BackupKey: key})
	require.NoError(t, err)
	job.UID = "source-job-uid"
	job.Status.Succeeded = 1
	managedBackupJobForCluster(job, cluster)
	cluster.Status.Backup.LastBackupName = key
	cluster.Status.OperationLock = &api.OperationLockStatus{Holder: backupOperationLockHolder,
		Operation: api.ClusterOperationBackup, Message: fmt.Sprintf("backup job %s", job.Name)}
	return cluster, job
}

func reserveRetentionFixture(t *testing.T) (*Manager, client.Client, *api.OpenBaoCluster, *batchv1.Job) {
	t.Helper()
	cluster, source := newRetentionSource(t)
	c := newTestClient(t, cluster, source)
	m := newBackupManager(c)
	require.NoError(t, m.reserveRetention(t.Context(), cluster, source, time.Now().UTC()))
	require.NotNil(t, cluster.Status.Backup.Retention.Active)
	return m, c, cluster, source
}

func retentionWorkerPod(t *testing.T, job *batchv1.Job, report RetentionReport, running bool) *corev1.Pod {
	t.Helper()
	message, err := json.Marshal(report)
	require.NoError(t, err)
	state := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: string(message)}}
	if running {
		state = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	} else if !report.Succeeded {
		state.Terminated.ExitCode = 5
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-pod", Namespace: job.Namespace,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name,
			UID: job.UID, Controller: ptr.To(true)}}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: retentionComponent, Image: "executor"}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: retentionComponent, State: state}}}}
}

func TestRetentionReservationAndRestoreTestCAS(t *testing.T) {
	for _, retentionWins := range []bool{false, true} {
		t.Run(fmt.Sprint(retentionWins), func(t *testing.T) {
			cluster, source := newRetentionSource(t)
			c := newTestClient(t, cluster, source)
			m := newBackupManager(c)
			stale := cluster.DeepCopy()
			expected := stale.Status.Backup.DeepCopy()
			stale.Status.Backup.RestoreTest = &api.RestoreTestStatus{Active: &api.RestoreTestRun{Key: "older.snap", Name: "test"}}
			if retentionWins {
				require.NoError(t, m.reserveRetention(t.Context(), cluster, source, time.Now().UTC()))
				require.ErrorContains(t, m.patchRestoreTestStatus(t.Context(), stale, expected), "retention is active")
			} else {
				require.NoError(t, m.patchRestoreTestStatus(t.Context(), stale, expected))
				require.ErrorContains(t, m.reserveRetention(t.Context(), cluster, source, time.Now().UTC()), "backup changed")
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
				require.NoError(t, m.reserveRetention(t.Context(), cluster, source, time.Now().UTC()))
				require.Equal(t, "older.snap", cluster.Status.Backup.Retention.Active.RestoreTestKey)
			}
		})
	}
}

func TestStaleBackupStatusPreservesRetention(t *testing.T) {
	m, c, cluster, _ := reserveRetentionFixture(t)
	stale := cluster.DeepCopy()
	stale.Status.Backup.Retention = nil
	require.NoError(t, m.patchStatusSSA(t.Context(), stale))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.NotNil(t, cluster.Status.Backup.Retention.Active)
}

func TestRetentionLostSubmissionReadBackNeverCreates(t *testing.T) {
	m, c, cluster, _ := reserveRetentionFixture(t)
	original := m.adminOpsMutator
	m.WithAdminOpsStatusMutator(func(ctx context.Context, obj *api.OpenBaoCluster, mutate func(*api.OpenBaoCluster) error, ownership adminops.OwnershipPolicy) error {
		if err := original(ctx, obj, mutate, ownership); err != nil {
			return err
		}
		return errors.New("lost status read-back")
	})
	_, err := m.reconcileRetention(t.Context(), logr.Discard(), cluster, time.Now().UTC())
	require.ErrorContains(t, err, "lost status read-back")
	jobs := &batchv1.JobList{}
	require.NoError(t, c.List(t.Context(), jobs))
	require.Len(t, jobs.Items, 1, "only the source Job exists")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.NotNil(t, cluster.Status.Backup.Retention.Active.SubmittedAt)
	m = newBackupManager(c)
	_, err = m.reconcileRetention(t.Context(), logr.Discard(), cluster, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.NotNil(t, cluster.Status.Backup.Retention.Active)
	require.Equal(t, "ExecutionUnknown", cluster.Status.Backup.Retention.Conditions[0].Reason)
}

func TestRetentionLostCreateAcknowledgementBindsWithoutRetry(t *testing.T) {
	cluster, source := newRetentionSource(t)
	creates := 0
	c := fake.NewClientBuilder().WithScheme(testScheme).WithStatusSubresource(&api.OpenBaoCluster{}).
		WithObjects(cluster, source).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			creates++
			obj.SetUID("retention-worker-uid")
			if err := c.Create(ctx, obj, opts...); err != nil {
				return err
			}
			return errors.New("response lost")
		},
	}).Build()
	m := newBackupManager(c)
	require.NoError(t, m.reserveRetention(t.Context(), cluster, source, time.Now().UTC()))
	_, err := m.reconcileRetention(t.Context(), logr.Discard(), cluster, time.Now().UTC())
	require.NoError(t, err)
	m = newBackupManager(c)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	_, err = m.reconcileRetention(t.Context(), logr.Discard(), cluster, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, types.UID("retention-worker-uid"), cluster.Status.Backup.Retention.Active.UID)
	require.Equal(t, 1, creates)
}

func TestRetentionRequiresAllOwnedPodsToTerminate(t *testing.T) {
	for _, state := range []string{"running", "unlabelled running", "missing", "replaced UID", "terminated"} {
		t.Run(state, func(t *testing.T) {
			m, c, cluster, source := reserveRetentionFixture(t)
			run := cluster.Status.Backup.Retention.Active
			run.SubmittedAt = ptrToTime(time.Now().UTC())
			run.UID = "worker-uid"
			require.NoError(t, c.Status().Update(t.Context(), cluster))
			job, err := buildRetentionJob(source, run)
			require.NoError(t, err)
			job.UID = run.UID
			if state == "replaced UID" {
				job.UID = "replacement-retention-worker"
			}
			managedBackupJobForCluster(job, cluster)
			job.Status.Succeeded = 1
			require.NoError(t, c.Create(t.Context(), job))
			report := RetentionReport{SourceJobUID: string(source.UID), Key: run.Key, Succeeded: true, Reason: "Completed", DeletedByCount: 2}
			if state != "missing" {
				pod := retentionWorkerPod(t, job, report, state == "running")
				require.NoError(t, c.Create(t.Context(), pod))
				if state == "unlabelled running" {
					pod = retentionWorkerPod(t, job, report, true)
					pod.Name += "-other"
					require.NoError(t, c.Create(t.Context(), pod))
				}
			}
			beforeKey := cluster.Status.Backup.LastBackupName
			// Finishing the frozen workload remains permitted after a spec change.
			cluster.Generation++
			cluster.Spec.Backup = nil
			require.NoError(t, c.Update(t.Context(), cluster))
			_, err = m.reconcileRetention(t.Context(), logr.Discard(), cluster, time.Now().Add(time.Hour))
			require.NoError(t, err)
			require.Equal(t, beforeKey, cluster.Status.Backup.LastBackupName)
			if state == "terminated" {
				require.Nil(t, cluster.Status.Backup.Retention.Active)
				require.Nil(t, cluster.Status.OperationLock)
				require.Equal(t, api.BackupRetentionSucceeded, cluster.Status.Backup.Retention.Last.Outcome)
			} else {
				require.NotNil(t, cluster.Status.Backup.Retention.Active)
				require.NotNil(t, cluster.Status.OperationLock)
			}
		})
	}
}

func TestRetentionLockReleasePinsSourceUIDAndMessage(t *testing.T) {
	const replacedSourceCluster = "source cluster replaced"
	for _, replacement := range []string{replacedSourceCluster, "new backup message"} {
		t.Run(replacement, func(t *testing.T) {
			cluster, source := newRetentionSource(t)
			stale := cluster.DeepCopy()
			if replacement == replacedSourceCluster {
				cluster.UID = "replacement-retention-source"
			} else {
				cluster.Status.OperationLock.Message = "backup job newer-backup"
			}
			c := newTestClient(t, cluster)
			err := newBackupManager(c).releaseRetentionLock(t.Context(), stale, source.Name)
			if replacement == replacedSourceCluster {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			require.NotNil(t, cluster.Status.OperationLock)
		})
	}
}

func TestRetentionWorkerTemplateContainsStorageOnly(t *testing.T) {
	for _, provider := range []string{"s3", "gcs", "azure"} {
		t.Run(provider, func(t *testing.T) {
			cluster, _ := newRetentionSource(t)
			cluster.Spec.Backup.Target.Provider = provider
			if provider != "s3" {
				cluster.Spec.Backup.Target.RoleARN = ""
			}
			cluster.Spec.Backup.Target.CredentialsSecretRef = &corev1.LocalObjectReference{Name: "storage-only"}
			cluster.Spec.Backup.Target.WorkloadIdentity = &api.WorkloadIdentityConfig{PodLabels: map[string]string{"azure.workload.identity/use": "true"}}
			cluster.Spec.Backup.TokenSecretRef = &corev1.LocalObjectReference{Name: "bao-token"}
			_, source := newRetentionSource(t)
			job, err := BuildJob(cluster, JobOptions{JobName: source.Name, JobType: JobTypeScheduled, BackupKey: source.Annotations["openbao.org/backup-key"], VerifiedExecutorDigest: "registry/executor@sha256:verified"})
			require.NoError(t, err)
			worker, err := buildRetentionJob(job, &api.BackupRetentionRun{Name: "retention", SourceJobUID: source.UID})
			require.NoError(t, err)
			require.Equal(t, backupServiceAccountName(cluster), worker.Spec.Template.Spec.ServiceAccountName)
			require.Equal(t, "registry/executor@sha256:verified", worker.Spec.Template.Spec.Containers[0].Image)
			require.Equal(t, "true", worker.Spec.Template.Labels["azure.workload.identity/use"])
			require.Equal(t, ComponentBackup, worker.Spec.Template.Labels[constants.LabelOpenBaoComponent])
			for _, env := range worker.Spec.Template.Spec.Containers[0].Env {
				require.NotContains(t, []string{constants.EnvBackupAuthMethod, constants.EnvBackupJWTAuthRole, constants.EnvBackupTokenPath}, env.Name)
			}
			for _, volume := range worker.Spec.Template.Spec.Volumes {
				require.True(t, retentionStorageVolume(volume.Name), "%s", volume.Name)
				if volume.Projected != nil {
					for _, item := range volume.Projected.Sources {
						require.Equal(t, awsWebIdentityTokenAudience, item.ServiceAccountToken.Audience)
					}
				}
			}
			require.False(t, *worker.Spec.Template.Spec.AutomountServiceAccountToken)
			require.Zero(t, *worker.Spec.BackoffLimit)
		})
	}
}

func TestRetentionWorkerPreservesNestedS3Identity(t *testing.T) {
	for _, role := range []string{"nested-writer", ""} {
		t.Run("role="+role, func(t *testing.T) {
			cluster, _ := newRetentionSource(t)
			cluster.Spec.Backup.Target.Region = "legacy-region"
			cluster.Spec.Backup.Target.UsePathStyle = true
			cluster.Spec.Backup.Target.S3 = &api.S3TargetConfig{
				Region: ptr.To("nested-region"), UsePathStyle: ptr.To(false), RoleARN: ptr.To(role),
			}
			source, err := BuildJob(cluster, JobOptions{JobName: "source", JobType: JobTypeScheduled})
			require.NoError(t, err)
			worker, err := buildRetentionJob(source, &api.BackupRetentionRun{Name: "retention", SourceJobUID: "source-uid"})
			require.NoError(t, err)
			env := map[string]string{}
			for _, value := range worker.Spec.Template.Spec.Containers[0].Env {
				env[value.Name] = value.Value
			}
			require.Equal(t, "nested-region", env[constants.EnvBackupRegion])
			require.Equal(t, "false", env[constants.EnvBackupUsePathStyle])
			require.Equal(t, role, env[constants.EnvAWSRoleARN])
			var projected bool
			for _, volume := range worker.Spec.Template.Spec.Volumes {
				if volume.Name == awsWebIdentityVolumeName {
					projected = true
				}
			}
			require.Equal(t, role != "", projected)
			require.Equal(t, role != "", env[constants.EnvAWSWebIdentityTokenFile] != "")
		})
	}
}

func TestRetentionConflictingReceiptsDoNotHideRunningExecutor(t *testing.T) {
	m, c, cluster, source := reserveRetentionFixture(t)
	run := cluster.Status.Backup.Retention.Active
	job, err := buildRetentionJob(source, run)
	require.NoError(t, err)
	job.UID = "worker-uid"
	report := RetentionReport{SourceJobUID: string(source.UID), Key: run.Key, Succeeded: true, Reason: "Completed"}
	for i := 0; i < 3; i++ {
		pod := retentionWorkerPod(t, job, report, i == 2)
		pod.Name = fmt.Sprintf("executor-%d", i)
		require.NoError(t, c.Create(t.Context(), pod))
		report.DeletedByCount++
	}
	stopped, receipt, err := m.observeRetentionPods(t.Context(), job, run)
	require.NoError(t, err)
	require.False(t, stopped)
	require.Nil(t, receipt)
}

func TestAlreadyRecordedSnapshotStillStartsRetentionAfterRestart(t *testing.T) {
	cluster, source := newRetentionSource(t)
	c := newTestClient(t, cluster, source)
	m := newBackupManager(c)
	observation := backupObservation{ownsLock: true, now: time.Now().UTC(),
		jobs: backupJobObservation{mostRecentTerminal: source}}
	_, err := m.applyFinalizeBackup(t.Context(), logr.Discard(), cluster, observation)
	require.NoError(t, err)
	require.NotNil(t, cluster.Status.Backup.Retention.Active.SubmittedAt)
	require.Equal(t, source.Annotations["openbao.org/backup-key"], cluster.Status.Backup.LastBackupName)
	jobs := &batchv1.JobList{}
	require.NoError(t, c.List(t.Context(), jobs, client.MatchingLabels{constants.LabelOpenBaoBackupType: retentionType}))
	require.Len(t, jobs.Items, 1)
}

func TestRetentionWorkerDoesNotReuseSourceJobIdentityLabels(t *testing.T) {
	_, source := newRetentionSource(t)
	labels := map[string]string{
		batchv1.ControllerUidLabel: string(source.UID),
		batchv1.JobNameLabel:       source.Name,
		"controller-uid":           string(source.UID),
		"job-name":                 source.Name,
	}
	for name, value := range labels {
		source.Spec.Template.Labels[name] = value
	}
	source.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{batchv1.ControllerUidLabel: string(source.UID)}}
	worker, err := buildRetentionJob(source, &api.BackupRetentionRun{Name: "retention-worker", SourceJobUID: source.UID})
	require.NoError(t, err)
	for name, value := range labels {
		require.NotContains(t, worker.Spec.Template.Labels, name)
		require.NotContains(t, worker.Labels, name)
		require.Equal(t, value, source.Spec.Template.Labels[name], "source Job remains unchanged")
	}
	require.Nil(t, worker.Spec.Selector, "the API server assigns the new Job selector")
}
