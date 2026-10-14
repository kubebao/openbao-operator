package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/kubebao/openbao-operator/internal/adapter/storage"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/blobstore"
	backupconfig "github.com/kubebao/openbao-operator/internal/service/backup"
)

func TestS3Region_MountedFilesToSigningScope(t *testing.T) {
	for _, tc := range []struct {
		name, target, restore, backupScope, restoreScope string
		file                                             *string
	}{
		{"secret override", "eu-west-1", "ap-south-1", "eu-central-1", "eu-central-1", ptr.To(" eu-central-1\n")},
		{"absent secret", "eu-west-1", "", "eu-west-1", "eu-west-1", nil},
		{"empty secret", "eu-west-1", "", "eu-west-1", "eu-west-1", ptr.To("")},
		{"blank secret", "eu-west-1", "ap-south-1", "eu-west-1", "ap-south-1", ptr.To(" \n")},
		{"default", "", "", "us-east-1", "us-east-1", nil},
		{"secret without target", "", "", "eu-central-1", "eu-central-1", ptr.To("eu-central-1")},
	} {
		for _, auth := range []string{"static", "ambient"} {
			for _, flow := range []string{"backup", "restore"} {
				t.Run(tc.name+"/"+auth+"/"+flow, func(t *testing.T) {
					type signedRequest struct{ authorization, token string }
					requests := make(chan signedRequest, 1)
					handlerErrors := newHTTPHandlerErrors(t)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						record := signedRequest{r.Header.Get("Authorization"), r.Header.Get("X-Amz-Security-Token")}
						select {
						case requests <- record:
						default:
						}
						w.Header().Set("Content-Length", "8")
						if _, err := io.WriteString(w, "snapshot"); err != nil {
							handlerErrors.Errorf("write snapshot response: %v", err)
						}
					}))
					t.Cleanup(server.Close)
					path := t.TempDir()
					if tc.file != nil {
						require.NoError(t, os.WriteFile(filepath.Join(path, "region"), []byte(*tc.file), 0o600))
					}
					if auth == "static" {
						for name, value := range map[string]string{
							"accessKeyId": "file-access", "secretAccessKey": "file-secret",
							"sessionToken": "file-session",
						} {
							require.NoError(t, os.WriteFile(filepath.Join(path, name), []byte(value), 0o600))
						}
					}
					configureStorageRegionTestEnv(t, server.URL, path, tc.target, tc.restore)
					cfg, err := backupconfig.LoadExecutorConfig()
					require.NoError(t, err)
					originalRegion := ""
					if cfg.StorageCredentials != nil {
						originalRegion = cfg.StorageCredentials.Region
					} else {
						require.Equal(t, "ambient", auth)
					}
					selected := cfg
					wantRegion := tc.backupScope
					if flow == "restore" {
						settings, err := resolveRestoreSettings(cfg)
						require.NoError(t, err)
						restored := buildRestoreExecutorConfig(cfg, settings)
						selected = &restored
						wantRegion = tc.restoreScope
					}
					storageCfg, err := buildStorageConfig(selected)
					require.NoError(t, err)
					require.Equal(t, wantRegion, storageCfg.Region)
					require.Equal(t, wantRegion, storageCfg.Credentials.Region)
					if cfg.StorageCredentials != nil {
						require.Equal(t, originalRegion, cfg.StorageCredentials.Region,
							"resolution must preserve the loaded credential object")
					} else {
						require.Nil(t, selected.StorageCredentials,
							"region-only configuration must retain ambient authentication")
					}
					// The loopback fixture requires an in-test exception. The builder
					// still enables the production request-time endpoint guard.
					require.True(t, storageCfg.ValidateEndpointRequests)
					storageCfg.ValidateEndpointRequests = false
					storageCfg.S3.EnsureExists = false
					store, err := storage.OpenBlobStore(t.Context(), storageCfg)
					require.NoError(t, err)
					t.Cleanup(func() { _ = store.Close() })
					requestCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					reader, err := store.Download(requestCtx, "snapshot")
					require.NoError(t, err)
					data, err := io.ReadAll(reader)
					require.NoError(t, err)
					require.NoError(t, reader.Close())
					require.Equal(t, "snapshot", string(data))
					var request signedRequest
					select {
					case request = <-requests:
					default:
						t.Fatal("the SDK did not issue a storage request")
					}
					matches := regexp.MustCompile(`Credential=([^, ]+)`).FindStringSubmatch(request.authorization)
					require.Len(t, matches, 2)
					scope := strings.Split(matches[1], "/")
					require.Len(t, scope, 5)
					require.Equal(t, wantRegion, scope[2])
					require.Equal(t, "s3", scope[3])
					require.Equal(t, "aws4_request", scope[4])
					if auth == "static" {
						require.Equal(t, "file-access", scope[0])
						require.Equal(t, "file-session", request.token)
					} else {
						require.Equal(t, "ambient-access", scope[0])
						require.Equal(t, "ambient-session", request.token)
					}
				})
			}
		}
	}
}

