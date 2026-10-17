package security

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// buildKeychain constructs a keychain from ImagePullSecrets.
func (v *ImageVerifier) buildKeychain(ctx context.Context, imagePullSecrets []corev1.LocalObjectReference, namespace string) (authn.Keychain, error) {
	if len(imagePullSecrets) == 0 {
		return nil, nil
	}
	if v.reader == nil {
		return nil, fmt.Errorf("a Kubernetes API reader is required for configured ImagePullSecrets")
	}
	if namespace == "" {
		return nil, fmt.Errorf("a namespace is required for configured ImagePullSecrets")
	}

	combined := make(map[string]authn.AuthConfig)
	for _, secretRef := range imagePullSecrets {
		secret := &corev1.Secret{}
		if err := v.reader.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      secretRef.Name,
		}, secret); err != nil {
			return nil, fmt.Errorf("failed to get ImagePullSecret %s/%s: %w", namespace, secretRef.Name, err)
		}

		if secret.Type != corev1.SecretTypeDockerConfigJson && secret.Type != corev1.SecretTypeDockercfg {
			return nil, fmt.Errorf("ImagePullSecret %s/%s has invalid type %s, expected %s or %s",
				namespace, secretRef.Name, secret.Type, corev1.SecretTypeDockerConfigJson, corev1.SecretTypeDockercfg)
		}

		dockerConfigKey := corev1.DockerConfigKey
		if secret.Type == corev1.SecretTypeDockerConfigJson {
			dockerConfigKey = corev1.DockerConfigJsonKey
		}

		dockerConfigData, ok := secret.Data[dockerConfigKey]
		if !ok {
			return nil, fmt.Errorf("ImagePullSecret %s/%s missing key %s", namespace, secretRef.Name, dockerConfigKey)
		}

		normalized, err := decodeRegistryCredentials(dockerConfigData, secret.Type)
		if err != nil {
			return nil, fmt.Errorf("failed to parse docker config from ImagePullSecret %s/%s: %w", namespace, secretRef.Name, err)
		}
		// Preserve the existing later-secret precedence for equivalent registry scopes.
		for registry, credentials := range normalized {
			combined[registry] = credentials
		}
	}

	if len(combined) == 0 {
		return nil, nil
	}
	// Keep credential selection deterministic when matching scopes have equal specificity.
	index := slices.Sorted(maps.Keys(combined))
	slices.Reverse(index)
	return &dockerConfigKeychain{auths: combined, index: index}, nil
}

func decodeRegistryCredentials(data []byte, secretType corev1.SecretType) (map[string]authn.AuthConfig, error) {
	var config struct {
		Auths map[string]authn.AuthConfig `json:"auths"`
	}
	if secretType == corev1.SecretTypeDockercfg {
		if err := json.Unmarshal(data, &config.Auths); err != nil {
			return nil, fmt.Errorf("decode dockercfg: %w", err)
		}
	} else if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("decode dockerconfigjson: %w", err)
	}
	if config.Auths == nil {
		return nil, fmt.Errorf("docker config must contain an auths object or a dockercfg registry map")
	}

	normalized := make(map[string]authn.AuthConfig, len(config.Auths))
	// Sort before combining equivalent URL and alias keys so map iteration cannot select credentials.
	for _, registry := range slices.Sorted(maps.Keys(config.Auths)) {
		credentials := config.Auths[registry]
		if credentials.Username == "" && credentials.Password == "" &&
			credentials.IdentityToken == "" && credentials.RegistryToken == "" {
			return nil, fmt.Errorf("registry entry has no supported credentials")
		}
		host, scopePath, err := registryCredentialScope(registry)
		if err != nil {
			return nil, err
		}
		switch host {
		case "docker.io", "index.docker.io", "registry-1.docker.io":
			for _, alias := range []string{"docker.io", "index.docker.io", "registry-1.docker.io"} {
				normalized[alias+scopePath] = credentials
			}
		default:
			normalized[host+scopePath] = credentials
		}
	}
	return normalized, nil
}

func registryCredentialScope(registry string) (string, string, error) {
	if !strings.Contains(registry, "://") {
		registry = "https://" + registry
	}
	parsed, err := url.Parse(registry)
	if err != nil {
		// URL parse errors can include user information from a malformed registry key.
		return "", "", fmt.Errorf("invalid registry address")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		strings.Contains(parsed.EscapedPath(), "%") {
		return "", "", fmt.Errorf("registry address must contain only an HTTP(S) host and optional path")
	}
	for _, label := range strings.Split(parsed.Hostname(), ".") {
		if _, err := path.Match(label, ""); err != nil {
			return "", "", fmt.Errorf("invalid registry hostname pattern")
		}
	}
	scopePath := parsed.Path
	// Match Kubernetes' treatment of legacy registry API prefixes.
	if strings.HasPrefix(scopePath, "/v1/") || strings.HasPrefix(scopePath, "/v2/") {
		scopePath = scopePath[3:]
	}
	if scopePath == "/" {
		scopePath = ""
	}
	return strings.ToLower(parsed.Host), scopePath, nil
}

type dockerConfigKeychain struct {
	auths map[string]authn.AuthConfig
	index []string
}

func (k *dockerConfigKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	target, err := url.Parse("https://" + resource.String())
	if err != nil {
		return nil, fmt.Errorf("parse registry resource: %w", err)
	}
	var selected authn.AuthConfig
	longestPath := -1
	exactHost := false
	for _, registry := range k.index {
		scope, err := url.Parse("https://" + registry)
		if err != nil {
			return nil, fmt.Errorf("parse registry credential scope: %w", err)
		}
		if !registryScopeMatches(scope, target) {
			continue
		}
		// Prefer the longest matching path, then an exact hostname over a hostname glob.
		matchesExactHost := strings.EqualFold(scope.Hostname(), target.Hostname())
		if len(scope.Path) < longestPath ||
			(len(scope.Path) == longestPath && (!matchesExactHost || exactHost)) {
			continue
		}
		selected = k.auths[registry]
		longestPath = len(scope.Path)
		exactHost = matchesExactHost
	}
	if longestPath == -1 {
		return authn.Anonymous, nil
	}
	// Use the decoded fields so padded and unpadded auth values behave identically.
	selected.Auth = ""
	return authn.FromConfig(selected), nil
}

// registryScopeMatches follows Kubernetes credential-provider matching: exact ports,
// literal path prefixes, and hostname globs that each match one domain label.
// See https://kubernetes.io/docs/concepts/containers/images/#interpretation-of-config-json.
func registryScopeMatches(scope, target *url.URL) bool {
	if scope.Port() != target.Port() || !strings.HasPrefix(target.Path, scope.Path) {
		return false
	}
	labels := strings.Split(scope.Hostname(), ".")
	targetLabels := strings.Split(strings.ToLower(target.Hostname()), ".")
	if len(labels) != len(targetLabels) {
		return false
	}
	for i, label := range labels {
		matched, err := path.Match(label, targetLabels[i])
		if err != nil || !matched {
			return false
		}
	}
	return true
}
