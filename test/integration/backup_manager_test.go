//go:build integration
// +build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/security"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/backup"
)

func newIntegrationBackupManager(t *testing.T) *backup.Manager {
	t.Helper()

	controllerClient := newControllerClient(t)
	return backup.NewManager(
		controllerClient,
		k8sScheme,
		portopenbao.ClientConfig{},
		security.NewImageVerifier(logr.Discard(), k8sClient, nil),
		"",
	).WithReader(k8sClient).WithAdminOpsStatusMutator(adminopsstatus.NewMutator(k8sClient, k8sClient))
}

func TestBackupManager_InitialSchedulePersistsAcrossReconciles(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(fmt.Sprintf("initialized=%t", initialized), func(t *testing.T) {
			namespace := newTestNamespace(t)
			cluster := newMinimalClusterObj(namespace, "initial-schedule")
			cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
				Schedule: "0 0 * * *",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "https://minio.example",
					Bucket:   testBackupBucket,
				},
				JWTAuthRole: "backup",
				Image:       "openbao-backup:dev",
			}
			require.NoError(t, k8sClient.Create(ctx, cluster))
			updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
				status.Initialized = initialized
				status.Phase = openbaov1alpha1.ClusterPhaseRunning
				status.CurrentVersion = cluster.Spec.Version
				status.UpgradeRequests = &openbaov1alpha1.UpgradeRequestStatus{LastHandledRetry: "keep-me"}
			})

			var latest openbaov1alpha1.OpenBaoCluster
			key := client.ObjectKeyFromObject(cluster)
			require.NoError(t, k8sClient.Get(ctx, key, &latest))
			require.Nil(t, latest.Status.Backup)
			manager := newIntegrationBackupManager(t)
			result, err := manager.Reconcile(ctx, logr.Discard(), &latest)
			require.NoError(t, err)
			require.Positive(t, result.RequeueAfter)

			var stored openbaov1alpha1.OpenBaoCluster
			require.NoError(t, k8sClient.Get(ctx, key, &stored))
			require.NotNil(t, stored.Status.Backup)
			require.NotNil(t, stored.Status.Backup.NextScheduledBackup)
			next := stored.Status.Backup.NextScheduledBackup.DeepCopy()
			require.Equal(t, "keep-me", stored.Status.UpgradeRequests.LastHandledRetry)

			// Use a new manager and a fresh API read to model a controller restart.
			manager = newIntegrationBackupManager(t)
			_, err = manager.Reconcile(ctx, logr.Discard(), &stored)
			require.NoError(t, err)
			require.NoError(t, k8sClient.Get(ctx, key, &latest))
			require.Equal(t, next, latest.Status.Backup.NextScheduledBackup)
			require.Equal(t, "keep-me", latest.Status.UpgradeRequests.LastHandledRetry)
			var jobs batchv1.JobList
			require.NoError(t, k8sClient.List(ctx, &jobs, client.InNamespace(namespace)))
			require.Empty(t, jobs.Items, "future schedule must not start a backup")
		})
	}
}

