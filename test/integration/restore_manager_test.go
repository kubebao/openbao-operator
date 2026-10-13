//go:build integration
// +build integration

package integration

import (
	"strings"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/adapter/security"
	"github.com/dc-tec/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	"github.com/dc-tec/openbao-operator/internal/platform/constants"
	portopenbao "github.com/dc-tec/openbao-operator/internal/port/openbao"
	"github.com/dc-tec/openbao-operator/internal/service/restore"
)

func TestRestoreManager_TransitionsAndCreatesJob(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "restore-target")
	cluster.Spec.TLS.Enabled = false
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
	})

	restoreObj := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-1",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: cluster.Name,
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.enc",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "http://minio." + namespace + ".svc",
					Bucket:   testBackupBucket,
				},
			},
			Image:       "openbao-backup:dev",
			JWTAuthRole: "restore-role", // Required for auth validation
		},
	}
	if err := k8sClient.Create(ctx, restoreObj); err != nil {
		t.Fatalf("create OpenBaoRestore: %v", err)
	}

	controllerClient := newControllerClient(t)
	verifier := security.NewImageVerifier(logr.Discard(), k8sClient, nil)
	mgr := withIntegrationRestoreStatusPersistence(
		restore.NewManager(controllerClient, k8sScheme, nil, verifier, ""),
		controllerClient,
	)

	// Pending -> Validating
	latest := &openbaov1alpha1.OpenBaoRestore{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	res, err := mgr.Reconcile(ctx, logr.Discard(), latest)
	if err != nil {
		t.Fatalf("reconcile pending: %v", err)
	}
	if res == (ctrl.Result{}) {
		t.Fatalf("expected requeue from pending->validating")
	}

	// Validating -> Running
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	res, err = mgr.Reconcile(ctx, logr.Discard(), latest)
	if err != nil {
		t.Fatalf("reconcile validating: %v", err)
	}
	if res == (ctrl.Result{}) {
		t.Fatalf("expected requeue from validating->running")
	}

	// Running -> create Job and RBAC
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	res, err = mgr.Reconcile(ctx, logr.Discard(), latest)
	if err != nil {
		t.Fatalf("reconcile running (create job): %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected requeue after creating restore job")
	}

	job := &batchv1.Job{}
	jobName := restore.RestoreJobNamePrefix + latest.Name
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: jobName}, job); err != nil {
		t.Fatalf("expected restore job to exist: %v", err)
	}
	jobEnv := envVarMap(job.Spec.Template.Spec.Containers[0].Env)
	if got := jobEnv[constants.EnvOpenBaoJWTAuthStrategy]; got != portopenbao.JWTAuthStrategyInline {
		t.Fatalf("%s=%q, want %q", constants.EnvOpenBaoJWTAuthStrategy, got, portopenbao.JWTAuthStrategyInline)
	}
	if got := jobEnv[constants.EnvBackupJWTAuthRole]; got != "restore-role" {
		t.Fatalf("%s=%q, want restore-role", constants.EnvBackupJWTAuthRole, got)
	}

	// Mark job succeeded.
	setRestoreJobTerminalStatus(job, 1, 0)
	if err := k8sClient.Status().Update(ctx, job); err != nil {
		t.Fatalf("update job status: %v", err)
	}

	// Running -> request voter restart.
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	_, err = mgr.Reconcile(ctx, logr.Discard(), latest)
	if err != nil {
		t.Fatalf("reconcile after job success: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore after voter restart request: %v", err)
	}
	if latest.Status.Phase != openbaov1alpha1.RestorePhaseRunning {
		t.Fatalf("phase=%s want=%s", latest.Status.Phase, openbaov1alpha1.RestorePhaseRunning)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cluster.Name}, cluster); err != nil {
		t.Fatalf("get cluster after voter restart request: %v", err)
	}
	if cluster.Status.Restore == nil || cluster.Status.Restore.UID != string(latest.UID) {
		t.Fatalf("cluster restore status=%+v want UID=%q", cluster.Status.Restore, latest.UID)
	}
	if cluster.Status.OperationLock == nil {
		t.Fatalf("operation lock released before voter restart completed")
	}

	createSettledRestoreVoterStatefulSet(controllerClient, cluster, latest, t.Fatalf)

	// Running -> Completed after the voter rollout settles.
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore before voter restart completion: %v", err)
	}
	if _, err := mgr.Reconcile(ctx, logr.Discard(), latest); err != nil {
		t.Fatalf("reconcile after voter restart: %v", err)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	if latest.Status.Phase != openbaov1alpha1.RestorePhaseCompleted {
		t.Fatalf("phase=%s want=%s", latest.Status.Phase, openbaov1alpha1.RestorePhaseCompleted)
	}

	// Restore service account should exist (created during validation).
	sa := &corev1.ServiceAccount{}
	saName := cluster.Name + restore.RestoreServiceAccountSuffix
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: saName}, sa); err != nil {
		if apierrors.IsNotFound(err) {
			t.Fatalf("expected restore ServiceAccount %q to exist", saName)
		}
		t.Fatalf("get restore ServiceAccount: %v", err)
	}
}

