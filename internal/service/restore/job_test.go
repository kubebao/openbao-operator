package restore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/platform/constants"
)

func TestBuildRestoreJob_RetainsExecutionReceipt(t *testing.T) {
	restoreObj := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
			UID:       types.UID("restore-operation-uid"),
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Image:   "example.com/restore-executor:v1",
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshots/test.snap",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "https://s3.example.com",
					Bucket:   "bao",
				},
			},
		},
	}
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
		Spec:       openbaov1alpha1.OpenBaoClusterSpec{Replicas: 3},
	}

	restoreObj.Status.Execution = newRestoreExecutionStatus(restoreObj)
	preparedAt := metav1.NewTime(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC))
	restoreObj.Status.Execution.PreparedAt = &preparedAt
	job, err := (&Manager{Platform: constants.PlatformKubernetes}).buildRestoreJob(restoreObj, cluster, "")
	require.NoError(t, err)
	require.Nil(t, job.Spec.TTLSecondsAfterFinished)
	assert.Equal(t, string(restoreObj.UID), job.Annotations[restoreExecutionIDAnnotation])
	assert.Equal(t, ptr.To(int32(0)), job.Spec.BackoffLimit, "a failed restore must not launch a retry")
	assert.Equal(t, ptr.To(int64(2400)), job.Spec.ActiveDeadlineSeconds, "bound unschedulable and image-pull failures as well as running executors")
	assert.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy,
		"a failed restore container must not repeat snapshot submission")
	container := job.Spec.Template.Spec.Containers[0]
	assert.Contains(t, container.Env, corev1.EnvVar{
		Name: constants.EnvRestorePreparationDeadline, Value: "2026-10-01T08:30:00Z",
	}, "Job construction must not reset the persisted preparation budget")
	assert.Equal(t, int64(9140*1024*1024), container.Resources.Requests.StorageEphemeral().Value())
	assert.Equal(t, int64(9140*1024*1024), container.Resources.Limits.StorageEphemeral().Value())
	assert.Contains(t, container.VolumeMounts, corev1.VolumeMount{
		Name: "restore-scratch", MountPath: constants.PathRestoreScratch,
	})
	var scratch *corev1.EmptyDirVolumeSource
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.Name == "restore-scratch" {
			scratch = volume.EmptyDir
		}
	}
	require.NotNil(t, scratch)
	assert.Empty(t, scratch.Medium, "staging must use disk, not memory")
	assert.Equal(t, int64(9012*1024*1024), scratch.SizeLimit.Value())
}

func TestBuildRestoreJobRequiresPersistedPreparationTime(t *testing.T) {
	for _, execution := range []*openbaov1alpha1.RestoreExecutionStatus{
		nil, {}, {PreparedAt: &metav1.Time{}},
	} {
		restore := &openbaov1alpha1.OpenBaoRestore{
			Status: openbaov1alpha1.OpenBaoRestoreStatus{Execution: execution},
		}
		job, err := (&Manager{}).buildRestoreJob(restore, &openbaov1alpha1.OpenBaoCluster{}, "")
		require.ErrorContains(t, err, "persisted preparation timestamp")
		require.Nil(t, job)
	}
}

func TestBuildRestoreJob_PodSecurityContext_Platform(t *testing.T) {
	restoreObj := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshots/test.snap",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint:     "https://s3.example.com",
					Bucket:       "bao",
					Region:       "us-east-1",
					UsePathStyle: true,
				},
			},
			Image: "example.com/restore-executor:v1",
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
		},
	}

	t.Run("openshift omits pinned IDs", func(t *testing.T) {
		mgr := &Manager{Platform: constants.PlatformOpenShift}
		restoreObj.Status.Execution = newRestoreExecutionStatus(restoreObj)
		job, err := mgr.buildRestoreJob(restoreObj, cluster, "")
		require.NoError(t, err)

		sc := job.Spec.Template.Spec.SecurityContext
		require.NotNil(t, sc)
		require.Equal(t, ptr.To(true), sc.RunAsNonRoot)
		require.NotNil(t, sc.SeccompProfile)
		require.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, sc.SeccompProfile.Type)

		require.Nil(t, sc.RunAsUser)
		require.Nil(t, sc.RunAsGroup)
		require.Nil(t, sc.FSGroup)
	})

	t.Run("kubernetes pins IDs", func(t *testing.T) {
		mgr := &Manager{Platform: constants.PlatformKubernetes}
		restoreObj.Status.Execution = newRestoreExecutionStatus(restoreObj)
		job, err := mgr.buildRestoreJob(restoreObj, cluster, "")
		require.NoError(t, err)

		sc := job.Spec.Template.Spec.SecurityContext
		require.NotNil(t, sc)
		require.Equal(t, ptr.To(true), sc.RunAsNonRoot)

		require.Equal(t, ptr.To(constants.UserBackup), sc.RunAsUser)
		require.Equal(t, ptr.To(constants.GroupBackup), sc.RunAsGroup)
		require.Equal(t, ptr.To(constants.GroupBackup), sc.FSGroup)
	})
}

