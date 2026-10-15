package bootstrap

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/auth"
	configbuilder "github.com/kubebao/openbao-operator/internal/adapter/config"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
)

const completedSelfInitConfig = constants.CompletedSelfInitConfig

// usesStaticSeal returns true if the cluster is configured to use the static seal
// (either by default when unseal config is nil, or explicitly when type is "static").
func usesStaticSeal(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	if cluster.Spec.Unseal == nil {
		return true
	}
	if cluster.Spec.Unseal.Type == "" {
		return true
	}
	return cluster.Spec.Unseal.Type == "static"
}

// ensureUnsealSecret manages the static auto-unseal Secret for the OpenBaoCluster.
// This function implements a "blind create" pattern: it generates the key in
// memory and attempts to create the Secret without reading Secret data. If a
// Secret already exists, only metadata ownership proof is accepted.
func (m *Manager) ensureUnsealSecret(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	// Referenced keys are administrator-managed. Kubernetes mounts the Secret;
	// the operator neither reads its key bytes nor adopts it.
	if cluster.Spec.Unseal != nil && cluster.Spec.Unseal.CredentialsSecretRef != nil {
		return nil
	}

	secretName := resourceidentity.UnsealSecretName(cluster)

	// Generate key in memory
	key, genErr := generateUnsealKey()
	if genErr != nil {
		return fmt.Errorf("failed to generate unseal key for OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, genErr)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: cluster.Namespace,
			Labels:    resourceidentity.Labels(cluster),
		},
		Type:      corev1.SecretTypeOpaque,
		Immutable: ptr.To(true), // Secure by default: prevent accidental overwrites
		Data: map[string][]byte{
			unsealSecretKey: key,
		},
	}

	// Set OwnerReference for garbage collection when the OpenBaoCluster is deleted.
	if err := controllerutil.SetControllerReference(cluster, secret, m.scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on unseal Secret %s/%s: %w", cluster.Namespace, secretName, err)
	}
	if err := resourceownership.SetOwnerUIDAnnotation(secret, cluster); err != nil {
		return err
	}

	// Attempt CREATE first. Existing Secrets are accepted only when metadata
	// proves they already belong to this OpenBaoCluster.
	if err := m.client.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			if proofErr := m.requireExistingSecretOwnerProof(ctx, cluster, secretName, "use existing unseal Secret"); proofErr != nil {
				return proofErr
			}
			logger.V(1).Info("Unseal Secret already exists; skipping creation", "secret", secretName)
			return nil
		}
		return fmt.Errorf("failed to create unseal Secret %s/%s: %w", cluster.Namespace, secretName, err)
	}

	logger.Info("Created unseal Secret", "secret", secretName)
	return nil
}

func (m *Manager) requireExistingSecretOwnerProof(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, secretName, action string) error {
	metadata := &metav1.PartialObjectMetadata{}
	metadata.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	if err := m.client.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: secretName}, metadata); err != nil {
		return fmt.Errorf("failed to verify existing Secret %s/%s owner proof: %w", cluster.Namespace, secretName, err)
	}
	return resourceownership.RequireOwnerProof(action, metadata, cluster)
}

// generateUnsealKey generates a 32-byte random key for the static unseal Secret.
func generateUnsealKey() ([]byte, error) {
	// Generate 32 random bytes for the static unseal key.
	// The raw bytes are written directly to the Secret so that the mounted
	// file contains a 32-byte key compatible with OpenBao's static seal.
	raw := make([]byte, unsealKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("failed to read random bytes: %w", err)
	}

	return raw, nil
}

// ensureConfigMap manages the config.hcl ConfigMap for the OpenBaoCluster using Server-Side Apply.
func (m *Manager) ensureConfigMap(ctx context.Context, _ logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, configContent string) error {
	return m.ensureConfigMapWithName(ctx, cluster, resourceidentity.ConfigMapName(cluster), configContent)
}

func (m *Manager) ensureConfigMapWithName(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, cmName string, configContent string) error {
	if strings.TrimSpace(cmName) == "" {
		return fmt.Errorf("config ConfigMap name is required")
	}

	configMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: cluster.Namespace,
			Labels:    resourceidentity.Labels(cluster),
		},
		Data: map[string]string{
			configFileName: configContent,
		},
	}

	if err := m.applyResource(ctx, configMap, cluster); err != nil {
		return fmt.Errorf("failed to ensure config ConfigMap %s/%s: %w", cluster.Namespace, cmName, err)
	}

	return nil
}