func TestBuildStorageConfig_RegionResolutionPreservesCredentials(t *testing.T) {
	creds := &blobstore.Credentials{
		AccessKeyID: "access", SecretAccessKey: "secret", SessionToken: "session", CACert: []byte("ca"),
	}
	cfg := &backupconfig.ExecutorConfig{BackupProvider: constants.StorageProviderS3,
		BackupRegion: "eu-west-1", StorageCredentials: creds,
	}
	restored := buildRestoreExecutorConfig(cfg, restoreSettings{region: "ap-south-1"})
	got, err := buildStorageConfig(&restored)
	require.NoError(t, err)
	require.Equal(t, "ap-south-1", got.Region)
	require.Equal(t, "ap-south-1", got.Credentials.Region)
	require.Equal(t, "access", got.Credentials.AccessKeyID)
	require.Equal(t, "secret", got.Credentials.SecretAccessKey)
	require.Equal(t, "session", got.Credentials.SessionToken)
	require.Equal(t, []byte("ca"), got.Credentials.CACert)
	require.Empty(t, creds.Region)
	creds.Region = "eu-central-1"
	got, err = buildStorageConfig(cfg)
	require.NoError(t, err)
	require.Equal(t, "eu-central-1", got.Region, "manual credential region remains an override")
	require.Equal(t, "eu-central-1", creds.Region)
}

func configureStorageRegionTestEnv(t *testing.T, endpoint, path, targetRegion, restoreRegion string) {
	t.Helper()
	for name, value := range map[string]string{
		constants.EnvClusterNamespace: "test", constants.EnvClusterName: "test", constants.EnvClusterReplicas: "1",
		constants.EnvBackupProvider: constants.StorageProviderS3, constants.EnvBackupEndpoint: endpoint,
		constants.EnvBackupBucket: "snapshots", constants.EnvBackupRegion: targetRegion,
		constants.EnvBackupUsePathStyle: "true", constants.EnvBackupCredentialsPath: path,
		constants.EnvBackupInsecureSkipVerify: "false", constants.EnvTLSCAPath: "",
		constants.EnvBackupAuthMethod: constants.BackupAuthMethodToken,
		constants.EnvJWTTokenPath:     filepath.Join(path, "missing-jwt"),
		constants.EnvRestoreKey:       "snapshot", constants.EnvRestoreBucket: "", constants.EnvRestoreEndpoint: "",
		constants.EnvRestoreRegion: restoreRegion, constants.EnvRestoreUsePathStyle: "true",
		constants.EnvRestoreForce: "false",
		constants.EnvAWSRoleARN:   "", constants.EnvAWSWebIdentityTokenFile: "",
		constants.EnvBackupPartSize: "", constants.EnvBackupConcurrency: "",
		"AWS_ACCESS_KEY_ID": "ambient-access", "AWS_SECRET_ACCESS_KEY": "ambient-secret",
		"AWS_SESSION_TOKEN": "ambient-session", "AWS_MAX_ATTEMPTS": "1",
	} {
		t.Setenv(name, value)
	}
	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("test-token"), 0o600))
	t.Setenv(constants.EnvBackupTokenPath, tokenPath)
}
