package backup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/storage"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/blobstore"
)

func TestApplyFinalizeBackup_FailedCurrentJobSkipsRetention(t *testing.T) {
	cluster := newTestClusterWithBackup("retention-cluster", "default")
	cluster.Spec.Backup.Retention = &openbaov1alpha1.BackupRetention{MaxCount: 1}
	cluster.Spec.Backup.Target.CredentialsSecretRef = &corev1.LocalObjectReference{Name: "backup-creds"}

	scheduledTime := time.Date(2025, 1, 2, 3, 0, 0, 0, time.UTC)
	lastBackupTime := metav1.NewTime(scheduledTime.Add(-24 * time.Hour))
	cluster.Status.Backup.LastBackupTime = &lastBackupTime
	cluster.Status.Backup.LastBackupName = "backups/default/retention-cluster/2025-01-01T03-00-00Z-valid.snap"
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationBackup,
		Holder:    backupOperationLockHolder,
	}

	jobName := backupJobName(cluster, scheduledTime)
	failedJob := newBackupJobForCluster(cluster, jobName, scheduledTime)
	failedJob.Annotations["openbao.org/backup-key"] = "backups/default/retention-cluster/2025-01-02T03-00-00Z-partial.snap"
	failedJob.Status = batchv1.JobStatus{Failed: 1}
	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-creds", Namespace: cluster.Namespace},
		Data: map[string][]byte{
			blobstore.SecretKeyAccessKeyID:     []byte("test-ak"),
			blobstore.SecretKeySecretAccessKey: []byte("test-sk"),
		},
	}

	k8sClient := newTestClient(t, cluster, failedJob, credentials)
	manager := newBackupManager(k8sClient)
	store := &fakeBlobStore{objects: []blobstore.ObjectInfo{
		{Key: cluster.Status.Backup.LastBackupName, LastModified: lastBackupTime.Time},
		{Key: failedJob.Annotations["openbao.org/backup-key"], LastModified: scheduledTime},
	}}
	openCalls := 0
	originalOpenBlobStoreFn := openBlobStoreFn
	openBlobStoreFn = func(context.Context, storage.Config) (blobstore.BlobStore, error) {
		openCalls++
		return store, nil
	}
	defer func() { openBlobStoreFn = originalOpenBlobStoreFn }()

	observation := backupObservation{
		configured:    true,
		ownsLock:      true,
		now:           scheduledTime,
		scheduledTime: scheduledTime,
		jobs: backupJobObservation{
			mostRecentTerminal: failedJob,
		},
	}
	result, err := manager.applyBackupDecision(
		context.Background(),
		logr.Discard(),
		cluster,
		NewMetrics(cluster.Namespace, cluster.Name),
		decideBackup(observation),
	)
	if err != nil {
		t.Fatalf("applyBackupDecision() error = %v", err)
	}
	if result.RequeueAfter != constants.RequeueShort {
		t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, constants.RequeueShort)
	}
	if openCalls != 0 {
		t.Fatalf("retention storage open calls = %d, want 0", openCalls)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("retention deleted objects after failed Job: %v", store.deleted)
	}
}

type failOnceReader struct {
	client.Reader
	err              error
	clusterGetFailed bool
}

type deleteTerminalJobsAfterListReader struct {
	client.Reader
	client  client.Client
	deleted bool
}

func (r *deleteTerminalJobsAfterListReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := r.Reader.List(ctx, list, opts...); err != nil {
		return err
	}
	jobs, ok := list.(*batchv1.JobList)
	if !ok || r.deleted {
		return nil
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if job.Status.Succeeded == 0 && job.Status.Failed == 0 {
			continue
		}
		if err := r.client.Delete(ctx, job); err != nil {
			return err
		}
		r.deleted = true
	}
	return nil
}