func createSettledRestoreVoterStatefulSet(
	controllerClient client.Client,
	cluster *openbaov1alpha1.OpenBaoCluster,
	restoreObj *openbaov1alpha1.OpenBaoRestore,
	failf func(string, ...any),
) {
	replicas := cluster.Spec.Replicas
	labels := map[string]string{"app.kubernetes.io/name": cluster.Name}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster")),
			},
			Annotations: map[string]string{
				constants.AnnotationOpenBaoOwnerUID: string(cluster.UID),
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: cluster.Name,
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						constants.AnnotationRestoreRevision: string(restoreObj.UID),
					},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "openbao", Image: "openbao/openbao:dev"}}},
			},
		},
	}
	if err := controllerClient.Create(ctx, statefulSet); err != nil {
		failf("create voter StatefulSet: %v", err)
		return
	}
	if err := controllerClient.Get(ctx, types.NamespacedName{Namespace: statefulSet.Namespace, Name: statefulSet.Name}, statefulSet); err != nil {
		failf("get voter StatefulSet: %v", err)
		return
	}
	statefulSet.Status = appsv1.StatefulSetStatus{
		ObservedGeneration: statefulSet.Generation,
		Replicas:           replicas,
		ReadyReplicas:      replicas,
		UpdatedReplicas:    replicas,
		CurrentReplicas:    replicas,
		CurrentRevision:    "restored-revision",
		UpdateRevision:     "restored-revision",
	}
	if err := controllerClient.Status().Update(ctx, statefulSet); err != nil {
		failf("update voter StatefulSet status: %v", err)
	}
}

func withIntegrationRestoreStatusPersistence(manager *restore.Manager, controllerClient client.Client) *restore.Manager {
	return manager.WithAdminOpsStatusMutator(adminopsstatus.NewMutator(controllerClient, controllerClient))
}