func TestBuildRestoreJob_WithRoleARNAndWorkloadIdentity(t *testing.T) {
	restoreObj := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshots/test.snap",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "https://s3.amazonaws.com",
					Bucket:   "bao",
					Region:   "eu-central-1",
					RoleARN:  "arn:aws:iam::123456789012:role/openbao-restore",
					WorkloadIdentity: &openbaov1alpha1.WorkloadIdentityConfig{
						PodLabels: map[string]string{
							"azure.workload.identity/use": "true",
							constants.LabelOpenBaoCluster: "should-not-override",
						},
					},
				},
			},
			Image: "example.com/restore-executor:v1",
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
		},
	}

	mgr := &Manager{Platform: constants.PlatformKubernetes}
	restoreObj.Status.Execution = newRestoreExecutionStatus(restoreObj)
	job, err := mgr.buildRestoreJob(restoreObj, cluster, "")
	require.NoError(t, err)

	envMap := make(map[string]string)
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		envMap[env.Name] = env.Value
	}
	assert.Equal(t, restoreObj.Spec.Source.Target.RoleARN, envMap[constants.EnvAWSRoleARN])
	assert.Equal(t, restoreAWSIdentityTokenFile, envMap[constants.EnvAWSWebIdentityTokenFile])

	assert.Equal(t, "true", job.Spec.Template.Labels["azure.workload.identity/use"])
	assert.Equal(t, cluster.Name, job.Spec.Template.Labels[constants.LabelOpenBaoCluster])
	assert.NotContains(t, job.Labels, "azure.workload.identity/use")

	var hasTokenMount bool
	for _, mount := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.Name == restoreAWSIdentityVolume {
			hasTokenMount = true
			assert.Equal(t, restoreAWSIdentityMountPath, mount.MountPath)
			assert.True(t, mount.ReadOnly)
		}
	}
	assert.True(t, hasTokenMount)

	var hasTokenVolume bool
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.Name != restoreAWSIdentityVolume {
			continue
		}
		hasTokenVolume = true
		require.NotNil(t, volume.Projected)
		require.Len(t, volume.Projected.Sources, 1)
		require.NotNil(t, volume.Projected.Sources[0].ServiceAccountToken)
		assert.Equal(t, restoreAWSIdentityAudience, volume.Projected.Sources[0].ServiceAccountToken.Audience)
		assert.Equal(t, "token", volume.Projected.Sources[0].ServiceAccountToken.Path)
	}
	assert.True(t, hasTokenVolume)
}

func TestBuildRestoreEnvVars_S3(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.snap",
				Target: openbaov1alpha1.BackupTarget{
					Provider:     constants.StorageProviderS3,
					Endpoint:     "https://s3.amazonaws.com",
					Bucket:       "test-bucket",
					Region:       "us-west-2",
					UsePathStyle: true,
				},
			},
			JWTAuthRole: "restore-role",
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
		},
	}

	envVars := buildRestoreEnvVars(restore, cluster)

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	// Verify common env vars
	assert.Equal(t, "restore", envMap["EXECUTOR_MODE"])
	assert.Equal(t, "test-cluster", envMap[constants.EnvClusterName])
	assert.Equal(t, "test-cluster", envMap[constants.EnvStatefulSetName])
	assert.Equal(t, "default", envMap[constants.EnvClusterNamespace])
	assert.Equal(t, "3", envMap[constants.EnvClusterReplicas])
	assert.Equal(t, constants.StorageProviderS3, envMap[constants.EnvBackupProvider])
	assert.Equal(t, "https://s3.amazonaws.com", envMap[constants.EnvBackupEndpoint])
	assert.Equal(t, "test-bucket", envMap[constants.EnvBackupBucket])
	assert.Equal(t, "backup.snap", envMap[constants.EnvRestoreKey])
	assert.Equal(t, "test-bucket", envMap[constants.EnvRestoreBucket])
	assert.Equal(t, "https://s3.amazonaws.com", envMap[constants.EnvRestoreEndpoint])
	assert.Equal(t, "false", envMap[constants.EnvRestoreForce])

	// Verify S3-specific env vars
	assert.Equal(t, "us-west-2", envMap[constants.EnvBackupRegion])
	assert.Equal(t, "true", envMap[constants.EnvBackupUsePathStyle])
	assert.Equal(t, "us-west-2", envMap[constants.EnvRestoreRegion])
	assert.Equal(t, "true", envMap[constants.EnvRestoreUsePathStyle])

	// Verify JWT auth
	assert.Equal(t, "restore-role", envMap[constants.EnvBackupJWTAuthRole])
	assert.Equal(t, constants.BackupAuthMethodJWT, envMap[constants.EnvBackupAuthMethod])

	// Verify GCS/Azure vars are NOT set
	assert.Empty(t, envMap[constants.EnvBackupGCSProject])
	assert.Empty(t, envMap[constants.EnvBackupAzureStorageAccount])
	assert.Empty(t, envMap[constants.EnvBackupAzureContainer])
}