func TestBackupManager_ManualTrigger_CreatesJobAndWiring(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "backup-manager")
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule: "0 0 * * *",
		Target: openbaov1alpha1.BackupTarget{
			Endpoint:   "https://minio.example",
			Bucket:     testBackupBucket,
			RoleARN:    "arn:aws:iam::123456789012:role/openbao-backup",
			Region:     "us-east-1",
			PathPrefix: "openbao",
		},
		JWTAuthRole: "backup",
		Image:       "openbao-backup:dev",
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
		status.Phase = openbaov1alpha1.ClusterPhaseRunning
		status.CurrentVersion = cluster.Spec.Version
	})

	var latest openbaov1alpha1.OpenBaoCluster
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &latest); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	original := latest.DeepCopy()
	if latest.Annotations == nil {
		latest.Annotations = map[string]string{}
	}
	latest.Annotations[constants.AnnotationTriggerBackup] = testTrueString
	if err := k8sClient.Patch(ctx, &latest, client.MergeFrom(original)); err != nil {
		t.Fatalf("set trigger annotation: %v", err)
	}

	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &latest); err != nil {
		t.Fatalf("get cluster after trigger: %v", err)
	}

	mgr := newIntegrationBackupManager(t)
	result, err := mgr.Reconcile(ctx, logr.Discard(), &latest)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("expected requeue while backup job is in progress")
	}

	// ServiceAccount + RBAC.
	saName := cluster.Name + constants.SuffixBackupServiceAccount
	sa := &corev1.ServiceAccount{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: saName}, sa); err != nil {
		t.Fatalf("expected backup ServiceAccount to exist: %v", err)
	}

	role := &rbacv1.Role{}
	roleName := saName + "-role"
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: roleName}, role); err != nil {
		t.Fatalf("expected backup Role to exist: %v", err)
	}

	rb := &rbacv1.RoleBinding{}
	rbName := saName + "-rolebinding"
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: rbName}, rb); err != nil {
		t.Fatalf("expected backup RoleBinding to exist: %v", err)
	}

	// Backup Job exists.
	var jobs batchv1.JobList
	if err := k8sClient.List(ctx, &jobs,
		client.InNamespace(namespace),
		client.MatchingLabels(map[string]string{
			constants.LabelOpenBaoCluster:   cluster.Name,
			constants.LabelOpenBaoComponent: backup.ComponentBackup,
		}),
	); err != nil {
		t.Fatalf("list backup jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 backup job, got %d", len(jobs.Items))
	}
	job := &jobs.Items[0]
	if !strings.HasPrefix(job.Name, "backup-"+cluster.Name+"-") {
		t.Fatalf("unexpected job name %q", job.Name)
	}
	if job.Annotations["openbao.org/backup-key"] == "" {
		t.Fatalf("expected job to have openbao.org/backup-key annotation")
	}
	jobEnv := envVarMap(job.Spec.Template.Spec.Containers[0].Env)
	if got := jobEnv[constants.EnvOpenBaoJWTAuthStrategy]; got != portopenbao.JWTAuthStrategyInline {
		t.Fatalf("%s=%q, want %q", constants.EnvOpenBaoJWTAuthStrategy, got, portopenbao.JWTAuthStrategyInline)
	}
	if got := jobEnv[constants.EnvBackupJWTAuthRole]; got != "backup" {
		t.Fatalf("%s=%q, want backup", constants.EnvBackupJWTAuthRole, got)
	}

	// Manual trigger annotation is cleared (best-effort).
	var after openbaov1alpha1.OpenBaoCluster
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &after); err != nil {
		t.Fatalf("get cluster after reconcile: %v", err)
	}
	if after.Annotations != nil {
		if _, ok := after.Annotations[constants.AnnotationTriggerBackup]; ok {
			t.Fatalf("expected trigger annotation to be cleared")
		}
	}
}

func TestBackupManager_RestoreInProgress_ReleasesStaleBackupLock(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "backup-restore-lock")
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule: "0 0 * * *",
		Target: openbaov1alpha1.BackupTarget{
			Endpoint: "https://minio.example",
			Bucket:   testBackupBucket,
		},
		JWTAuthRole: "backup",
		Image:       "openbao-backup:dev",
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
		status.Phase = openbaov1alpha1.ClusterPhaseRunning
		status.CurrentVersion = cluster.Spec.Version
		status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationBackup,
			Holder:    constants.ControllerNameOpenBaoCluster + "/backup",
			Message:   "backup in progress",
		}
	})

	restoreObj := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-in-progress",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: cluster.Name,
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.enc",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "https://minio.example",
					Bucket:   testBackupBucket,
				},
			},
			JWTAuthRole: "restore-role",
			Image:       "openbao-backup:dev",
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseRunning,
		},
	}
	if err := k8sClient.Create(ctx, restoreObj); err != nil {
		t.Fatalf("create OpenBaoRestore: %v", err)
	}

	var latest openbaov1alpha1.OpenBaoCluster
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &latest); err != nil {
		t.Fatalf("get cluster: %v", err)
	}

	mgr := newIntegrationBackupManager(t)
	result, err := mgr.Reconcile(ctx, logr.Discard(), &latest)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter != constants.RequeueShort {
		t.Fatalf("expected short requeue while restore is in progress, got %v", result.RequeueAfter)
	}

	var updated openbaov1alpha1.OpenBaoCluster
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &updated); err != nil {
		t.Fatalf("get updated cluster: %v", err)
	}
	if updated.Status.OperationLock != nil {
		t.Fatalf("expected stale backup operation lock to be released, got %#v", updated.Status.OperationLock)
	}

	var jobs batchv1.JobList
	if err := k8sClient.List(ctx, &jobs,
		client.InNamespace(namespace),
		client.MatchingLabels(map[string]string{
			constants.LabelOpenBaoCluster:   cluster.Name,
			constants.LabelOpenBaoComponent: backup.ComponentBackup,
		}),
	); err != nil {
		t.Fatalf("list backup jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("expected no backup jobs while restore is in progress, got %d", len(jobs.Items))
	}
}

