package security

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/sigstore/cosign/v3/pkg/oci/static"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/kubebao/openbao-operator/internal/port/imageverify"
)

func TestImageVerifierAuthenticatedRegistry(t *testing.T) {
	for _, tt := range []struct {
		name             string
		secretType       corev1.SecretType
		urlKey           bool
		scope            string
		password         string
		wantErr          bool
		wantAuthHeader   bool
		wildcardFallback bool
	}{
		{
			name: "dockerconfigjson scoped URL", secretType: corev1.SecretTypeDockerConfigJson,
			urlKey: true, scope: "/v1/team/", password: "test-password", wantAuthHeader: true,
		},
		{
			name: "dockercfg scoped URL", secretType: corev1.SecretTypeDockercfg,
			urlKey: true, scope: "/v2/team/", password: "test-password", wantAuthHeader: true,
		},
		{
			name: "dockercfg host port", secretType: corev1.SecretTypeDockercfg,
			password: "test-password", wantAuthHeader: true,
		},
		{
			name: "exact hostname before wildcard credentials", secretType: corev1.SecretTypeDockerConfigJson,
			password: "test-password", wantAuthHeader: true, wildcardFallback: true,
		},
		{
			name: "unmatched path sends no credentials", secretType: corev1.SecretTypeDockerConfigJson,
			urlKey: true, scope: "/other/", password: "test-password", wantErr: true,
		},
		{
			name: "incorrect password fails", secretType: corev1.SecretTypeDockerConfigJson,
			password: "incorrect-password", wantErr: true, wantAuthHeader: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			registry := newAuthenticatedImageRegistry(t)
			registryKey := registry.server.URL
			if !tt.urlKey {
				registryKey = strings.TrimPrefix(registryKey, "http://")
			}
			auths := map[string]map[string]string{
				registryKey + tt.scope: {"auth": base64.StdEncoding.EncodeToString([]byte("reader:" + tt.password))},
			}
			if tt.wildcardFallback {
				registryURL, err := url.Parse(registry.server.URL)
				require.NoError(t, err)
				wildcardKey := registryURL.Hostname() + "*:" + registryURL.Port() + tt.scope
				auths[wildcardKey] = map[string]string{"username": "reader", "password": "incorrect-password"}
			}
			secret := newImagePullSecret(t, "tenant-a", "registry", tt.secretType, auths)
			verifier := NewImageVerifier(logr.Discard(), newTrustedRootTestClient(t, secret), nil)
			result, err := verifier.Verify(context.Background(), registry.imageRef, imageverify.VerifyConfig{
				PublicKey: registry.publicKey, IgnoreTlog: true, Namespace: secret.Namespace,
				ImagePullSecrets: []corev1.LocalObjectReference{{Name: secret.Name}},
			})
			if tt.wantErr {
				require.Error(t, err)
				require.Empty(t, result)
			} else {
				require.NoError(t, err)
				require.Equal(t, registry.digestRef, result)
				require.Positive(t, registry.tagReads.Load(), "tag resolution must authenticate")
				require.Positive(t, registry.signatureReads.Load(), "signature retrieval must authenticate")
			}
			require.Equal(t, tt.wantAuthHeader, registry.authHeader.Load())
		})
	}
}

type authenticatedImageRegistry struct {
	server         *httptest.Server
	imageRef       string
	digestRef      string
	publicKey      string
	tagReads       atomic.Int32
	signatureReads atomic.Int32
	authHeader     atomic.Bool
}

func newAuthenticatedImageRegistry(t *testing.T) *authenticatedImageRegistry {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	imageManifest, err := empty.Image.RawManifest()
	require.NoError(t, err)
	imageDigest, err := empty.Image.Digest()
	require.NoError(t, err)
	imageMediaType, err := empty.Image.MediaType()
	require.NoError(t, err)
	payload := []byte(fmt.Sprintf(`{"critical":{"image":{"docker-manifest-digest":%q}}}`, imageDigest.String()))
	payloadHash := sha256.Sum256(payload)
	signature, err := ecdsa.SignASN1(rand.Reader, key, payloadHash[:])
	require.NoError(t, err)
	sig, err := static.NewSignature(payload, base64.StdEncoding.EncodeToString(signature))
	require.NoError(t, err)
	annotations, err := sig.Annotations()
	require.NoError(t, err)
	signatureImage, err := mutate.Append(empty.Image, mutate.Addendum{Layer: sig, Annotations: annotations})
	require.NoError(t, err)
	signatureManifest, err := signatureImage.RawManifest()
	require.NoError(t, err)
	signatureDigest, err := signatureImage.Digest()
	require.NoError(t, err)
	signatureMediaType, err := signatureImage.MediaType()
	require.NoError(t, err)
	payloadDigest, err := sig.Digest()
	require.NoError(t, err)
	fixture := &authenticatedImageRegistry{
		publicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicKeyDER})),
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if r.Header.Get("Authorization") != "" {
			fixture.authHeader.Store(true)
		}
		if !ok || user != "reader" || password != "test-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="image-registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/team/image/manifests/latest":
			fixture.tagReads.Add(1)
			w.Header().Set("Content-Type", string(imageMediaType))
			w.Header().Set("Content-Length", strconv.Itoa(len(imageManifest)))
			w.Header().Set("Docker-Content-Digest", imageDigest.String())
			_, _ = w.Write(imageManifest)
		case "/v2/team/image/manifests/sha256-" + imageDigest.Hex + ".sig":
			fixture.signatureReads.Add(1)
			w.Header().Set("Content-Type", string(signatureMediaType))
			w.Header().Set("Docker-Content-Digest", signatureDigest.String())
			_, _ = w.Write(signatureManifest)
		case "/v2/team/image/blobs/" + payloadDigest.String():
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	repository := strings.TrimPrefix(fixture.server.URL, "http://") + "/team/image"
	fixture.imageRef = repository + ":latest"
	fixture.digestRef = repository + "@" + imageDigest.String()
	return fixture
}
