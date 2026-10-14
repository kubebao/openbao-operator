package backup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/storage"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/blobstore"
)

func TestOpenBackupStorageClientDestinationMatchesJobEnv(t *testing.T) {
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
			cluster := newTestClusterWithBackup("storage-target", "default")
			cluster.Spec.Backup.Target = tt.target
			var captured storage.Config
			originalOpen := openBlobStoreFn
			openBlobStoreFn = func(_ context.Context, cfg storage.Config) (blobstore.BlobStore, error) {
				captured = cfg
				return &fakeBlobStore{}, nil
			}
			t.Cleanup(func() { openBlobStoreFn = originalOpen })

			store, err := (&Manager{}).openBackupStorageClient(context.Background(), cluster, false)
			require.NoError(t, err)
			require.NoError(t, store.Close())
			env := make(map[string]string)
			for _, value := range BuildEnvVars(cluster, JobOptions{}) {
				env[value.Name] = value.Value
			}
			assert.Equal(t, env[constants.EnvBackupProvider], string(captured.Provider))
			assert.Equal(t, tt.wantBucket, captured.Bucket)
			assert.Equal(t, captured.Bucket, env[constants.EnvBackupBucket])
			assert.Equal(t, tt.target.Endpoint, captured.Endpoint)
			assert.Equal(t, captured.Endpoint, env[constants.EnvBackupEndpoint])
			assert.Equal(t, tt.wantRegion, captured.Region)
			assert.Equal(t, captured.Region, env[constants.EnvBackupRegion])
			assert.False(t, captured.EnsureExists, "retention must not acquire bucket creation behavior")
			assert.True(t, captured.ValidateEndpointRequests)
			if captured.S3 != nil {
				assert.False(t, captured.S3.EnsureExists)
				assert.Equal(t, tt.target.UsePathStyle, captured.S3.UsePathStyle)
			}
		})
	}
}

func TestConfigureAzureStorageConfig_UsesManagedIdentityWithoutStaticCredentials(t *testing.T) {
	storageConfig := &storage.Config{
		Bucket: "backups",
	}

	configureAzureStorageConfig(storageConfig, openbaov1alpha1.BackupTarget{
		Provider: "azure",
		Bucket:   "backups",
		Azure: &openbaov1alpha1.AzureTargetConfig{
			StorageAccount: "storageaccount",
		},
	}, nil)

	require.NotNil(t, storageConfig.Azure)
	assert.True(t, storageConfig.Azure.UseManagedIdentity)
	assert.Equal(t, "storageaccount", storageConfig.Azure.StorageAccount)
}

func TestConfigureAzureStorageConfig_PrefersStaticCredentialsWhenProvided(t *testing.T) {
	storageConfig := &storage.Config{
		Bucket: "backups",
	}
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"accountKey": []byte("secret"),
		},
	}

	configureAzureStorageConfig(storageConfig, openbaov1alpha1.BackupTarget{
		Provider: "azure",
		Bucket:   "backups",
		Azure: &openbaov1alpha1.AzureTargetConfig{
			StorageAccount: "storageaccount",
		},
	}, secret)

	require.NotNil(t, storageConfig.Azure)
	assert.False(t, storageConfig.Azure.UseManagedIdentity)
	assert.Equal(t, "secret", storageConfig.Azure.AccountKey)
}
