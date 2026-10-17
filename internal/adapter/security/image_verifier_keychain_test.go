package security

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebao/openbao-operator/internal/port/imageverify"
)

func TestImageVerifierKeychainRegistryScopes(t *testing.T) {
	for _, secretType := range []corev1.SecretType{corev1.SecretTypeDockerConfigJson, corev1.SecretTypeDockercfg} {
		t.Run(string(secretType), func(t *testing.T) {
			for _, tt := range []struct {
				key       string
				target    string
				anonymous bool
			}{
				{key: "private.example", target: "private.example/team/image"},
				{key: "https://private.example", target: "private.example/team/image"},
				{key: "http://private.example/v1/", target: "private.example/team/image"},
				{key: "private.example/v2/", target: "private.example/team/image"},
				{key: "private.example:5443", target: "private.example:5443/team/image"},
				{key: "private.example:5443", target: "private.example:5444/team/image", anonymous: true},
				{key: "private.example:5443", target: "private.example/team/image", anonymous: true},
				{key: "https://[::1]:5443/team/", target: "[::1]:5443/team/image"},
				{key: "https://[::1]:5443/team/", target: "[::1]:5444/team/image", anonymous: true},
				{key: "private.example", target: "private.example.attacker.test/team/image", anonymous: true},
				{key: "private.example/team/", target: "private.example/team/image"},
				{key: "private.example/team/", target: "private.example/other/image", anonymous: true},
				{key: "private.example/team/", target: "private.example/team-other/image", anonymous: true},
				{key: "private.example/team", target: "private.example/team-other/image"},
				{key: "private.example/v2/team/", target: "private.example/team/image"},
				{key: "private.example/v1/v2/team/", target: "private.example/v2/team/image"},
				{key: "private.example/v1/v2/team/", target: "private.example/team/image", anonymous: true},
				{key: "private.example/v2/v1/team/", target: "private.example/v1/team/image"},
				{key: "private.example/v2/v1/team/", target: "private.example/team/image", anonymous: true},
				{key: "*.example.test/team/", target: "a.example.test/team/image"},
				{key: "*.example.test/team/", target: "a.b.example.test/team/image", anonymous: true},
				{key: "*.example.test/team/", target: "example.test/team/image", anonymous: true},
				{key: "https://index.docker.io/v1/", target: "docker.io/library/alpine"},
				{key: "docker.io", target: "index.docker.io/library/alpine"},
				{key: "index.docker.io", target: "registry-1.docker.io/library/alpine"},
				{key: "registry-1.docker.io", target: "docker.io/library/alpine"},
				{key: "https://docker.io/v1/team/", target: "registry-1.docker.io/team/image"},
				{key: "https://index.docker.io/v1/team/", target: "docker.io/other/image", anonymous: true},
				{key: "https://index.docker.io/v1/", target: "attacker.docker.io/library/alpine", anonymous: true},
				{key: "docker.io:5443", target: "index.docker.io:5443/library/alpine", anonymous: true},
			} {
				t.Run(tt.key+"->"+tt.target, func(t *testing.T) {
					secret := newImagePullSecret(t, "tenant-a", "registry", secretType,
						map[string]map[string]string{tt.key: {"username": "reader", "password": "test-password"}})
					verifier := NewImageVerifier(logr.Discard(), newTrustedRootTestClient(t, secret), nil)
					keychain, err := verifier.buildKeychain(context.Background(), []corev1.LocalObjectReference{{Name: secret.Name}}, secret.Namespace)
					require.NoError(t, err)
					require.NotNil(t, keychain)
					resource, err := name.NewRepository(tt.target)
					require.NoError(t, err)
					auth, err := keychain.Resolve(resource)
					require.NoError(t, err)
					if tt.anonymous {
						require.Equal(t, authn.Anonymous, auth)
						return
					}
					config, err := auth.Authorization()
					require.NoError(t, err)
					require.Equal(t, "reader", config.Username)
					require.Equal(t, "test-password", config.Password)
				})
			}
		})
	}
}

