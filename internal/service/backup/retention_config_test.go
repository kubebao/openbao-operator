package backup

import (
	"testing"
	"time"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/stretchr/testify/require"
)

func TestRetentionLoaderRequiresNoOpenBaoCredentials(t *testing.T) {
	for _, provider := range []string{"s3", "gcs", "azure"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv(constants.EnvClusterNamespace, "test")
			t.Setenv(constants.EnvClusterName, "bao")
			t.Setenv(constants.EnvBackupProvider, provider)
			t.Setenv(constants.EnvBackupEndpoint, "https://storage.example")
			t.Setenv(constants.EnvBackupBucket, "snapshots")
			t.Setenv(constants.EnvBackupPathPrefix, "backups")
			t.Setenv(constants.EnvBackupAzureStorageAccount, "account")
			t.Setenv(constants.EnvBackupCredentialsPath, t.TempDir())
			t.Setenv(constants.EnvBackupAuthMethod, "")
			t.Setenv(constants.EnvBackupJWTAuthRole, "")
			t.Setenv(constants.EnvBackupTokenPath, "/absent")
			t.Setenv(constants.EnvTLSCAPath, "/absent")
			t.Setenv(EnvRetentionPrefix, "backups/test/bao/")
			t.Setenv(EnvRetentionKey, "backups/test/bao/2026-10-14T10-00-00Z-aaaaaaaa.snap")
			t.Setenv(EnvRetentionMaxCount, "1")
			t.Setenv(EnvRetentionMaxAge, "168h")
			t.Setenv(EnvRetentionSourceJobUID, "source-uid")
			deadline := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			t.Setenv(EnvRetentionDeadline, deadline.Format(time.RFC3339))
			cfg, err := LoadRetentionExecutorConfig()
			require.NoError(t, err)
			require.Equal(t, deadline, cfg.Deadline, "an expired deadline is never extended")
			require.Empty(t, cfg.Storage.AuthMethod)
			require.Empty(t, cfg.Storage.OpenBaoToken)
			t.Setenv(EnvRetentionPrefix, "backups/test/another/")
			_, err = LoadRetentionExecutorConfig()
			require.ErrorContains(t, err, "cluster boundary")
		})
	}
}