func TestRestoreManager_GCSProvider(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "restore-target-gcs")
	cluster.Spec.TLS.Enabled = false
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
	})

	restoreObj := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-gcs",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: cluster.Name,
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.enc",
				Target: openbaov1alpha1.BackupTarget{
					Provider: "gcs",
					Endpoint: "https://storage.googleapis.com",
					Bucket:   testBackupBucket,
					GCS: &openbaov1alpha1.GCSTargetConfig{
						Project: "my-gcp-project",
					},
				},
			},
			Image:       "openbao-backup:dev",
			JWTAuthRole: "restore-role",
		},
	}
	if err := k8sClient.Create(ctx, restoreObj); err != nil {
		t.Fatalf("create OpenBaoRestore: %v", err)
	}

	controllerClient := newControllerClient(t)
	verifier := security.NewImageVerifier(logr.Discard(), k8sClient, nil)
	mgr := restore.NewManager(controllerClient, k8sScheme, nil, verifier, "")

	// Pending -> Validating
	latest := &openbaov1alpha1.OpenBaoRestore{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	res, err := mgr.Reconcile(ctx, logr.Discard(), latest)
	if err != nil {
		t.Fatalf("reconcile pending: %v", err)
	}
	if res == (ctrl.Result{}) {
		t.Fatalf("expected requeue from pending->validating")
	}

	// Validating -> Running
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	res, err = mgr.Reconcile(ctx, logr.Discard(), latest)
	if err != nil {
		t.Fatalf("reconcile validating: %v", err)
	}
	if res == (ctrl.Result{}) {
		t.Fatalf("expected requeue from validating->running")
	}

	// Running -> create Job
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	res, err = mgr.Reconcile(ctx, logr.Discard(), latest)
	if err != nil {
		t.Fatalf("reconcile running (create job): %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected requeue after creating restore job")
	}

	// Verify job exists and has correct env vars
	job := &batchv1.Job{}
	jobName := restore.RestoreJobNamePrefix + latest.Name
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: jobName}, job); err != nil {
		t.Fatalf("expected restore job to exist: %v", err)
	}

	// Verify GCS-specific environment variables are set
	container := job.Spec.Template.Spec.Containers[0]
	envMap := envVarMap(container.Env)

	if envMap[constants.EnvBackupProvider] != "gcs" {
		t.Errorf("BACKUP_PROVIDER = %v, want gcs", envMap[constants.EnvBackupProvider])
	}
	if envMap[constants.EnvBackupGCSProject] != "my-gcp-project" {
		t.Errorf("BACKUP_GCS_PROJECT = %v, want my-gcp-project", envMap[constants.EnvBackupGCSProject])
	}
	if envMap[constants.EnvBackupEndpoint] != "https://storage.googleapis.com" {
		t.Errorf("BACKUP_ENDPOINT = %v, want https://storage.googleapis.com", envMap[constants.EnvBackupEndpoint])
	}
	if envMap[constants.EnvBackupBucket] != testBackupBucket {
		t.Errorf("BACKUP_BUCKET = %v, want %s", envMap[constants.EnvBackupBucket], testBackupBucket)
	}

	// Verify S3-specific vars are NOT set
	if envMap[constants.EnvBackupRegion] != "" {
		t.Errorf("BACKUP_REGION should not be set for GCS, got %v", envMap[constants.EnvBackupRegion])
	}
	if envMap[constants.EnvBackupUsePathStyle] != "" {
		t.Errorf("BACKUP_USE_PATH_STYLE should not be set for GCS, got %v", envMap[constants.EnvBackupUsePathStyle])
	}

	// Verify Azure-specific vars are NOT set
	if envMap[constants.EnvBackupAzureStorageAccount] != "" {
		t.Errorf(
			"BACKUP_AZURE_STORAGE_ACCOUNT should not be set for GCS, got %v",
			envMap[constants.EnvBackupAzureStorageAccount],
		)
	}
}