func TestImageVerifierKeychainAuthFormats(t *testing.T) {
	for _, tt := range []struct {
		name     string
		config   map[string]string
		username string
		password string
	}{
		{
			name: "padded auth", config: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("reader:password!"))},
			username: "reader", password: "password!",
		},
		{
			name: "unpadded auth", config: map[string]string{"auth": base64.RawStdEncoding.EncodeToString([]byte("reader:password!"))},
			username: "reader", password: "password!",
		},
		{
			name: "password contains colon", config: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("reader:p:a:ss"))},
			username: "reader", password: "p:a:ss",
		},
		{
			name: "username password", config: map[string]string{"username": "reader", "password": "password"},
			username: "reader", password: "password",
		},
		{
			name: "empty password", config: map[string]string{"username": "reader", "password": ""},
			username: "reader",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			secret := newImagePullSecret(t, "tenant-a", "registry", corev1.SecretTypeDockerConfigJson,
				map[string]map[string]string{"private.example": tt.config})
			verifier := NewImageVerifier(logr.Discard(), newTrustedRootTestClient(t, secret), nil)
			keychain, err := verifier.buildKeychain(context.Background(), []corev1.LocalObjectReference{{Name: secret.Name}}, secret.Namespace)
			require.NoError(t, err)
			resource, err := name.NewRepository("private.example/team/image")
			require.NoError(t, err)
			auth, err := keychain.Resolve(resource)
			require.NoError(t, err)
			config, err := auth.Authorization()
			require.NoError(t, err)
			require.Equal(t, tt.username, config.Username)
			require.Equal(t, tt.password, config.Password)
		})
	}
}

func TestImageVerifierKeychainScopePrecedence(t *testing.T) {
	for _, secretType := range []corev1.SecretType{corev1.SecretTypeDockerConfigJson, corev1.SecretTypeDockercfg} {
		t.Run(string(secretType), func(t *testing.T) {
			for _, tt := range []struct {
				name      string
				preferred string
				fallback  string
				target    string
			}{
				{
					name:      "exact hostname before trailing wildcard",
					preferred: "foo.example.com", fallback: "foo.example.com*", target: "foo.example.com/team/image",
				},
				{
					name:      "exact hostname with port and equal path",
					preferred: "foo.example.com:5443/team/", fallback: "foo.example.com*:5443/team/",
					target: "foo.example.com:5443/team/image",
				},
				{
					name:      "longer wildcard path before exact hostname",
					preferred: "*.example.com/team/", fallback: "foo.example.com", target: "foo.example.com/team/image",
				},
				{
					name:      "longer exact path before wildcard hostname",
					preferred: "foo.example.com/team/sub/", fallback: "foo.example.com*/team/",
					target: "foo.example.com/team/sub/image",
				},
				{
					name:      "wildcard ties retain deterministic key ordering",
					preferred: "foo*.example.com/team/", fallback: "*.example.com/team/", target: "foo.example.com/team/image",
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					secret := newImagePullSecret(t, "tenant-a", "registry", secretType, map[string]map[string]string{
						tt.preferred: {"username": "preferred", "password": "password"},
						tt.fallback:  {"username": "fallback", "password": "password"},
					})
					verifier := NewImageVerifier(logr.Discard(), newTrustedRootTestClient(t, secret), nil)
					keychain, err := verifier.buildKeychain(context.Background(), []corev1.LocalObjectReference{{Name: secret.Name}}, secret.Namespace)
					require.NoError(t, err)
					resource, err := name.NewRepository(tt.target)
					require.NoError(t, err)
					auth, err := keychain.Resolve(resource)
					require.NoError(t, err)
					config, err := auth.Authorization()
					require.NoError(t, err)
					require.Equal(t, "preferred", config.Username)
				})
			}
		})
	}
}

