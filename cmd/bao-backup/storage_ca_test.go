package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kubebao/openbao-operator/internal/adapter/storage"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	backupconfig "github.com/kubebao/openbao-operator/internal/service/backup"
)

const storageCASnapshot = "snapshot-bytes"

func TestStorageCustomCA_MountedFilesToSDKRequest(t *testing.T) {
	for _, provider := range []string{"s3-static", "s3-ambient", "gcs", "azure"} {
		for _, trust := range []string{"matching", "wrong", "absent"} {
			t.Run(provider+"/"+trust, func(t *testing.T) {
				var requests atomic.Int32
				handlerErrors := newHTTPHandlerErrors(t)
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					assert.Equal(t, http.MethodGet, r.Method)
					assert.Contains(t, r.URL.Path, "snapshot")
					if strings.HasPrefix(provider, "s3") {
						assert.Contains(t, r.Header.Get("Authorization"), "AWS4-HMAC-SHA256")
					} else if provider == "azure" {
						assert.Contains(t, r.Header.Get("Authorization"), "SharedKey")
					}
					w.Header().Set("Content-Length", strconv.Itoa(len(storageCASnapshot)))
					w.Header().Set("Content-Type", "application/octet-stream")
					w.Header().Set("ETag", `"snapshot-etag"`)
					w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
					w.Header().Set("x-ms-blob-type", "BlockBlob")
					w.Header().Set("x-goog-generation", "1")
					_, err := io.WriteString(w, storageCASnapshot)
					if err != nil {
						handlerErrors.Errorf("write snapshot response: %v", err)
					}
				})
				server, caCert := newStorageCATestServer(t, handler)
				credsPath := t.TempDir()
				if trust != "absent" {
					if trust == "wrong" {
						_, caCert = newStorageCATestServer(t, http.NotFoundHandler())
					}
					require.NoError(t, os.WriteFile(filepath.Join(credsPath, "caCert"), caCert, 0o600))
				}
				configureStorageCATestEnv(t, provider, server.URL, credsPath)
				cfg, err := backupconfig.LoadExecutorConfig()
				require.NoError(t, err)
				require.False(t, cfg.InsecureSkipVerify)
				if provider == "s3-ambient" {
					require.Nil(t, cfg.StorageCredentials, "CA-only trust must retain ambient credential discovery")
				}
				if trust == "absent" {
					require.Empty(t, cfg.StorageCACert)
				} else {
					require.Equal(t, caCert, cfg.StorageCACert)
				}
				// Restore executor configuration must retain the storage trust too.
				settings, err := resolveRestoreSettings(cfg)
				require.NoError(t, err)
				restoreCfg := buildRestoreExecutorConfig(cfg, settings)
				require.Equal(t, cfg.StorageCACert, restoreCfg.StorageCACert)
				storageCfg, err := buildStorageConfig(&restoreCfg)
				require.NoError(t, err)
				// The loopback HTTPS fixture is isolated to this test; production
				// configuration still enables request-time endpoint validation.
				require.True(t, storageCfg.ValidateEndpointRequests)
				storageCfg.ValidateEndpointRequests = false
				if storageCfg.S3 != nil {
					require.False(t, storageCfg.S3.InsecureSkipVerify)
					storageCfg.S3.EnsureExists = false
				} else if storageCfg.GCS != nil {
					require.False(t, storageCfg.GCS.InsecureSkipVerify)
				} else {
					require.False(t, storageCfg.Azure.InsecureSkipVerify)
				}
				store, err := storage.OpenBlobStore(t.Context(), storageCfg)
				require.NoError(t, err)
				t.Cleanup(func() { _ = store.Close() })
				requestCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				requestCtx = policy.WithRetryOptions(requestCtx, policy.RetryOptions{MaxRetries: -1})
				reader, err := store.Download(requestCtx, "snapshot")
				if trust != "matching" {
					require.Error(t, err, "untrusted endpoint certificates must be rejected")
					require.ErrorContains(t, err, "certificate")
					require.Zero(t, requests.Load(), "TLS must fail before an HTTP request reaches the server")
					return
				}
				require.NoError(t, err)
				data, err := io.ReadAll(reader)
				require.NoError(t, err)
				require.NoError(t, reader.Close())
				require.Equal(t, storageCASnapshot, string(data))
				require.Positive(t, requests.Load())
			})
		}
	}
}

