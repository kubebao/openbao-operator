package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	backupconfig "github.com/kubebao/openbao-operator/internal/service/backup"
)

func TestStorageDestinationRoundTripFromBackupJob(t *testing.T) {
	tests := []struct {
		name       string
		target     openbaov1alpha1.BackupTarget
		wantBucket string
		wantRegion string
	}{
		{
			name: "default S3", target: openbaov1alpha1.BackupTarget{Bucket: "snapshots"},
			wantBucket: "snapshots", wantRegion: constants.DefaultS3Region,
		},
		{
			name: "configured S3", target: openbaov1alpha1.BackupTarget{
				Provider: "s3", Bucket: "snapshots", Region: "eu-west-1", UsePathStyle: true,
				RoleARN: "arn:aws:iam::123456789012:role/backup",
			},
			wantBucket: "snapshots", wantRegion: "eu-west-1",
		},
		{
			name: "GCS", target: openbaov1alpha1.BackupTarget{
				Provider: "gcs", Bucket: "snapshots", GCS: &openbaov1alpha1.GCSTargetConfig{Project: "test-project"},
			},
			wantBucket: "snapshots",
		},
		{
			name: "Azure bucket fallback", target: openbaov1alpha1.BackupTarget{
				Provider: "azure", Bucket: "snapshots", Azure: &openbaov1alpha1.AzureTargetConfig{StorageAccount: "account"},
			},
			wantBucket: "snapshots",
		},
		{
			name: "Azure container alias", target: openbaov1alpha1.BackupTarget{
				Provider: "azure", Bucket: "legacy-bucket",
				Azure: &openbaov1alpha1.AzureTargetConfig{StorageAccount: "account", Container: "selected-container"},
			},
			wantBucket: "selected-container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.target.Endpoint = "https://storage.example.test/service/path/"
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "storage-target", Namespace: "default"},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Replicas: 3,
					Backup: &openbaov1alpha1.BackupSchedule{
						Target: tt.target, TokenSecretRef: &corev1.LocalObjectReference{Name: "backup-token"},
					},
				},
			}
			env := backupconfig.BuildEnvVars(cluster, backupconfig.JobOptions{})
			setStorageDestinationTestEnv(t, env)
			cfg, err := backupconfig.LoadExecutorConfig()
			require.NoError(t, err)
			got, err := buildStorageConfig(cfg)
			require.NoError(t, err)
			assert.Equal(t, cfg.BackupProvider, string(got.Provider))
			assert.Equal(t, tt.wantBucket, got.Bucket)
			assert.Equal(t, tt.target.Endpoint, got.Endpoint)
			assert.Equal(t, tt.wantRegion, got.Region)
			assert.True(t, got.ValidateEndpointRequests)
			assert.False(t, got.EnsureExists)
			if got.S3 != nil {
				assert.Equal(t, tt.target.UsePathStyle, got.S3.UsePathStyle)
				assert.True(t, got.S3.EnsureExists, "preserve the executor's existing S3 creation behavior")
			}
			if got.GCS != nil {
				assert.Equal(t, tt.target.GCS.Project, got.GCS.Project)
			}
			if got.Azure != nil {
				assert.Equal(t, tt.target.Azure.StorageAccount, got.Azure.StorageAccount)
				assert.True(t, got.Azure.UseManagedIdentity)
				assert.Equal(t, tt.target.Azure.Container, cfg.AzureContainer, "retain the legacy alias environment")
			}
			if tt.target.RoleARN != "" {
				assert.Equal(t, tt.target.RoleARN, os.Getenv(constants.EnvAWSRoleARN))
				assert.NotEmpty(t, os.Getenv(constants.EnvAWSWebIdentityTokenFile))
			}
		})
	}
}

func setStorageDestinationTestEnv(t *testing.T, env []corev1.EnvVar) {
	t.Helper()
	for _, name := range []string{
		constants.EnvBackupProvider, constants.EnvBackupRegion, constants.EnvBackupUsePathStyle,
		constants.EnvBackupInsecureSkipVerify, constants.EnvBackupGCSProject,
		constants.EnvBackupAzureStorageAccount, constants.EnvBackupAzureContainer,
		constants.EnvBackupJWTAuthRole, constants.EnvBackupPartSize, constants.EnvBackupConcurrency,
	} {
		t.Setenv(name, "")
	}
	for _, value := range env {
		t.Setenv(value.Name, value.Value)
	}
	credentialsPath := t.TempDir()
	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("test-token"), 0o600))
	t.Setenv(constants.EnvBackupCredentialsPath, credentialsPath)
	t.Setenv(constants.EnvBackupTokenPath, tokenPath)
	t.Setenv(constants.EnvJWTTokenPath, filepath.Join(credentialsPath, "absent-jwt"))
	t.Setenv(constants.EnvTLSCAPath, "")
}

func TestRestoreStorageDestinationOverrides(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		azureContainer string
		wantBucket     string
	}{
		{name: "S3", provider: "s3", wantBucket: "restore-bucket"},
		{name: "GCS", provider: "gcs", wantBucket: "restore-bucket"},
		{name: "Azure bucket fallback", provider: "azure", wantBucket: "restore-bucket"},
		{
			name: "Azure legacy alias retains precedence", provider: "azure",
			azureContainer: "selected-container", wantBucket: "selected-container",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &backupconfig.ExecutorConfig{
				BackupProvider: tt.provider, BackupBucket: "backup-bucket",
				BackupEndpoint: "https://original.example.test", BackupRegion: "eu-west-1",
				AzureStorageAccount: "account", AzureContainer: tt.azureContainer,
			}
			t.Setenv(constants.EnvRestoreKey, "backup.snap")
			t.Setenv(constants.EnvRestoreBucket, "restore-bucket")
			t.Setenv(constants.EnvRestoreEndpoint, "https://restore.example.test/service/path/")
			t.Setenv(constants.EnvRestoreRegion, "eu-north-1")
			t.Setenv(constants.EnvRestoreUsePathStyle, "true")
			t.Setenv(constants.EnvRestoreForce, "false")
			settings, err := resolveRestoreSettings(cfg)
			require.NoError(t, err)
			restoreCfg := buildRestoreExecutorConfig(cfg, settings)
			got, err := buildStorageConfig(&restoreCfg)
			require.NoError(t, err)
			assert.Equal(t, tt.wantBucket, got.Bucket)
			assert.Equal(t, "https://restore.example.test/service/path/", got.Endpoint)
			if got.S3 != nil {
				assert.Equal(t, "eu-north-1", got.Region)
				assert.True(t, got.S3.UsePathStyle)
			}
			assert.Equal(t, "backup-bucket", cfg.BackupBucket, "restore overrides must preserve the backup destination")
			assert.Equal(t, "https://original.example.test", cfg.BackupEndpoint)
		})
	}
}