func TestImageVerifierKeychainSecretPrecedence(t *testing.T) {
	first := newImagePullSecret(t, "tenant-a", "first", corev1.SecretTypeDockerConfigJson, map[string]map[string]string{
		"https://private.example/v1/": {"username": "first", "password": "password"},
		"private.example/team/":       {"username": "scoped", "password": "password"},
		"other.example":               {"username": "fallback", "password": "password"},
	})
	last := newImagePullSecret(t, "tenant-a", "last", corev1.SecretTypeDockercfg, map[string]map[string]string{
		"private.example":  {"username": "last", "password": "password"},
		"private.example*": {"username": "wildcard", "password": "password"},
	})
	otherNamespace := newImagePullSecret(t, "tenant-b", "last", corev1.SecretTypeDockercfg, map[string]map[string]string{
		"private.example": {"username": "wrong-namespace", "password": "password"},
	})
	verifier := NewImageVerifier(logr.Discard(), newTrustedRootTestClient(t, first, last, otherNamespace), nil)
	keychain, err := verifier.buildKeychain(context.Background(), []corev1.LocalObjectReference{{Name: "first"}, {Name: "last"}}, "tenant-a")
	require.NoError(t, err)
	for _, tt := range []struct {
		target   string
		username string
	}{
		{target: "private.example/other/image", username: "last"},
		{target: "private.example/team/image", username: "scoped"},
		{target: "other.example/team/image", username: "fallback"},
	} {
		t.Run(tt.target, func(t *testing.T) {
			resource, err := name.NewRepository(tt.target)
			require.NoError(t, err)
			auth, err := keychain.Resolve(resource)
			require.NoError(t, err)
			config, err := auth.Authorization()
			require.NoError(t, err)
			require.Equal(t, tt.username, config.Username)
		})
	}
}