func (r *failOnceReader) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*openbaov1alpha1.OpenBaoCluster); ok && !r.clusterGetFailed {
		r.clusterGetFailed = true
		return r.err
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestApplyFinalizeBackup_RetriesTransientLockReleaseFailure(t *testing.T) {
	cluster := newTestClusterWithBackup("release-cluster", "default")
	completedAt := time.Date(2025, 1, 2, 3, 0, 0, 0, time.UTC)
	nextScheduled := completedAt.Add(24 * time.Hour)
	lastBackupTime := metav1.NewTime(completedAt)
	backupKey := "backups/default/release-cluster/2025-01-02T03-00-00Z-valid.snap"
	cluster.Status.Backup.LastBackupTime = &lastBackupTime
	cluster.Status.Backup.LastBackupName = backupKey
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationBackup,
		Holder:    backupOperationLockHolder,
	}

	jobName := backupJobName(cluster, completedAt)
	succeededJob := newBackupJobForCluster(cluster, jobName, completedAt)
	succeededJob.Annotations["openbao.org/backup-key"] = backupKey
	succeededJob.Status = batchv1.JobStatus{Succeeded: 1}

	k8sClient := newTestClient(t, cluster, succeededJob)
	reader := &failOnceReader{Reader: k8sClient, err: errors.New("temporary API read failure")}
	manager := newBackupManager(k8sClient).WithReader(reader)

	decision := decideBackup(backupObservation{
		configured:    true,
		ownsLock:      true,
		now:           completedAt,
		scheduledTime: nextScheduled,
		jobs: backupJobObservation{
			mostRecentTerminal: succeededJob,
		},
	})
	result, err := manager.applyBackupDecision(
		context.Background(),
		logr.Discard(),
		cluster,
		NewMetrics(cluster.Namespace, cluster.Name),
		decision,
	)
	if err != nil {
		t.Fatalf("applyBackupDecision() first call error = %v", err)
	}
	if result.RequeueAfter != constants.RequeueShort {
		t.Fatalf("first RequeueAfter = %v, want %v", result.RequeueAfter, constants.RequeueShort)
	}
	if cluster.Status.OperationLock == nil {
		t.Fatal("operation lock was cleared after failed release")
	}

	_, err = manager.applyBackupDecision(
		context.Background(),
		logr.Discard(),
		cluster,
		NewMetrics(cluster.Namespace, cluster.Name),
		decision,
	)
	if err != nil {
		t.Fatalf("applyBackupDecision() retry error = %v", err)
	}
	if cluster.Status.OperationLock != nil {
		t.Fatalf("operation lock was not cleared on retry: %#v", cluster.Status.OperationLock)
	}
}

func TestReconcile_ProcessesOwnedBackupBeforePendingOperations(t *testing.T) {
	cluster := newTestClusterWithBackup("backup-before-upgrade", "default")
	cluster.Status.Initialized = true
	cluster.Status.CurrentVersion = cluster.Spec.Version
	cluster.Spec.Version = "2.5.0"
	cluster.Spec.Image = "openbao/openbao:2.5.0"
	cluster.Spec.Backup.Schedule = "invalid schedule"
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationBackup,
		Holder:    backupOperationLockHolder,
	}

	completedAt := time.Now().UTC().Add(-time.Minute)
	job := newBackupJobForCluster(cluster, "backup-before-upgrade-complete", completedAt)
	job.Annotations["openbao.org/backup-key"] = "backups/default/backup-before-upgrade/complete.snap"
	job.Status = batchv1.JobStatus{Succeeded: 1}
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pending-restore",
			Namespace: cluster.Namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: cluster.Name},
	}

	k8sClient := newTestClient(t, cluster, job, restore)
	manager := newBackupManager(k8sClient)

	result, err := manager.Reconcile(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != constants.RequeueShort {
		t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, constants.RequeueShort)
	}

	updated := &openbaov1alpha1.OpenBaoCluster{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), updated); err != nil {
		t.Fatalf("Get() cluster error = %v", err)
	}
	if updated.Status.OperationLock != nil {
		t.Fatalf("operation lock = %#v, want released", updated.Status.OperationLock)
	}
	if updated.Status.Backup == nil || updated.Status.Backup.LastBackupName != job.Annotations["openbao.org/backup-key"] {
		t.Fatalf("backup status = %#v, want completed Job result", updated.Status.Backup)
	}
}

func TestReconcile_RestoreInProgressRequeues(t *testing.T) {
	cluster := newTestClusterWithBackup("restore-requeue", "default")
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "active-restore",
			Namespace: cluster.Namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: cluster.Name},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseRunning,
		},
	}

	k8sClient := newTestClient(t, cluster, restore)
	manager := newBackupManager(k8sClient)

	result, err := manager.Reconcile(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != constants.RequeueShort {
		t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, constants.RequeueShort)
	}

	jobs := &batchv1.JobList{}
	if err := k8sClient.List(context.Background(), jobs,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(backupLabels(cluster)),
	); err != nil {
		t.Fatalf("List() backup Jobs error = %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("backup Jobs = %d, want 0", len(jobs.Items))
	}
}