func TestBackupManager_ManualTrigger_BlockedByOperationLock(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "backup-lock-held")
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule: "0 0 * * *",
		Target: openbaov1alpha1.BackupTarget{
			Endpoint: "https://minio.example",
			Bucket:   testBackupBucket,
		},
		JWTAuthRole: "backup",
		Image:       "openbao-backup:dev",
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
		status.Phase = openbaov1alpha1.ClusterPhaseRunning
		status.CurrentVersion = cluster.Spec.Version
		status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationUpgrade,
			Holder:    constants.ControllerNameOpenBaoCluster + "/upgrade",
			Message:   "upgrade in progress",
		}
	})

	var latest openbaov1alpha1.OpenBaoCluster
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &latest); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	original := latest.DeepCopy()
	if latest.Annotations == nil {
		latest.Annotations = map[string]string{}
	}
	latest.Annotations[constants.AnnotationTriggerBackup] = testTrueString
	if err := k8sClient.Patch(ctx, &latest, client.MergeFrom(original)); err != nil {
		t.Fatalf("set trigger annotation: %v", err)
	}

	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &latest); err != nil {
		t.Fatalf("get cluster after trigger: %v", err)
	}

	mgr := newIntegrationBackupManager(t)
	result, err := mgr.Reconcile(ctx, logr.Discard(), &latest)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter != constants.RequeueStandard {
		t.Fatalf(
			"expected requeue=%v when backup lock acquisition is blocked, got %v",
			constants.RequeueStandard,
			result.RequeueAfter,
		)
	}

	var jobs batchv1.JobList
	if err := k8sClient.List(ctx, &jobs,
		client.InNamespace(namespace),
		client.MatchingLabels(map[string]string{
			constants.LabelOpenBaoCluster:   cluster.Name,
			constants.LabelOpenBaoComponent: backup.ComponentBackup,
		}),
	); err != nil {
		t.Fatalf("list backup jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("expected no backup jobs when lock is held by another operation, got %d", len(jobs.Items))
	}
}

