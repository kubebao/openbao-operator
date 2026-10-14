package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/storage"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/blobstore"
)

func TestLoadStorageCredentials_PreservesRegionPresence(t *testing.T) {
	for _, fixture := range []string{"absent", "empty", "blank", "configured"} {
		t.Run(fixture, func(t *testing.T) {
			path := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(path, "accessKeyId"), []byte("file-access"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(path, "secretAccessKey"), []byte("file-secret"), 0o600))
			region := ""
			switch fixture {
			case "blank":
				region = " \n"
			case "configured":
				region = " eu-west-1\n"
			}
			if fixture != "absent" {
				require.NoError(t, os.WriteFile(filepath.Join(path, "region"), []byte(region), 0o600))
			}
			creds, err := loadStorageCredentials(path)
			require.NoError(t, err)
			want := ""
			if fixture == "configured" {
				want = "eu-west-1"
			}
			require.Equal(t, want, creds.Region)
			require.Equal(t, "file-access", creds.AccessKeyID)
			require.Equal(t, "file-secret", creds.SecretAccessKey)
		})
	}
}

func TestLoadS3StorageConfig_RegionOnlyRetainsAmbientAuthentication(t *testing.T) {
	path := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(path, "region"), []byte("eu-west-1"), 0o600))
	t.Setenv(constants.EnvBackupRegion, "us-east-1")
	t.Setenv(constants.EnvBackupUsePathStyle, "false")
	t.Setenv(constants.EnvBackupCredentialsPath, path)
	cfg := &ExecutorConfig{}
	require.NoError(t, loadS3StorageConfig(cfg))
	require.Equal(t, "eu-west-1", cfg.StorageRegion)
	require.Nil(t, cfg.StorageCredentials)
}

func TestConfigureS3StorageConfig_RegionPrecedence(t *testing.T) {
	for _, tc := range []struct{ name, target, secret, want string }{
		{"secret override", "us-east-1", "eu-west-1", "eu-west-1"},
		{"target fallback", "eu-central-1", "", "eu-central-1"},
		{"blank secret", "eu-central-1", " \n", "eu-central-1"},
		{"default", "", "", "us-east-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret := &corev1.Secret{Data: map[string][]byte{
				blobstore.SecretKeyAccessKeyID: []byte("file-access"), blobstore.SecretKeySecretAccessKey: []byte("file-secret"),
				blobstore.SecretKeySessionToken: []byte("session-token"), blobstore.SecretKeyRegion: []byte(tc.secret),
				blobstore.SecretKeyCACert: []byte("unchanged-ca"),
			}}
			cfg := &storage.Config{}
			configureS3StorageConfig(cfg, api.BackupTarget{Region: tc.target}, secret, false)
			require.Equal(t, tc.want, cfg.Region)
			require.Equal(t, tc.want, cfg.Credentials.Region)
			require.Equal(t, "file-access", cfg.Credentials.AccessKeyID)
			require.Equal(t, "file-secret", cfg.Credentials.SecretAccessKey)
			require.Equal(t, "session-token", cfg.Credentials.SessionToken)
			require.Equal(t, []byte("unchanged-ca"), cfg.Credentials.CACert)
			require.Equal(t, []byte(tc.secret), secret.Data[blobstore.SecretKeyRegion])
		})
	}
}

func TestConfigureS3StorageConfig_NestedRegionAndSecretPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withSecret bool
		region     *string
		want       string
	}{
		{name: "no Secret", want: "eu-central-1"},
		{name: "Secret without region", withSecret: true, want: "eu-central-1"},
		{name: "blank Secret region", withSecret: true, region: ptr.To(" \n\t"), want: "eu-central-1"},
		{name: "Secret region overrides nested region", withSecret: true, region: ptr.To(" ap-south-1\n"), want: "ap-south-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := api.BackupTarget{
				Provider: "s3", Region: "us-east-1", UsePathStyle: true,
				S3: &api.S3TargetConfig{Region: ptr.To("eu-central-1"), UsePathStyle: ptr.To(false)},
			}
			originalTarget := target.DeepCopy()
			var secret *corev1.Secret
			if tc.withSecret {
				secret = &corev1.Secret{Data: map[string][]byte{
					blobstore.SecretKeyAccessKeyID: []byte("file-access"), blobstore.SecretKeySecretAccessKey: []byte("file-secret"),
					blobstore.SecretKeySessionToken: []byte("session-token"), blobstore.SecretKeyCACert: []byte("unchanged-ca"),
				}}
				if tc.region != nil {
					secret.Data[blobstore.SecretKeyRegion] = []byte(*tc.region)
				}
			}
			originalSecret := secret.DeepCopy()
			cfg := &storage.Config{}
			configureS3StorageConfig(cfg, target, secret, false)
			require.Equal(t, tc.want, cfg.Region, "the signer uses Secret region, then the effective nested target region")
			require.Equal(t, tc.want, cfg.Credentials.Region)
			require.False(t, cfg.S3.UsePathStyle, "nested false overrides the legacy addressing default")
			if tc.withSecret {
				require.Equal(t, "file-access", cfg.Credentials.AccessKeyID)
				require.Equal(t, "file-secret", cfg.Credentials.SecretAccessKey)
				require.Equal(t, "session-token", cfg.Credentials.SessionToken)
				require.Equal(t, []byte("unchanged-ca"), cfg.Credentials.CACert)
			} else {
				require.Empty(t, cfg.Credentials.AccessKeyID)
				require.Empty(t, cfg.Credentials.SecretAccessKey)
			}
			require.Equal(t, originalTarget, &target, "controller configuration preserves the API target")
			require.Equal(t, originalSecret, secret, "controller configuration preserves the Secret")
		})
	}
}