func TestImageVerifierKeychainRejectsInvalidSecrets(t *testing.T) {
	for _, tt := range []struct {
		name string
		data string
	}{
		{name: "malformed JSON", data: "{"},
		{name: "missing auths", data: `{}`},
		{name: "null auths", data: `{"auths":null}`},
		{name: "empty credentials", data: `{"auths":{"private.example":{}}}`},
		{name: "invalid encoded auth", data: `{"auths":{"private.example":{"auth":"not-base64!"}}}`},
		{name: "invalid auth with basic fallback", data: `{"auths":{"private.example":{"auth":"not-base64!","username":"reader","password":"password"}}}`},
		{name: "encoded auth without separator", data: `{"auths":{"private.example":{"auth":"bm9zZXBhcmF0b3I="}}}`},
		{name: "non-object credentials", data: `{"auths":{"private.example":"not-an-object"}}`},
		{name: "registry with user info", data: `{"auths":{"https://sensitive@example.test":{"username":"reader"}}}`},
		{name: "registry with query", data: `{"auths":{"https://example.test/?secret=sensitive":{"username":"reader"}}}`},
		{name: "registry with invalid port", data: `{"auths":{"https://example.test:invalid":{"username":"reader"}}}`},
		{name: "registry with encoded path", data: `{"auths":{"https://example.test/team%2Fsub":{"username":"reader"}}}`},
		{name: "registry with encoded percent", data: `{"auths":{"https://example.test/team%25sub":{"username":"reader"}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "registry"},
				Type:       corev1.SecretTypeDockerConfigJson,
				Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(tt.data)},
			}
			verifier := NewImageVerifier(logr.Discard(), newTrustedRootTestClient(t, secret), nil)
			keychain, err := verifier.buildKeychain(context.Background(), []corev1.LocalObjectReference{{Name: secret.Name}}, secret.Namespace)
			require.ErrorContains(t, err, "tenant-a/registry")
			require.NotContains(t, err.Error(), "sensitive")
			require.Nil(t, keychain)
		})
	}
}

func TestImageVerifierKeychainAliasPrecedence(t *testing.T) {
	first := newImagePullSecret(t, "tenant-a", "first", corev1.SecretTypeDockerConfigJson, map[string]map[string]string{
		"docker.io":                   {"username": "alias", "password": "password"},
		"https://index.docker.io/v1/": {"username": "canonical", "password": "password"},
	})
	last := newImagePullSecret(t, "tenant-a", "last", corev1.SecretTypeDockercfg, map[string]map[string]string{
		"registry-1.docker.io": {"username": "last", "password": "password"},
	})
	verifier := NewImageVerifier(logr.Discard(), newTrustedRootTestClient(t, first, last), nil)
	for _, tt := range []struct {
		name     string
		secrets  []corev1.LocalObjectReference
		username string
	}{
		{name: "same Secret keys are deterministic", secrets: []corev1.LocalObjectReference{{Name: "first"}}, username: "canonical"},
		{name: "later Secret overrides aliases", secrets: []corev1.LocalObjectReference{{Name: "first"}, {Name: "last"}}, username: "last"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			keychain, err := verifier.buildKeychain(context.Background(), tt.secrets, "tenant-a")
			require.NoError(t, err)
			for _, registry := range []string{"docker.io", "index.docker.io", "registry-1.docker.io"} {
				resource, err := name.NewRepository(registry + "/team/image")
				require.NoError(t, err)
				auth, err := keychain.Resolve(resource)
				require.NoError(t, err)
				config, err := auth.Authorization()
				require.NoError(t, err)
				require.Equal(t, tt.username, config.Username)
			}
		})
	}
}

func TestImageVerifierConfiguredSecretsRequireReader(t *testing.T) {
	verifier := NewImageVerifier(logr.Discard(), nil, nil)
	config := imageverify.VerifyConfig{
		Namespace: "tenant-a", ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}},
	}
	_, err := verifier.resolveDigest(context.Background(), "private.example/team/image:latest", config)
	require.ErrorContains(t, err, "Kubernetes API reader")
	err = verifier.verifyImageSignature(context.Background(), testImageDigest, config, nil)
	require.ErrorContains(t, err, "Kubernetes API reader")
}

func TestImageVerifierKeychainNamespaceAndSecretValidation(t *testing.T) {
	secret := newImagePullSecret(t, "tenant-b", "registry", corev1.SecretTypeDockerConfigJson,
		map[string]map[string]string{"private.example": {"username": "reader", "password": "password"}})
	for _, tt := range []struct {
		name      string
		namespace string
		mutate    func(*corev1.Secret)
	}{
		{name: "missing namespace"},
		{name: "Secret exists only in another namespace", namespace: "tenant-a"},
		{name: "invalid type", namespace: "tenant-b", mutate: func(secret *corev1.Secret) { secret.Type = corev1.SecretTypeOpaque }},
		{name: "missing data key", namespace: "tenant-b", mutate: func(secret *corev1.Secret) { secret.Data = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := secret.DeepCopy()
			if tt.mutate != nil {
				tt.mutate(fixture)
			}
			verifier := NewImageVerifier(logr.Discard(), newTrustedRootTestClient(t, fixture), nil)
			keychain, err := verifier.buildKeychain(context.Background(), []corev1.LocalObjectReference{{Name: secret.Name}}, tt.namespace)
			require.Error(t, err)
			require.Nil(t, keychain)
		})
	}
}

func newImagePullSecret(t *testing.T, namespace, secretName string, secretType corev1.SecretType, auths map[string]map[string]string) *corev1.Secret {
	t.Helper()
	data, err := json.Marshal(auths)
	require.NoError(t, err)
	key := corev1.DockerConfigKey
	if secretType == corev1.SecretTypeDockerConfigJson {
		key = corev1.DockerConfigJsonKey
		data = []byte(`{"auths":` + string(data) + `}`)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: secretName},
		Type:       secretType,
		Data:       map[string][]byte{key: data},
	}
}
