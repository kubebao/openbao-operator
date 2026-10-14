package hardenedcontract

import (
	"fmt"
	"net/netip"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

type Violation struct {
	Rule    RuleID
	Reason  string
	Message string
}

func EvaluateOpenBaoCluster(cluster *openbaov1alpha1.OpenBaoCluster) *Violation {
	if cluster == nil || cluster.Spec.Profile != openbaov1alpha1.ProfileHardened {
		return nil
	}

	if !cluster.Spec.TLS.Enabled {
		return securityViolation(RuleRuntimeTLSEnabled, "Hardened profile requires spec.tls.enabled=true.")
	}

	if listenerTLSDisabled(cluster) {
		return securityViolation(
			RuleListenerTLS,
			"Hardened profile does not allow spec.configuration.listener.tlsDisable=true.",
		)
	}

	if target := backupTarget(cluster); target != nil {
		if violation := EvaluateStorageTarget("Backup", *target); violation != nil {
			return violation
		}
	}

	if serviceMonitorTLSSkipVerify(cluster) {
		return securityViolation(
			RuleServiceMonitorTLSVerification,
			"Hardened profile does not allow ServiceMonitor TLS insecureSkipVerify.",
		)
	}

	if gatewayBackendTLSDisabled(cluster) {
		return securityViolation(
			RuleGatewayBackendTLS,
			"Hardened profile requires Gateway backend TLS unless spec.gateway.tlsPassthrough=true.",
		)
	}

	if flagName, ok := dangerousRuntimeFlag(cluster); ok {
		return securityViolation(
			RuleDangerousRuntimeFlags,
			fmt.Sprintf("Hardened profile does not allow %s=true.", flagName),
		)
	}

	if rawIngressRulesConfigured(cluster) {
		return securityViolation(
			RuleRawIngressRules,
			"Hardened profile does not allow spec.network.ingressRules; use spec.network.trustedIngressPeers.",
		)
	}

	if network := cluster.Spec.Network; network != nil {
		if !TrustedIngressPeersExplicit(network.TrustedIngressPeers) {
			return securityViolation(
				RuleTrustedIngressPeers,
				"Hardened profile requires spec.network.trustedIngressPeers entries to select explicit non-wildcard sources.",
			)
		}
		if !EgressRulesExplicit(network.EgressRules) {
			return securityViolation(
				RuleEgressRules,
				"Hardened profile requires spec.network.egressRules entries to be port-scoped and target explicit non-wildcard peers.",
			)
		}
	}

	return nil
}

func EvaluateStorageTarget(operation string, target openbaov1alpha1.BackupTarget) *Violation {
	title := strings.TrimSpace(operation)
	if title == "" {
		title = "Storage"
	}
	if target.InsecureSkipVerify {
		return securityViolation(
			RuleStorageTLSVerification,
			fmt.Sprintf(
				"Hardened profile does not allow %s storage TLS verification to be disabled.",
				strings.ToLower(title),
			),
		)
	}
	if !HasExplicitStorageIdentity(target) {
		return securityViolation(
			RuleStorageExplicitIdentity,
			fmt.Sprintf(
				"Hardened profile does not allow %s storage to rely on ambient credentials; configure "+
					"target.credentialsSecretRef, target.workloadIdentity, or target.s3.roleArn (target.roleArn for compatibility) for S3 targets.",
				strings.ToLower(title),
			),
		)
	}
	return nil
}

func HasExplicitStorageIdentity(target openbaov1alpha1.BackupTarget) bool {
	if target.CredentialsSecretRef != nil && strings.TrimSpace(target.CredentialsSecretRef.Name) != "" {
		return true
	}
	if storageTargetUsesS3(target) && strings.TrimSpace(target.EffectiveS3RoleARN()) != "" {
		return true
	}
	if target.WorkloadIdentity == nil {
		return false
	}
	return len(target.WorkloadIdentity.ServiceAccountAnnotations) > 0 || len(target.WorkloadIdentity.PodLabels) > 0
}

func storageTargetUsesS3(target openbaov1alpha1.BackupTarget) bool {
	switch strings.TrimSpace(strings.ToLower(target.Provider)) {
	case "", constants.StorageProviderS3:
		return true
	default:
		return false
	}
}

func TrustedIngressPeersExplicit(peers []networkingv1.NetworkPolicyPeer) bool {
	for _, peer := range peers {
		if !NetworkPolicyPeerExplicit(peer) {
			return false
		}
	}
	return true
}

func EgressRulesExplicit(rules []networkingv1.NetworkPolicyEgressRule) bool {
	for _, rule := range rules {
		if len(rule.To) == 0 || len(rule.Ports) == 0 {
			return false
		}
		for _, port := range rule.Ports {
			if port.Port == nil {
				return false
			}
		}
		for _, peer := range rule.To {
			if !NetworkPolicyPeerExplicit(peer) {
				return false
			}
		}
	}
	return true
}

func NetworkPolicyPeerExplicit(peer networkingv1.NetworkPolicyPeer) bool {
	namespaceExplicit := labelSelectorExplicit(peer.NamespaceSelector)
	podExplicit := labelSelectorExplicit(peer.PodSelector)

	if peer.NamespaceSelector != nil && !namespaceExplicit {
		return false
	}
	if peer.PodSelector != nil && !podExplicit && !namespaceExplicit {
		return false
	}
	if peer.IPBlock != nil && !ipBlockExplicit(peer.IPBlock) {
		return false
	}

	return namespaceExplicit || podExplicit || peer.IPBlock != nil
}

func securityViolation(rule RuleID, message string) *Violation {
	return &Violation{
		Rule:    rule,
		Reason:  constants.ReasonSecurityViolation,
		Message: message,
	}
}

func listenerTLSDisabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster.Spec.Configuration != nil &&
		cluster.Spec.Configuration.Listener != nil &&
		cluster.Spec.Configuration.Listener.TLSDisable != nil &&
		*cluster.Spec.Configuration.Listener.TLSDisable
}