func TestRestoreManager_AzureProvider(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "restore-target-azure")
	cluster.Spec.TLS.Enabled = false
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
	})

	restoreObj := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-azure",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: cluster.Name,
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.enc",
				Target: openbaov1alpha1.BackupTarget{
					Provider: "azure",
					Endpoint: "https://myaccount.blob.core.windows.net",
					Bucket:   testBackupBucket,
					Azure: &openbaov1alpha1.AzureTargetConfig{
						StorageAccount: "myaccount",
						Container:      testBackupBucket,
					},
				},
			},
			Image:       "openbao-backup:dev",
			JWTAuthRole: "restore-role",
		},
	}
	if err := k8sClient.Create(ctx, restoreObj); err != nil {
		t.Fatalf("create OpenBaoRestore: %v", err)
	}

	controllerClient := newControllerClient(t)
	verifier := security.NewImageVerifier(logr.Discard(), k8sClient, nil)
	mgr := restore.NewManager(controllerClient, k8sScheme, nil, verifier, "")

	// Pending -> Validating
	latest := &openbaov1alpha1.OpenBaoRestore{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	res, err := mgr.Reconcile(ctx, logr.Discard(), latest)
	if err != nil {
		t.Fatalf("reconcile pending: %v", err)
	}
	if res == (ctrl.Result{}) {
		t.Fatalf("expected requeue from pending->validating")
	}

	// Validating -> Running
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	res, err = mgr.Reconcile(ctx, logr.Discard(), latest)
	if err != nil {
		t.Fatalf("reconcile validating: %v", err)
	}
	if res == (ctrl.Result{}) {
		t.Fatalf("expected requeue from validating->running")
	}

	// Running -> create Job
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	res, err = mgr.Reconcile(ctx, logr.Discard(), latest)
	if err != nil {
		t.Fatalf("reconcile running (create job): %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected requeue after creating restore job")
	}

	// Verify job exists and has correct env vars
	job := &batchv1.Job{}
	jobName := restore.RestoreJobNamePrefix + latest.Name
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: jobName}, job); err != nil {
		t.Fatalf("expected restore job to exist: %v", err)
	}

	// Verify Azure-specific environment variables are set
	container := job.Spec.Template.Spec.Containers[0]
	envMap := envVarMap(container.Env)

	if envMap[constants.EnvBackupProvider] != "azure" {
		t.Errorf("BACKUP_PROVIDER = %v, want azure", envMap[constants.EnvBackupProvider])
	}
	if envMap[constants.EnvBackupAzureStorageAccount] != "myaccount" {
		t.Errorf("BACKUP_AZURE_STORAGE_ACCOUNT = %v, want myaccount", envMap[constants.EnvBackupAzureStorageAccount])
	}
	if envMap[constants.EnvBackupAzureContainer] != testBackupBucket {
		t.Errorf("BACKUP_AZURE_CONTAINER = %v, want %s", envMap[constants.EnvBackupAzureContainer], testBackupBucket)
	}
	if envMap[constants.EnvBackupEndpoint] != "https://myaccount.blob.core.windows.net" {
		t.Errorf("BACKUP_ENDPOINT = %v, want https://myaccount.blob.core.windows.net", envMap[constants.EnvBackupEndpoint])
	}
	if envMap[constants.EnvBackupBucket] != testBackupBucket {
		t.Errorf("BACKUP_BUCKET = %v, want %s", envMap[constants.EnvBackupBucket], testBackupBucket)
	}

	// Verify S3-specific vars are NOT set
	if envMap[constants.EnvBackupRegion] != "" {
		t.Errorf("BACKUP_REGION should not be set for Azure, got %v", envMap[constants.EnvBackupRegion])
	}
	if envMap[constants.EnvBackupUsePathStyle] != "" {
		t.Errorf("BACKUP_USE_PATH_STYLE should not be set for Azure, got %v", envMap[constants.EnvBackupUsePathStyle])
	}

	// Verify GCS-specific vars are NOT set
	if envMap[constants.EnvBackupGCSProject] != "" {
		t.Errorf("BACKUP_GCS_PROJECT should not be set for Azure, got %v", envMap[constants.EnvBackupGCSProject])
	}
}

func TestRestoreManager_ValidatingLockContention_RequeuesWithWaitingMessage(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "restore-lock-contention")
	cluster.Spec.TLS.Enabled = false
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
	})

	restoreObj := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-lock-contention",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: cluster.Name,
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.enc",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "http://minio." + namespace + ".svc",
					Bucket:   testBackupBucket,
				},
			},
			Image:       "openbao-backup:dev",
			JWTAuthRole: "restore-role",
		},
	}
	if err := k8sClient.Create(ctx, restoreObj); err != nil {
		t.Fatalf("create OpenBaoRestore: %v", err)
	}

	controllerClient := newControllerClient(t)
	verifier := security.NewImageVerifier(logr.Discard(), k8sClient, nil)
	mgr := restore.NewManager(controllerClient, k8sScheme, nil, verifier, "")
	latest := &openbaov1alpha1.OpenBaoRestore{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	if _, err := mgr.Reconcile(ctx, logr.Discard(), latest); err != nil {
		t.Fatalf("reconcile pending: %v", err)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationBackup,
			Holder:    constants.ControllerNameOpenBaoCluster + "/backup",
			Message:   "backup operation",
		}
	})

	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore for validating: %v", err)
	}
	if latest.Status.Phase != openbaov1alpha1.RestorePhaseValidating {
		t.Fatalf("expected restore to be in Validating after first reconcile, got %s", latest.Status.Phase)
	}

	result, err := mgr.Reconcile(ctx, logr.Discard(), latest)
	if err != nil {
		t.Fatalf("reconcile validating with lock contention: %v", err)
	}
	if result.RequeueAfter != constants.RequeueShort {
		t.Fatalf("expected requeue=%v when lock is held, got %v", constants.RequeueShort, result.RequeueAfter)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get updated restore: %v", err)
	}
	if latest.Status.Phase != openbaov1alpha1.RestorePhaseValidating {
		t.Fatalf("expected restore to remain in Validating phase, got %s", latest.Status.Phase)
	}
	if !strings.Contains(latest.Status.Message, "Waiting for cluster operation lock") {
		t.Fatalf("expected waiting-for-lock message, got %q", latest.Status.Message)
	}
}

