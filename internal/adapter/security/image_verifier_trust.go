package security

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/root"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubebao/openbao-operator/internal/port/imageverify"
)

const trustedRootConfigMapKey = "trusted_root.json"

type trustedRootSnapshot struct {
	identity string
	material root.TrustedMaterial
}

// loadTrustedRoot loads the trusted root material for keyless verification.
func (v *ImageVerifier) loadTrustedRoot(ctx context.Context) (root.TrustedMaterial, error) {
	snapshot, err := v.loadTrustedRootSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snapshot.material, nil
}

func (v *ImageVerifier) loadTrustedRootSnapshot(ctx context.Context) (trustedRootSnapshot, error) {
	if v.trustedRootConfig != nil {
		configMapName := strings.TrimSpace(v.trustedRootConfig.ConfigMapName)
		configMapNamespace := strings.TrimSpace(v.trustedRootConfig.ConfigMapNamespace)
		if configMapName != "" || configMapNamespace != "" {
			return v.loadTrustedRootSnapshotFromConfigMap(ctx, configMapNamespace, configMapName)
		}
	}

	v.logger.V(1).Info("Using embedded trusted_root.json")
	trustedRoot, err := parseTrustedRootWithPolicy(embeddedTrustedRootJSON)
	if err != nil {
		return trustedRootSnapshot{}, fmt.Errorf("failed to parse embedded trusted_root.json: %w", err)
	}
	return newTrustedRootSnapshot(embeddedTrustedRootJSON, trustedRoot), nil
}

func (v *ImageVerifier) loadTrustedRootSnapshotFromConfigMap(ctx context.Context, namespace, name string) (trustedRootSnapshot, error) {
	trustedRootJSON, err := v.loadTrustedRootJSONFromConfigMap(ctx, namespace, name)
	if err != nil {
		return trustedRootSnapshot{}, err
	}

	v.logger.Info("Loading trusted root from ConfigMap", "configmap", name, "namespace", namespace)
	trustedRoot, err := parseTrustedRootWithPolicy(trustedRootJSON)
	if err != nil {
		return trustedRootSnapshot{}, fmt.Errorf("failed to parse %s from ConfigMap %s/%s: %w", trustedRootConfigMapKey, namespace, name, err)
	}
	return newTrustedRootSnapshot(trustedRootJSON, trustedRoot), nil
}

func (v *ImageVerifier) loadTrustedRootJSONFromConfigMap(ctx context.Context, namespace, name string) ([]byte, error) {
	if namespace == "" || name == "" {
		return nil, fmt.Errorf("trusted root ConfigMap requires both namespace and name")
	}
	if v.reader == nil {
		return nil, fmt.Errorf("trusted root ConfigMap %s/%s is configured but Kubernetes client is not available", namespace, name)
	}

	configMap := &corev1.ConfigMap{}
	if err := v.reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, configMap); err != nil {
		return nil, fmt.Errorf("failed to load trusted root ConfigMap %s/%s: %w", namespace, name, err)
	}

	trustedRootJSON, ok := configMap.Data[trustedRootConfigMapKey]
	if !ok || strings.TrimSpace(trustedRootJSON) == "" {
		return nil, fmt.Errorf("trusted root ConfigMap %s/%s is missing required key %q", namespace, name, trustedRootConfigMapKey)
	}
	return []byte(trustedRootJSON), nil
}

func (v *ImageVerifier) verificationTrustedRoot(ctx context.Context, config imageverify.VerifyConfig) (trustedRootSnapshot, error) {
	if strings.TrimSpace(config.PublicKey) != "" && config.IgnoreTlog {
		return trustedRootSnapshot{}, nil
	}
	return v.loadTrustedRootSnapshot(ctx)
}

func newTrustedRootSnapshot(trustedRootJSON []byte, trustedRoot root.TrustedMaterial) trustedRootSnapshot {
	trustedRootHash := sha256.Sum256(trustedRootJSON)
	return trustedRootSnapshot{
		identity: fmt.Sprintf("sha256:%x", trustedRootHash),
		material: trustedRoot,
	}
}