func TestReconcile_ProcessesOwnedTerminalJobFromSingleObservation(t *testing.T) {
	cluster := newTestClusterWithBackup("terminal-list-race", "default")
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationBackup,
		Holder:    backupOperationLockHolder,
	}
	job := newBackupJobForCluster(cluster, "terminal-list-race-complete", time.Now().UTC().Add(-time.Minute))
	job.Annotations["openbao.org/backup-key"] = "backups/default/terminal-list-race/complete.snap"
	job.Status = batchv1.JobStatus{Succeeded: 1}

	k8sClient := newTestClient(t, cluster, job)
	reader := &deleteTerminalJobsAfterListReader{Reader: k8sClient, client: k8sClient}
	manager := newBackupManager(k8sClient).WithReader(reader)

	result, err := manager.Reconcile(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != constants.RequeueShort {
		t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, constants.RequeueShort)
	}
	if !reader.deleted {
		t.Fatal("test reader did not delete the terminal Job after List")
	}

	updated := &openbaov1alpha1.OpenBaoCluster{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), updated); err != nil {
		t.Fatalf("Get() cluster error = %v", err)
	}
	if updated.Status.OperationLock != nil {
		t.Fatalf("operation lock = %#v, want released", updated.Status.OperationLock)
	}
	if updated.Status.Backup == nil || updated.Status.Backup.LastBackupName != job.Annotations["openbao.org/backup-key"] {
		t.Fatalf("backup status = %#v, want terminal Job result", updated.Status.Backup)
	}
}

func TestReconcile_OwnedBackupClearsCrashRecoveredManualTrigger(t *testing.T) {
	cluster := newTestClusterWithBackup("manual-crash-recovery", "default")
	cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "manual-request-1"}
	cluster.Spec.Backup.JWTAuthRole = "backup-role"
	nextScheduled := metav1.NewTime(time.Now().UTC().Add(24 * time.Hour))
	cluster.Status.Backup.NextScheduledBackup = &nextScheduled
	cluster.Status.Initialized = true
	cluster.Status.CurrentVersion = cluster.Spec.Version
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationBackup,
		Holder:    backupOperationLockHolder,
	}
	job := newBackupJobForCluster(cluster, "manual-crash-recovery-complete", time.Now().UTC().Add(-time.Minute))
	job.Annotations["openbao.org/backup-key"] = "backups/default/manual-crash-recovery/complete.snap"
	job.Status = batchv1.JobStatus{Succeeded: 1}

	k8sClient := newTestClient(t, cluster, job)
	manager := newBackupManager(k8sClient)
	if _, err := manager.Reconcile(context.Background(), logr.Discard(), cluster); err != nil {
		t.Fatalf("owned Reconcile() error = %v", err)
	}

	current := &openbaov1alpha1.OpenBaoCluster{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), current); err != nil {
		t.Fatalf("Get() cluster error = %v", err)
	}
	if _, found := current.Annotations[constants.AnnotationTriggerBackup]; found {
		t.Fatal("manual trigger remains after owned backup completion")
	}
	if _, err := manager.Reconcile(context.Background(), logr.Discard(), current); err != nil {
		t.Fatalf("post-recovery Reconcile() error = %v", err)
	}
	jobs := &batchv1.JobList{}
	if err := k8sClient.List(context.Background(), jobs,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: cluster.Name},
	); err != nil {
		t.Fatalf("List() Jobs error = %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("backup Job count = %d, want 1", len(jobs.Items))
	}
}

func TestReconcile_OwnedBackupWithoutJobPreservesManualTrigger(t *testing.T) {
	cluster := newTestClusterWithBackup("manual-before-job-crash", "default")
	cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "manual-request-1"}
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationBackup,
		Holder:    backupOperationLockHolder,
	}

	k8sClient := newTestClient(t, cluster)
	manager := newBackupManager(k8sClient)
	result, err := manager.Reconcile(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != constants.RequeueShort {
		t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, constants.RequeueShort)
	}

	updated := &openbaov1alpha1.OpenBaoCluster{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), updated); err != nil {
		t.Fatalf("Get() cluster error = %v", err)
	}
	if got := updated.Annotations[constants.AnnotationTriggerBackup]; got != "manual-request-1" {
		t.Fatalf("manual trigger = %q, want preserved for retry", got)
	}
	if updated.Status.OperationLock != nil {
		t.Fatalf("operation lock = %#v, want released", updated.Status.OperationLock)
	}
}

