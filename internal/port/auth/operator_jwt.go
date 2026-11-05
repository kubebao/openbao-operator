package auth

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// ControllerTokenProvider supplies the credential selected for one target.
type ControllerTokenProvider interface {
	Token(context.Context, *openbaov1alpha1.OpenBaoCluster) (string, error)
}

// ControllerJWTAudience resolves the controller audience without accepting a
// tenant-selected target identifier. Recreating a CR produces a new audience.
// A Cluster imported by an API group migration keeps the identity of the
// Cluster it continues: its reserved, immutable migration marker names that
// UID, so the OpenBao controller role keeps trusting the same audience.
func ControllerJWTAudience(cluster *openbaov1alpha1.OpenBaoCluster, installationAudience string) (string, error) {
	if cluster == nil {
		return "", fmt.Errorf("cluster is required for controller JWT authentication")
	}
	switch cluster.Spec.ControllerJWTMode {
	case "", openbaov1alpha1.ControllerJWTModeShared:
		return OperatorJWTAudience(installationAudience), nil
	case openbaov1alpha1.ControllerJWTModeTarget:
		identity := cluster.UID
		if source := strings.TrimSpace(cluster.Annotations[constants.AnnotationMigrationSourceUID]); source != "" {
			identity = types.UID(source)
		}
		if identity == "" {
			return "", fmt.Errorf("cluster UID is required for target-specific controller JWT authentication")
		}
		return "urn:openbao:controller:" + string(identity), nil
	default:
		return "", fmt.Errorf("unsupported controller JWT mode %q", cluster.Spec.ControllerJWTMode)
	}
}

const (
	// TokenAudienceOpenBaoInternal is the default Kubernetes projected
	// ServiceAccount token audience used for OpenBao JWT authentication.
	TokenAudienceOpenBaoInternal = "openbao-internal"

	// JWT auth role names used by the operator and helper executors.
	RoleNameOperator = "openbao-operator"
	RoleNameBackup   = "openbao-operator-backup"
	RoleNameUpgrade  = "openbao-operator-upgrade"
	RoleNameRestore  = "openbao-operator-restore"

	// Policy names used by the operator and helper executors.
	PolicyNameOperator = "openbao-operator"
	PolicyNameBackup   = "openbao-operator-backup"
	PolicyNameUpgrade  = "openbao-operator-upgrade"
	PolicyNameRestore  = "openbao-operator-restore"
	PolicyNameApproval = "openbao-operator-policy-approval"
)

// OperatorJWTBootstrapEnabled reports whether the cluster is configured to let
// the operator bootstrap JWT auth and the default executor roles through
// self-initialization.
func OperatorJWTBootstrapEnabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster != nil &&
		cluster.Spec.SelfInit != nil &&
		cluster.Spec.SelfInit.Enabled &&
		cluster.Spec.SelfInit.OIDC != nil &&
		cluster.Spec.SelfInit.OIDC.Enabled
}

// PolicyReconciliationEnabled reports whether the cluster opts into ownership
// of the built-in operational policies.
func PolicyReconciliationEnabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster != nil && cluster.Spec.ReconcilePolicies
}

// EffectiveJWTRole returns the configured role or a default when operator JWT
// bootstrap is enabled for the cluster.
func EffectiveJWTRole(configuredRole string, bootstrapEnabled bool, defaultRole string) string {
	role := strings.TrimSpace(configuredRole)
	if role != "" {
		return role
	}
	if bootstrapEnabled {
		return defaultRole
	}
	return ""
}

// OperatorJWTAudience returns the install-scoped audience used for projected
// OpenBao auth tokens. It defaults to TokenAudienceOpenBaoInternal when unset.
func OperatorJWTAudience(configuredAudience string) string {
	audience := strings.TrimSpace(configuredAudience)
	if audience == "" {
		audience = TokenAudienceOpenBaoInternal
	}
	return audience
}

// BootstrapAudienceOverride returns the audience explicitly configured on the
// cluster self-init OIDC stanza. It is retained for compatibility, but the
// effective JWT audience is installation-scoped.
func BootstrapAudienceOverride(cluster *openbaov1alpha1.OpenBaoCluster) string {
	if cluster == nil || cluster.Spec.SelfInit == nil || cluster.Spec.SelfInit.OIDC == nil {
		return ""
	}

	return strings.TrimSpace(cluster.Spec.SelfInit.OIDC.Audience)
}

// BootstrapAudienceMatchesInstallation reports whether the optional
// cluster-scoped self-init audience agrees with the install-scoped operator JWT
// audience. A blank override is always accepted.
func BootstrapAudienceMatchesInstallation(cluster *openbaov1alpha1.OpenBaoCluster, operatorAudience string) bool {
	override := BootstrapAudienceOverride(cluster)
	return override == "" || override == OperatorJWTAudience(operatorAudience)
}
