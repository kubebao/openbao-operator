package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestLoadStorageCACert(t *testing.T) {
	for _, fixture := range []string{"absent-directory", "absent-file", "certificate", "unreadable-file", "invalid-path"} {
		t.Run(fixture, func(t *testing.T) {
			path := t.TempDir()
			caPath := filepath.Join(path, "caCert")
			switch fixture {
			case "absent-directory":
				path = filepath.Join(path, "missing")
			case "certificate":
				require.NoError(t, os.WriteFile(caPath, []byte("test-ca"), 0o600))
			case "unreadable-file":
				require.NoError(t, os.Mkdir(caPath, 0o700))
			case "invalid-path":
				path = "../../outside"
			}
			ca, err := loadStorageCACert(path)
			if fixture == "unreadable-file" || fixture == "invalid-path" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if fixture == "certificate" {
				require.Equal(t, []byte("test-ca"), ca)
			} else {
				require.Nil(t, ca)
			}
		})
	}
}

func TestLoadStorageConfig_CAOnlyDoesNotSelectStaticS3Credentials(t *testing.T) {
	path := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(path, "caCert"), []byte("test-ca"), 0o600))
	t.Setenv(constants.EnvBackupProvider, constants.StorageProviderS3)
	t.Setenv(constants.EnvBackupBucket, "snapshots")
	t.Setenv(constants.EnvBackupCredentialsPath, path)
	t.Setenv(constants.EnvBackupRegion, "eu-west-1")
	t.Setenv(constants.EnvBackupUsePathStyle, "false")
	cfg := &ExecutorConfig{}
	require.NoError(t, loadStorageConfig(cfg))
	require.Equal(t, []byte("test-ca"), cfg.StorageCACert)
	require.Nil(t, cfg.StorageCredentials)
	require.Equal(t, "eu-west-1", cfg.BackupRegion, "storage trust must not change region selection")

	require.NoError(t, os.WriteFile(filepath.Join(path, "accessKeyId"), []byte("incomplete"), 0o600))
	require.ErrorContains(t, loadStorageConfig(&ExecutorConfig{}), "both accessKeyId and secretAccessKey must be provided")
}