func TestReconcile_ReservesRetentionOnceForNewSuccessfulJob(t *testing.T) {
	cluster, job := newRetentionSource(t)
	cluster.Status.Backup.LastBackupName = ""
	job.CreationTimestamp = metav1.NewTime(time.Now().UTC().Add(-time.Minute))
	c := newTestClient(t, cluster, job)
	for i := 0; i < 2; i++ {
		current := &openbaov1alpha1.OpenBaoCluster{}
		if err := c.Get(t.Context(), client.ObjectKeyFromObject(cluster), current); err != nil {
			t.Fatal(err)
		}
		if _, err := newBackupManager(c).Reconcile(t.Context(), logr.Discard(), current); err != nil {
			t.Fatal(err)
		}
	}
	updated := &openbaov1alpha1.OpenBaoCluster{}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(cluster), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Backup.LastBackupName != job.Annotations["openbao.org/backup-key"] || updated.Status.Backup.SuccessfulBackups != 1 {
		t.Fatalf("snapshot result = %#v, want one successful snapshot", updated.Status.Backup)
	}
	if updated.Status.Backup.Retention == nil || updated.Status.Backup.Retention.Active == nil {
		t.Fatal("expected durable independent retention reservation")
	}
	jobs := &batchv1.JobList{}
	if err := c.List(t.Context(), jobs, client.MatchingLabels{constants.LabelOpenBaoBackupType: retentionType}); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("retention Jobs = %d, want one", len(jobs.Items))
	}
}

func TestReconcile_DisabledBackupFinishesOwnedOperation(t *testing.T) {
	testCases := []struct {
		name         string
		jobStatus    *batchv1.JobStatus
		wantLastKey  string
		wantFailures int32
	}{
		{name: "completed Job", jobStatus: &batchv1.JobStatus{Succeeded: 1}, wantLastKey: "backups/default/disabled-backup/complete.snap"},
		{name: "failed Job", jobStatus: &batchv1.JobStatus{Failed: 1}, wantFailures: 1},
		{name: "no Job"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cluster := newTestClusterWithBackup("disabled-backup", "default")
			cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationBackup,
				Holder:    backupOperationLockHolder,
			}
			objects := []client.Object{cluster}
			if testCase.jobStatus != nil {
				job := newBackupJobForCluster(cluster, "disabled-backup-complete", time.Now().UTC().Add(-time.Minute))
				job.Annotations["openbao.org/backup-key"] = testCase.wantLastKey
				job.Status = *testCase.jobStatus
				objects = append(objects, job)
			}
			cluster.Spec.Backup = nil

			k8sClient := newTestClient(t, objects...)
			manager := newBackupManager(k8sClient)
			result, err := manager.Reconcile(context.Background(), logr.Discard(), cluster)
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if result.RequeueAfter != constants.RequeueShort {
				t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, constants.RequeueShort)
			}

			updated := &openbaov1alpha1.OpenBaoCluster{}
			if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), updated); err != nil {
				t.Fatalf("Get() cluster error = %v", err)
			}
			if updated.Status.OperationLock != nil {
				t.Fatalf("operation lock = %#v, want released", updated.Status.OperationLock)
			}
			if testCase.wantLastKey != "" && (updated.Status.Backup == nil || updated.Status.Backup.LastBackupName != testCase.wantLastKey) {
				t.Fatalf("backup status = %#v, want key %q", updated.Status.Backup, testCase.wantLastKey)
			}
			if updated.Status.Backup == nil || updated.Status.Backup.ConsecutiveFailures != testCase.wantFailures {
				t.Fatalf("backup status = %#v, want consecutive failures %d", updated.Status.Backup, testCase.wantFailures)
			}
		})
	}
}

func TestApplyCreateBackup_KeysStaleScheduleWithCreationTime(t *testing.T) {
	cluster := newTestClusterWithBackup("stalled-schedule", "default")
	staleSchedule := time.Date(2025, 1, 2, 3, 0, 0, 0, time.UTC)
	now := staleSchedule.Add(48 * time.Hour)
	next := metav1.NewTime(staleSchedule)
	cluster.Status.Backup.NextScheduledBackup = &next

	k8sClient := newTestClient(t, cluster)
	manager := newBackupManager(k8sClient)

	observation := backupObservation{
		configured:    true,
		due:           true,
		now:           now,
		scheduledTime: staleSchedule,
		nextSchedule:  now.Add(24 * time.Hour),
	}
	if _, err := manager.applyBackupDecision(
		context.Background(),
		logr.Discard(),
		cluster,
		NewMetrics(cluster.Namespace, cluster.Name),
		decideBackup(observation),
	); err != nil {
		t.Fatalf("applyBackupDecision() error = %v", err)
	}

	job := &batchv1.Job{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      backupJobName(cluster, staleSchedule),
	}, job); err != nil {
		t.Fatalf("expected backup Job: %v", err)
	}
	_, _, keyTime, _, err := ParseBackupKey(job.Annotations["openbao.org/backup-key"])
	if err != nil {
		t.Fatalf("ParseBackupKey() error = %v", err)
	}
	if !keyTime.Equal(now) {
		t.Fatalf("backup key time = %s, want creation time %s rather than stale schedule %s", keyTime, now, staleSchedule)
	}
}