func TestRestoreManager_RunningLockTaken_FailsDeterministically(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "restore-running-lock-taken")
	cluster.Spec.TLS.Enabled = false
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
	})

	restoreObj := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-running-lock-taken",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: cluster.Name,
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.enc",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "http://minio." + namespace + ".svc",
					Bucket:   testBackupBucket,
				},
			},
			Image:       "openbao-backup:dev",
			JWTAuthRole: "restore-role",
		},
	}
	if err := k8sClient.Create(ctx, restoreObj); err != nil {
		t.Fatalf("create OpenBaoRestore: %v", err)
	}

	controllerClient := newControllerClient(t)
	verifier := security.NewImageVerifier(logr.Discard(), k8sClient, nil)
	mgr := restore.NewManager(controllerClient, k8sScheme, nil, verifier, "")
	latest := &openbaov1alpha1.OpenBaoRestore{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	if _, err := mgr.Reconcile(ctx, logr.Discard(), latest); err != nil {
		t.Fatalf("reconcile pending: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore for validating: %v", err)
	}
	if _, err := mgr.Reconcile(ctx, logr.Discard(), latest); err != nil {
		t.Fatalf("reconcile validating: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore for running: %v", err)
	}
	if latest.Status.Phase != openbaov1alpha1.RestorePhaseRunning {
		t.Fatalf("expected restore phase Running before lock-steal simulation, got %s", latest.Status.Phase)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationUpgrade,
			Holder:    constants.ControllerNameOpenBaoCluster + "/upgrade",
			Message:   "upgrade operation",
		}
	})

	if _, err := mgr.Reconcile(ctx, logr.Discard(), latest); err != nil {
		t.Fatalf("reconcile running with stolen lock: %v", err)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get updated restore: %v", err)
	}
	if latest.Status.Phase != openbaov1alpha1.RestorePhaseFailed {
		t.Fatalf("expected restore to fail when lock is stolen, got phase %s", latest.Status.Phase)
	}
	expected := "Restore stopped because another operation took the cluster operation lock " +
		"while the restore Job was running. Check concurrent backup or upgrade activity, " +
		"then create a new OpenBaoRestore to retry."
	if latest.Status.Message != expected {
		t.Fatalf("expected failure message %q, got %q", expected, latest.Status.Message)
	}
}