func TestBuildRestoreEnvVars_Force(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Force: true,
			Source: openbaov1alpha1.RestoreSource{
				Target: openbaov1alpha1.BackupTarget{},
			},
		},
	}
	cluster := &openbaov1alpha1.OpenBaoCluster{}

	envVars := buildRestoreEnvVars(restore, cluster)
	envMap := make(map[string]string, len(envVars))
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	assert.Equal(t, "true", envMap[constants.EnvRestoreForce])
}

func TestBuildRestoreEnvVars_BlueGreenStatefulSetName(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.snap",
				Target: openbaov1alpha1.BackupTarget{
					Provider: constants.StorageProviderS3,
					Endpoint: "https://s3.amazonaws.com",
					Bucket:   "test-bucket",
				},
			},
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{
				BlueRevision: "abc123",
			},
		},
	}

	envVars := buildRestoreEnvVars(restore, cluster)

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	assert.Equal(t, "test-cluster-abc123", envMap[constants.EnvStatefulSetName])
}

func TestBuildRestoreEnvVars_S3Default(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.snap",
				Target: openbaov1alpha1.BackupTarget{
					// Provider not set, should default to S3
					Endpoint: "https://s3.amazonaws.com",
					Bucket:   "test-bucket",
					Region:   "us-east-1",
				},
			},
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
		},
	}

	envVars := buildRestoreEnvVars(restore, cluster)

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	// Should default to S3
	assert.Equal(t, constants.StorageProviderS3, envMap[constants.EnvBackupProvider])
	assert.Equal(t, "us-east-1", envMap[constants.EnvBackupRegion])
	assert.Equal(t, "false", envMap[constants.EnvBackupUsePathStyle]) // default
}

func TestBuildRestoreEnvVars_GCS(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.snap",
				Target: openbaov1alpha1.BackupTarget{
					Provider: "gcs",
					Endpoint: "https://storage.googleapis.com",
					Bucket:   "test-bucket",
					GCS: &openbaov1alpha1.GCSTargetConfig{
						Project: "my-gcp-project",
					},
				},
			},
			JWTAuthRole: "restore-role",
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 2,
		},
	}

	envVars := buildRestoreEnvVars(restore, cluster)

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	// Verify common env vars
	assert.Equal(t, constants.StorageProviderGCS, envMap[constants.EnvBackupProvider])
	assert.Equal(t, "https://storage.googleapis.com", envMap[constants.EnvBackupEndpoint])
	assert.Equal(t, "test-bucket", envMap[constants.EnvBackupBucket])

	// Verify GCS-specific env var
	assert.Equal(t, "my-gcp-project", envMap[constants.EnvBackupGCSProject])

	// Verify S3/Azure vars are NOT set
	assert.Empty(t, envMap[constants.EnvBackupRegion])
	assert.Empty(t, envMap[constants.EnvBackupUsePathStyle])
	assert.Empty(t, envMap[constants.EnvRestoreRegion])
	assert.Empty(t, envMap[constants.EnvRestoreUsePathStyle])
	assert.Empty(t, envMap[constants.EnvBackupAzureStorageAccount])
	assert.Empty(t, envMap[constants.EnvBackupAzureContainer])
}

func TestBuildRestoreEnvVars_GCS_NoProject(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.snap",
				Target: openbaov1alpha1.BackupTarget{
					Provider: "gcs",
					Bucket:   "test-bucket",
					GCS:      &openbaov1alpha1.GCSTargetConfig{
						// Project not set (optional)
					},
				},
			},
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
		},
	}

	envVars := buildRestoreEnvVars(restore, cluster)

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	// GCS project should not be set when not specified
	assert.Empty(t, envMap[constants.EnvBackupGCSProject])
	assert.Equal(t, constants.StorageProviderGCS, envMap[constants.EnvBackupProvider])
}