// ensureSelfInitConfigMap creates or updates a separate ConfigMap containing only
// self-initialization stanzas. This ConfigMap is only mounted for pod-0, since
// only the first pod needs to execute initialization requests.
func (m *Manager) ensureSelfInitConfigMap(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	cmName := resourceidentity.ConfigInitMapName(cluster)

	if cluster.Status.SelfInitialized || (cluster.Status.Initialized && cluster.Spec.SelfInit != nil && cluster.Spec.SelfInit.Enabled) {
		logger.V(1).Info("Self-init already completed; reconciling inert self-init ConfigMap", "configmap", cmName)
		return m.ensureConfigMapWithName(ctx, cluster, cmName, completedSelfInitConfig)
	}

	// If self-init is not enabled, delete the ConfigMap if it exists
	if cluster.Spec.SelfInit == nil || !cluster.Spec.SelfInit.Enabled {
		configMap := &corev1.ConfigMap{}
		err := m.client.Get(ctx, types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      cmName,
		}, configMap)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil // Already deleted, nothing to do
			}
			return fmt.Errorf("failed to get self-init ConfigMap %s/%s: %w", cluster.Namespace, cmName, err)
		}

		logger.Info("Self-init disabled; deleting self-init ConfigMap", "configmap", cmName)
		if err := resourceownership.RequireOwnerProof("delete self-init ConfigMap", configMap, cluster); err != nil {
			return err
		}
		if err := m.client.Delete(ctx, configMap); err != nil {
			return fmt.Errorf("failed to delete self-init ConfigMap %s/%s: %w", cluster.Namespace, cmName, err)
		}
		return nil
	}

	// Completion is written before status. Preserve that durable witness if the
	// controller stops before it can persist status, so restarts need no inputs.
	if completed, err := m.selfInitConfigCompleted(ctx, cluster, cmName); err != nil {
		return err
	} else if completed {
		return nil
	}

	// TIGHTENED: No discovery logic here. Use the struct fields.
	var bootstrapConfig *configbuilder.OperatorBootstrapConfig
	if shouldBootstrapJWTAuth(cluster) {
		operatorAudience := auth.OpenBaoJWTAudience()
		if err := validateBootstrapAudience(cluster, operatorAudience); err != nil {
			return err
		}
		if m.oidcIssuer == "" {
			return fmt.Errorf("cannot configure OpenBao JWT auth: OIDC issuer could not be determined. Ensure the operator ServiceAccount can GET %q and %q on the Kubernetes API server (nonResourceURLs RBAC); optionally set spec.selfInit.oidc.issuer to override the issuer string", "/.well-known/openid-configuration", "/openid/v1/jwks")
		}
		if strings.TrimSpace(m.oidcDiscoveryURL) == "" && strings.TrimSpace(m.oidcJWKSURL) == "" && len(m.oidcJWTKeys) == 0 {
			return fmt.Errorf("cannot configure OpenBao JWT auth: OIDC validation material could not be determined. Ensure the operator ServiceAccount can GET %q and %q on the Kubernetes API server (nonResourceURLs RBAC)", "/.well-known/openid-configuration", "/openid/v1/jwks")
		}

		operatorNS := m.operatorNamespace
		if operatorNS == "" {
			operatorNS = "openbao-operator-system"
		}
		operatorSA := os.Getenv("OPERATOR_SERVICE_ACCOUNT_NAME")
		if operatorSA == "" {
			operatorSA = "openbao-operator-controller"
		}

		// Allow overrides from Spec
		issuer := m.oidcIssuer
		if cluster.Spec.SelfInit.OIDC.Issuer != "" {
			issuer = cluster.Spec.SelfInit.OIDC.Issuer
		}
		audience := portauth.OperatorJWTAudience(operatorAudience)

		bootstrapConfig = &configbuilder.OperatorBootstrapConfig{
			OIDCIssuerURL:      issuer,
			OIDCDiscoveryURL:   m.oidcDiscoveryURL,
			OIDCDiscoveryCAPEM: m.oidcDiscoveryCAPEM,
			OIDCJWKSURL:        m.oidcJWKSURL,
			OIDCJWKSCAPEM:      m.oidcJWKSCAPEM,
			JWTKeysPEM:         m.oidcJWTKeys,
			OperatorNS:         operatorNS,
			OperatorSA:         operatorSA,
			JWTAuthAudience:    audience,
		}
	}

	// Render self-init stanzas
	initConfig, err := configbuilder.RenderSelfInitHCL(cluster, bootstrapConfig)
	if err != nil {
		return fmt.Errorf("failed to render self-init config.hcl for OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, err)
	}

	initConfigContent := string(initConfig)
	if len(initConfigContent) == 0 {
		// No self-init requests, delete the ConfigMap if it exists
		configMap := &corev1.ConfigMap{}
		err := m.client.Get(ctx, types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      cmName,
		}, configMap)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil // Already deleted, nothing to do
			}
			return fmt.Errorf("failed to get self-init ConfigMap %s/%s: %w", cluster.Namespace, cmName, err)
		}

		logger.Info("No self-init requests; deleting self-init ConfigMap", "configmap", cmName)
		if err := resourceownership.RequireOwnerProof("delete self-init ConfigMap", configMap, cluster); err != nil {
			return err
		}
		if err := m.client.Delete(ctx, configMap); err != nil {
			return fmt.Errorf("failed to delete self-init ConfigMap %s/%s: %w", cluster.Namespace, cmName, err)
		}
		return nil
	}

	// Use SSA to create or update the ConfigMap
	configMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: cluster.Namespace,
			Labels:    resourceidentity.Labels(cluster),
		},
		Data: map[string]string{
			configFileName: initConfigContent,
		},
	}

	if err := m.applyResource(ctx, configMap, cluster); err != nil {
		return fmt.Errorf("failed to ensure self-init ConfigMap %s/%s: %w", cluster.Namespace, cmName, err)
	}

	return nil
}

func (m *Manager) selfInitConfigCompleted(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, name string) (bool, error) {
	completed := &corev1.ConfigMap{}
	reader := m.reader
	if reader == nil {
		reader = m.client
	}
	err := reader.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: name}, completed)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check self-init completion ConfigMap: %w", err)
	}
	if completed.Data[configFileName] != completedSelfInitConfig {
		return false, nil
	}
	if err := resourceownership.RequireOwnerProof("preserve completed self-init configuration", completed, cluster); err != nil {
		return false, err
	}
	return true, nil
}

func shouldBootstrapJWTAuth(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return portauth.OperatorJWTBootstrapEnabled(cluster)
}