func backupTarget(cluster *openbaov1alpha1.OpenBaoCluster) *openbaov1alpha1.BackupTarget {
	if cluster.Spec.Backup == nil {
		return nil
	}
	return &cluster.Spec.Backup.Target
}

func serviceMonitorTLSSkipVerify(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster.Spec.Observability != nil &&
		cluster.Spec.Observability.Metrics != nil &&
		cluster.Spec.Observability.Metrics.ServiceMonitor != nil &&
		cluster.Spec.Observability.Metrics.ServiceMonitor.TLSConfig != nil &&
		cluster.Spec.Observability.Metrics.ServiceMonitor.TLSConfig.InsecureSkipVerify != nil &&
		*cluster.Spec.Observability.Metrics.ServiceMonitor.TLSConfig.InsecureSkipVerify
}

func gatewayBackendTLSDisabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster.Spec.Gateway != nil &&
		cluster.Spec.Gateway.Enabled &&
		!cluster.Spec.Gateway.TLSPassthrough &&
		cluster.Spec.Gateway.BackendTLS != nil &&
		cluster.Spec.Gateway.BackendTLS.Enabled != nil &&
		!*cluster.Spec.Gateway.BackendTLS.Enabled
}

func dangerousRuntimeFlag(cluster *openbaov1alpha1.OpenBaoCluster) (string, bool) {
	if cluster.Spec.Configuration == nil {
		return "", false
	}
	config := cluster.Spec.Configuration
	switch {
	case boolPtrTrue(config.DetectDeadlocks):
		return "spec.configuration.detectDeadlocks", true
	case boolPtrTrue(config.RawStorageEndpoint):
		return "spec.configuration.rawStorageEndpoint", true
	case boolPtrTrue(config.IntrospectionEndpoint):
		return "spec.configuration.introspectionEndpoint", true
	case boolPtrTrue(config.UnsafeAllowAPIAuditCreation):
		return "spec.configuration.unsafeAllowAPIAuditCreation", true
	default:
		return "", false
	}
}

func rawIngressRulesConfigured(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster.Spec.Network != nil && len(cluster.Spec.Network.IngressRules) > 0
}

func boolPtrTrue(value *bool) bool {
	return value != nil && *value
}

func labelSelectorExplicit(selector *metav1.LabelSelector) bool {
	return selector != nil &&
		(len(selector.MatchLabels) > 0 || len(selector.MatchExpressions) > 0)
}

func ipBlockExplicit(ipBlock *networkingv1.IPBlock) bool {
	if ipBlock == nil {
		return false
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(ipBlock.CIDR))
	if err != nil {
		return false
	}
	if prefix.Bits() == 0 {
		return false
	}
	for _, blocked := range unsafeCIDRPrefixes() {
		if prefixesOverlap(prefix, blocked) {
			return false
		}
	}
	return true
}

func unsafeCIDRPrefixes() []netip.Prefix {
	return []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("fe80::/10"),
	}
}

func prefixesOverlap(a, b netip.Prefix) bool {
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}