func TestStorageCustomCA_PreservesAzureWorkloadIdentitySelection(t *testing.T) {
	path := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(path, "caCert"), []byte("test-ca"), 0o600))
	configureStorageCATestEnv(t, "azure", "https://storage.example.test", path)
	require.NoError(t, os.Remove(filepath.Join(path, "accountKey")))
	cfg, err := backupconfig.LoadExecutorConfig()
	require.NoError(t, err)
	storageCfg, err := buildStorageConfig(cfg)
	require.NoError(t, err)
	require.True(t, storageCfg.Azure.UseManagedIdentity)
	require.Empty(t, storageCfg.Azure.AccountKey)
	require.Empty(t, storageCfg.Azure.ConnectionString)
	require.Equal(t, []byte("test-ca"), storageCfg.Azure.CACert)
}

func configureStorageCATestEnv(t *testing.T, provider, endpoint, credsPath string) {
	t.Helper()
	t.Setenv(constants.EnvClusterNamespace, "test")
	t.Setenv(constants.EnvClusterName, "test")
	t.Setenv(constants.EnvClusterReplicas, "1")
	t.Setenv(constants.EnvBackupEndpoint, endpoint)
	t.Setenv(constants.EnvBackupBucket, "snapshots")
	t.Setenv(constants.EnvBackupRegion, "us-east-1")
	t.Setenv(constants.EnvBackupUsePathStyle, "true")
	t.Setenv(constants.EnvBackupCredentialsPath, credsPath)
	t.Setenv(constants.EnvBackupInsecureSkipVerify, "false")
	t.Setenv(constants.EnvTLSCAPath, "")
	t.Setenv(constants.EnvBackupAuthMethod, constants.BackupAuthMethodToken)
	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("test-token"), 0o600))
	t.Setenv(constants.EnvBackupTokenPath, tokenPath)
	t.Setenv(constants.EnvJWTTokenPath, filepath.Join(credsPath, "missing-jwt"))
	for _, name := range []string{constants.EnvRestoreEndpoint, constants.EnvRestoreBucket, constants.EnvRestoreRegion,
		constants.EnvRestoreUsePathStyle, constants.EnvBackupAzureContainer, constants.EnvBackupAzureStorageAccount,
		constants.EnvBackupGCSProject, constants.EnvBackupPartSize, constants.EnvBackupConcurrency,
		constants.EnvAWSRoleARN, constants.EnvAWSWebIdentityTokenFile,
	} {
		t.Setenv(name, "")
	}
	t.Setenv(constants.EnvRestoreKey, "snapshot")
	t.Setenv(constants.EnvRestoreUsePathStyle, "true")
	t.Setenv(constants.EnvRestoreForce, "false")
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	switch provider {
	case "s3-static":
		t.Setenv(constants.EnvBackupProvider, constants.StorageProviderS3)
		require.NoError(t, os.WriteFile(filepath.Join(credsPath, "accessKeyId"), []byte("static-access-key"), 0o600))
		secretPath := filepath.Join(credsPath, "secretAccessKey")
		require.NoError(t, os.WriteFile(secretPath, []byte("static-secret-key"), 0o600))
	case "s3-ambient":
		t.Setenv(constants.EnvBackupProvider, constants.StorageProviderS3)
		t.Setenv("AWS_ACCESS_KEY_ID", "ambient-access-key")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret-key")
		t.Setenv("AWS_SESSION_TOKEN", "")
	case "gcs":
		t.Setenv(constants.EnvBackupProvider, constants.StorageProviderGCS)
		// The HTTPS emulator path avoids an external token service while retaining
		// the actual Google storage client's request and certificate verification.
		t.Setenv(constants.EnvBackupEndpoint, endpoint+"/fake-gcs-server")
	case "azure":
		t.Setenv(constants.EnvBackupProvider, constants.StorageProviderAzure)
		t.Setenv(constants.EnvBackupAzureStorageAccount, "testaccount")
		key := base64.StdEncoding.EncodeToString([]byte("test-account-key"))
		require.NoError(t, os.WriteFile(filepath.Join(credsPath, "accountKey"), []byte(key), 0o600))
	}
}

func newStorageCATestServer(t *testing.T, handler http.Handler) (*httptest.Server, []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "storage-test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "storage-test"},
		NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	require.NoError(t, err)
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}