func TestBackupManager_CompletedFailureThenSuccess_ClearsStaleFailureStatus(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "backup-fail-recover")
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule: "0 0 * * *",
		Target: openbaov1alpha1.BackupTarget{
			Endpoint: "https://minio.example",
			Bucket:   testBackupBucket,
		},
		JWTAuthRole: "backup",
		Image:       "openbao-backup:dev",
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
		status.Phase = openbaov1alpha1.ClusterPhaseRunning
		status.CurrentVersion = cluster.Spec.Version
		next := metav1.NewTime(time.Now().Add(1 * time.Hour))
		status.Backup = &openbaov1alpha1.BackupStatus{
			NextScheduledBackup: &next,
		}
	})

	createCompletedBackupJobForCluster(t, namespace, cluster.Name, "backup-failure-job", "", 0, 1)

	var latest openbaov1alpha1.OpenBaoCluster
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &latest); err != nil {
		t.Fatalf("get cluster: %v", err)
	}

	mgr := newIntegrationBackupManager(t)
	firstResult, err := mgr.Reconcile(ctx, logr.Discard(), &latest)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if firstResult.RequeueAfter != constants.RequeueShort {
		t.Fatalf(
			"expected first reconcile requeue=%v after processing failed job, got %v",
			constants.RequeueShort,
			firstResult.RequeueAfter,
		)
	}

	var afterFailure openbaov1alpha1.OpenBaoCluster
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &afterFailure); err != nil {
		t.Fatalf("get cluster after failed job processing: %v", err)
	}
	if afterFailure.Status.Backup == nil {
		t.Fatalf("expected backup status to be initialized")
	}
	if afterFailure.Status.Backup.ConsecutiveFailures != 1 {
		t.Fatalf("expected consecutiveFailures=1 after failed job, got %d", afterFailure.Status.Backup.ConsecutiveFailures)
	}
	if afterFailure.Status.Backup.LastFailureReason != backup.ReasonBackupFailed {
		t.Fatalf(
			"expected lastFailureReason=%q after failed job, got %q",
			backup.ReasonBackupFailed,
			afterFailure.Status.Backup.LastFailureReason,
		)
	}
	if afterFailure.Status.Backup.LastFailureMessage == "" {
		t.Fatalf("expected lastFailureMessage to be set after failed job")
	}

	// Backup manager selects the most recent completed job by creation timestamp.
	// Sleep long enough to ensure second-level timestamp ordering under envtest.
	time.Sleep(1100 * time.Millisecond)

	createCompletedBackupJobForCluster(t, namespace, cluster.Name, "backup-success-job", "recovery-key", 1, 0)

	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &latest); err != nil {
		t.Fatalf("get cluster before recovery reconcile: %v", err)
	}

	secondResult, err := mgr.Reconcile(ctx, logr.Discard(), &latest)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if secondResult.RequeueAfter != constants.RequeueShort {
		t.Fatalf(
			"expected second reconcile requeue=%v after processing successful job, got %v",
			constants.RequeueShort,
			secondResult.RequeueAfter,
		)
	}

	var afterRecovery openbaov1alpha1.OpenBaoCluster
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &afterRecovery); err != nil {
		t.Fatalf("get cluster after successful job processing: %v", err)
	}
	if afterRecovery.Status.Backup == nil {
		t.Fatalf("expected backup status to be initialized")
	}
	if afterRecovery.Status.Backup.ConsecutiveFailures != 0 {
		t.Fatalf(
			"expected consecutiveFailures reset to 0 after recovery, got %d",
			afterRecovery.Status.Backup.ConsecutiveFailures,
		)
	}
	if afterRecovery.Status.Backup.LastFailureReason != "" {
		t.Fatalf(
			"expected lastFailureReason to be cleared after recovery, got %q",
			afterRecovery.Status.Backup.LastFailureReason,
		)
	}
	if afterRecovery.Status.Backup.LastFailureMessage != "" {
		t.Fatalf(
			"expected lastFailureMessage to be cleared after recovery, got %q",
			afterRecovery.Status.Backup.LastFailureMessage,
		)
	}
	if afterRecovery.Status.Backup.LastBackupName != "recovery-key" {
		t.Fatalf("expected lastBackupName to be updated to recovery-key, got %q", afterRecovery.Status.Backup.LastBackupName)
	}
}

func createCompletedBackupJobForCluster(
	t *testing.T,
	namespace, clusterName, jobName, backupKey string,
	succeeded, failed int32,
) {
	t.Helper()
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: clusterName}, cluster); err != nil {
		t.Fatalf("get OpenBaoCluster %q: %v", clusterName, err)
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(
					cluster,
					openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
				),
			},
			Labels: map[string]string{
				constants.LabelAppInstance:       clusterName,
				constants.LabelAppManagedBy:      constants.LabelValueAppManagedByOpenBaoOperator,
				constants.LabelOpenBaoCluster:    clusterName,
				constants.LabelOpenBaoComponent:  backup.ComponentBackup,
				constants.LabelOpenBaoBackupType: constants.BackupTypeScheduled,
			},
			Annotations: map[string]string{
				constants.AnnotationOpenBaoOwnerUID: string(cluster.UID),
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "test",
							Image:   "busybox:1.36",
							Command: []string{"sh", "-c", testTrueString},
						},
					},
				},
			},
		},
	}
	if backupKey != "" {
		job.Annotations["openbao.org/backup-key"] = backupKey
	}

	controllerClient := newControllerClient(t)
	if err := controllerClient.Create(ctx, job); err != nil {
		t.Fatalf("create backup job %q: %v", jobName, err)
	}

	var latest batchv1.Job
	if err := controllerClient.Get(ctx, client.ObjectKeyFromObject(job), &latest); err != nil {
		t.Fatalf("get backup job %q: %v", jobName, err)
	}

	latest.Status.Succeeded = succeeded
	latest.Status.Failed = failed
	now := metav1.Now()
	latest.Status.StartTime = &now
	if err := controllerClient.Status().Update(ctx, &latest); err != nil {
		t.Fatalf("update backup job status %q: %v", jobName, err)
	}
}