func TestBuildRestoreEnvVars_Azure(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.snap",
				Target: openbaov1alpha1.BackupTarget{
					Provider: "azure",
					Endpoint: "https://myaccount.blob.core.windows.net",
					Bucket:   "test-container",
					Azure: &openbaov1alpha1.AzureTargetConfig{
						StorageAccount: "myaccount",
						Container:      "test-container",
					},
				},
			},
			JWTAuthRole: "restore-role",
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
		},
	}

	envVars := buildRestoreEnvVars(restore, cluster)

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	// Verify common env vars
	assert.Equal(t, constants.StorageProviderAzure, envMap[constants.EnvBackupProvider])
	assert.Equal(t, "https://myaccount.blob.core.windows.net", envMap[constants.EnvBackupEndpoint])
	assert.Equal(t, "test-container", envMap[constants.EnvBackupBucket])

	// Verify Azure-specific env vars
	assert.Equal(t, "myaccount", envMap[constants.EnvBackupAzureStorageAccount])
	assert.Equal(t, "test-container", envMap[constants.EnvBackupAzureContainer])

	// Verify S3/GCS vars are NOT set
	assert.Empty(t, envMap[constants.EnvBackupRegion])
	assert.Empty(t, envMap[constants.EnvBackupUsePathStyle])
	assert.Empty(t, envMap[constants.EnvRestoreRegion])
	assert.Empty(t, envMap[constants.EnvRestoreUsePathStyle])
	assert.Empty(t, envMap[constants.EnvBackupGCSProject])
}

func TestBuildRestoreEnvVars_Azure_NoContainer(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.snap",
				Target: openbaov1alpha1.BackupTarget{
					Provider: "azure",
					Bucket:   "test-container",
					Azure: &openbaov1alpha1.AzureTargetConfig{
						StorageAccount: "myaccount",
						// Container not set (optional, uses bucket)
					},
				},
			},
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
		},
	}

	envVars := buildRestoreEnvVars(restore, cluster)

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	// Azure container should not be set when not specified
	assert.Equal(t, "myaccount", envMap[constants.EnvBackupAzureStorageAccount])
	assert.Empty(t, envMap[constants.EnvBackupAzureContainer])
	assert.Equal(t, constants.StorageProviderAzure, envMap[constants.EnvBackupProvider])
}

func TestBuildRestoreEnvVars_JWTAuth(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.snap",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "https://s3.amazonaws.com",
					Bucket:   "test-bucket",
				},
			},
			JWTAuthRole: "restore-role",
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
		},
	}

	envVars := buildRestoreEnvVars(restore, cluster)

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	assert.Equal(t, "restore-role", envMap[constants.EnvBackupJWTAuthRole])
	assert.Equal(t, constants.BackupAuthMethodJWT, envMap[constants.EnvBackupAuthMethod])
}

func TestBuildRestoreEnvVars_TokenAuth(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.snap",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "https://s3.amazonaws.com",
					Bucket:   "test-bucket",
				},
			},
			TokenSecretRef: &corev1.LocalObjectReference{
				Name: "restore-token",
			},
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
		},
	}

	envVars := buildRestoreEnvVars(restore, cluster)

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	assert.Empty(t, envMap[constants.EnvBackupJWTAuthRole])
	assert.Equal(t, constants.BackupAuthMethodToken, envMap[constants.EnvBackupAuthMethod])
}

func TestBuildRestoreJob_IgnoresTokenSecretRefWhenJWTAuthConfigured(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup.snap",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "https://s3.amazonaws.com",
					Bucket:   "test-bucket",
				},
			},
			Image:       "example.com/restore-executor:v1",
			JWTAuthRole: "restore-role",
			TokenSecretRef: &corev1.LocalObjectReference{
				Name: "restore-token",
			},
		},
	}
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
		},
	}

	mgr := &Manager{Platform: constants.PlatformKubernetes}
	restore.Status.Execution = newRestoreExecutionStatus(restore)
	job, err := mgr.buildRestoreJob(restore, cluster, "")
	require.NoError(t, err)

	envMap := make(map[string]string)
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		envMap[env.Name] = env.Value
	}
	assert.Equal(t, "restore-role", envMap[constants.EnvBackupJWTAuthRole])
	assert.Equal(t, constants.BackupAuthMethodJWT, envMap[constants.EnvBackupAuthMethod])

	volumeNames := make([]string, 0, len(job.Spec.Template.Spec.Volumes))
	for _, volume := range job.Spec.Template.Spec.Volumes {
		volumeNames = append(volumeNames, volume.Name)
	}
	assert.NotContains(t, volumeNames, restoreTokenVolumeName)

	mountNames := make([]string, 0, len(job.Spec.Template.Spec.Containers[0].VolumeMounts))
	for _, mount := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		mountNames = append(mountNames, mount.Name)
	}
	assert.NotContains(t, mountNames, restoreTokenVolumeName)
}