func TestRestoreManager_FailedJob_RemainsTerminalAcrossReconcileRetries(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "restore-terminal-failure")
	cluster.Spec.TLS.Enabled = false
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
	})

	restoreObj := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-terminal-failure",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: cluster.Name,
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.enc",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "http://minio." + namespace + ".svc",
					Bucket:   testBackupBucket,
				},
			},
			Image:       "openbao-backup:dev",
			JWTAuthRole: "restore-role",
		},
	}
	if err := k8sClient.Create(ctx, restoreObj); err != nil {
		t.Fatalf("create OpenBaoRestore: %v", err)
	}

	controllerClient := newControllerClient(t)
	verifier := security.NewImageVerifier(logr.Discard(), k8sClient, nil)
	mgr := restore.NewManager(controllerClient, k8sScheme, nil, verifier, "")
	latest := &openbaov1alpha1.OpenBaoRestore{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	if _, err := mgr.Reconcile(ctx, logr.Discard(), latest); err != nil {
		t.Fatalf("reconcile pending: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore for validating: %v", err)
	}
	if _, err := mgr.Reconcile(ctx, logr.Discard(), latest); err != nil {
		t.Fatalf("reconcile validating: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore for running: %v", err)
	}
	if latest.Status.Phase != openbaov1alpha1.RestorePhaseRunning {
		t.Fatalf("expected restore phase Running before failed job processing, got %s", latest.Status.Phase)
	}

	createRestoreJobWithStatus(t, restoreObj, restore.RestoreJobNamePrefix+restoreObj.Name, 0, 1)

	if _, err := mgr.Reconcile(ctx, logr.Discard(), latest); err != nil {
		t.Fatalf("reconcile running with failed job: %v", err)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get restore after committed Job observation: %v", err)
	}
	if latest.Status.Phase != openbaov1alpha1.RestorePhaseRunning {
		t.Fatalf("expected restore phase Running while the creation receipt becomes durable, got %s", latest.Status.Phase)
	}
	if latest.Status.Execution == nil || latest.Status.Execution.Stage != openbaov1alpha1.RestoreExecutionStageCreated {
		t.Fatalf("expected restore execution stage Created before terminal observation, got %+v", latest.Status.Execution)
	}

	if _, err := mgr.Reconcile(ctx, logr.Discard(), latest); err != nil {
		t.Fatalf("reconcile created restore with failed job: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get failed restore: %v", err)
	}
	if latest.Status.Phase != openbaov1alpha1.RestorePhaseFailed {
		t.Fatalf("expected restore phase Failed after failed job, got %s", latest.Status.Phase)
	}
	jobName := restore.RestoreJobNamePrefix + restoreObj.Name
	expectedPrefix := "Restore Job " + jobName + " failed. Check kubectl logs job/" + jobName +
		" -n " + namespace + " and create a new OpenBaoRestore to retry."
	if !strings.Contains(latest.Status.Message, expectedPrefix) {
		t.Fatalf("expected failed-job message to contain %q, got %q", expectedPrefix, latest.Status.Message)
	}
	if !strings.Contains(latest.Status.Message, "generated ServiceAccount") {
		t.Fatalf("expected failed-job message to include identity hint, got %q", latest.Status.Message)
	}
	if latest.Status.CompletionTime == nil {
		t.Fatalf("expected completionTime to be set on terminal failure")
	}
	terminalMessage := latest.Status.Message

	if _, err := mgr.Reconcile(ctx, logr.Discard(), latest); err != nil {
		t.Fatalf("reconcile terminal restore: %v", err)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restoreObj.Name}, latest); err != nil {
		t.Fatalf("get terminal restore after second reconcile: %v", err)
	}
	if latest.Status.Phase != openbaov1alpha1.RestorePhaseFailed {
		t.Fatalf("expected restore to remain Failed after terminal reconcile, got %s", latest.Status.Phase)
	}
	if latest.Status.Message != terminalMessage {
		t.Fatalf("expected terminal failure message to remain stable, got %q", latest.Status.Message)
	}
}

func createRestoreJobWithStatus(
	t *testing.T,
	restoreObj *openbaov1alpha1.OpenBaoRestore,
	name string,
	succeeded, failed int32,
) {
	t.Helper()

	owner := &openbaov1alpha1.OpenBaoRestore{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: restoreObj.Namespace,
		Name:      restoreObj.Name,
	}, owner); err != nil {
		t.Fatalf("get OpenBaoRestore %q: %v", restoreObj.Name, err)
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: owner.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(
					owner,
					openbaov1alpha1.GroupVersion.WithKind("OpenBaoRestore"),
				),
			},
			Annotations: map[string]string{
				constants.AnnotationOpenBaoOwnerUID: string(owner.UID),
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
	controllerClient := newControllerClient(t)
	if err := controllerClient.Create(ctx, job); err != nil {
		t.Fatalf("create restore job %q: %v", name, err)
	}

	latest := &batchv1.Job{}
	if err := controllerClient.Get(ctx, types.NamespacedName{Namespace: owner.Namespace, Name: name}, latest); err != nil {
		t.Fatalf("get restore job %q: %v", name, err)
	}

	setRestoreJobTerminalStatus(latest, succeeded, failed)
	now := metav1.Now()
	latest.Status.StartTime = &now
	if err := controllerClient.Status().Update(ctx, latest); err != nil {
		t.Fatalf("update restore job status %q: %v", name, err)
	}
}
