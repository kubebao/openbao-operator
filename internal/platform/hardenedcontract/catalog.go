package hardenedcontract

// RuleID is a stable identifier for one logical Hardened-profile guardrail.
type RuleID string

// EnforcementLayer identifies a layer that owns a Hardened-profile rule.
type EnforcementLayer string

const (
	// LayerCRDSchema is reserved for profile-specific CRD/CEL rules. The current
	// Hardened contract has no rules owned by this layer.
	LayerCRDSchema EnforcementLayer = "crd-schema"
	// LayerAdmissionPolicy identifies rules enforced by the OpenBaoCluster VAP.
	LayerAdmissionPolicy EnforcementLayer = "admission-policy"
	// LayerRuntimeReadiness identifies the smaller Go subset used by runtime
	// validation and readiness/status reporting.
	LayerRuntimeReadiness EnforcementLayer = "runtime-readiness"
)

const (
	RuleHardenedBaseline                   RuleID = "hardened.baseline"
	RuleTransitInlineToken                 RuleID = "hardened.unseal.transit-inline-token"
	RuleAWSInlineSecretKey                 RuleID = "hardened.unseal.aws-inline-secret-key"
	RuleAWSInlineSessionToken              RuleID = "hardened.unseal.aws-inline-session-token"
	RuleAzureInlineClientSecret            RuleID = "hardened.unseal.azure-inline-client-secret"
	RulePKCS11InlinePIN                    RuleID = "hardened.unseal.pkcs11-inline-pin"
	RuleKMSPluginInlineConfig              RuleID = "hardened.unseal.kms-plugin-inline-config"
	RuleImageVerificationEnabled           RuleID = "hardened.image-verification.enabled"
	RuleOperatorImageVerificationEnabled   RuleID = "hardened.operator-image-verification.enabled"
	RuleImageVerificationFailurePolicy     RuleID = "hardened.image-verification.failure-policy"
	RuleOperatorImageVerificationPolicy    RuleID = "hardened.operator-image-verification.failure-policy"
	RuleRunAsNonRoot                       RuleID = "hardened.security-context.run-as-non-root"
	RuleSeccompProfile                     RuleID = "hardened.security-context.seccomp"
	RuleRootIdentity                       RuleID = "hardened.security-context.root-identity"
	RuleRootSupplementalGroups             RuleID = "hardened.security-context.root-supplemental-groups"
	RulePodSysctls                         RuleID = "hardened.security-context.sysctls"
	RuleWindowsSecurityOptions             RuleID = "hardened.security-context.windows-options"
	RuleListenerTLS                        RuleID = "hardened.listener-tls"
	RuleStorageTLSVerification             RuleID = "hardened.storage.tls-verification"
	RuleStorageExplicitIdentity            RuleID = "hardened.storage.explicit-identity"
	RuleServiceMonitorTLSVerification      RuleID = "hardened.service-monitor.tls-verification"
	RuleGatewayBackendTLS                  RuleID = "hardened.gateway.backend-tls"
	RuleDangerousRuntimeFlags              RuleID = "hardened.configuration.dangerous-runtime-flags"
	RuleRawIngressRules                    RuleID = "hardened.network.raw-ingress-rules"
	RuleTrustedIngressPeers                RuleID = "hardened.network.trusted-ingress-peers"
	RuleEgressRules                        RuleID = "hardened.network.egress-rules"
	RuleOperationEgressRequired            RuleID = "hardened.network.operation-egress-required"
	RuleBackupEndpointScheme               RuleID = "hardened.backup.endpoint-scheme"
	RuleMinimumReplicas                    RuleID = "hardened.minimum-replicas"
	RuleCustomImageTrustRootsAuthorization RuleID = "hardened.authorization.image-trust-roots"
	RuleRuntimeTLSEnabled                  RuleID = "hardened.runtime.tls-enabled"
)

// Rule declares ownership of one Hardened-profile guardrail. AdmissionMessage
// is the exact VAP message when the admission layer owns the rule.
type Rule struct {
	ID               RuleID
	Layers           []EnforcementLayer
	AdmissionMessage string
}

// EnforcedBy reports whether the rule is owned by the requested layer.
func (r Rule) EnforcedBy(layer EnforcementLayer) bool {
	for _, candidate := range r.Layers {
		if candidate == layer {
			return true
		}
	}
	return false
}

var rules = []Rule{
	{
		ID:     RuleHardenedBaseline,
		Layers: []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile requires TLS enabled with mode External or ACME, an external unseal " +
			"(non-static), self-init enabled, and disallows tlsSkipVerify=true in seal configuration.",
	},
	{
		ID:     RuleTransitInlineToken,
		Layers: []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow spec.unseal.transit.token; use " +
			"spec.unseal.credentialsSecretRef instead.",
	},
	{
		ID:     RuleAWSInlineSecretKey,
		Layers: []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow spec.unseal.awskms.secretKey; use " +
			"spec.unseal.credentialsSecretRef or workload identity instead.",
	},
	{
		ID:     RuleAWSInlineSessionToken,
		Layers: []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow spec.unseal.awskms.sessionToken; use " +
			"spec.unseal.credentialsSecretRef or workload identity instead.",
	},
	{
		ID:     RuleAzureInlineClientSecret,
		Layers: []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow spec.unseal.azureKeyVault.clientSecret; use " +
			"spec.unseal.credentialsSecretRef or managed identity instead.",
	},
	{
		ID:     RulePKCS11InlinePIN,
		Layers: []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow spec.unseal.pkcs11.pin; use " +
			"spec.unseal.credentialsSecretRef instead.",
	},
	{
		ID:     RuleKMSPluginInlineConfig,
		Layers: []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow non-empty spec.unseal.kms.config; use " +
			"spec.unseal.credentialsSecretRef instead.",
	},
	{
		ID:               RuleImageVerificationEnabled,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow spec.imageVerification.enabled=false.",
	},
	{
		ID:               RuleOperatorImageVerificationEnabled,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow spec.operatorImageVerification.enabled=false.",
	},
	{
		ID:               RuleImageVerificationFailurePolicy,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow spec.imageVerification.failurePolicy=Warn.",
	},
	{
		ID:               RuleOperatorImageVerificationPolicy,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow spec.operatorImageVerification.failurePolicy=Warn.",
	},
	{
		ID:               RuleRunAsNonRoot,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow spec.securityContext.runAsNonRoot=false.",
	},
	{
		ID:               RuleSeccompProfile,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow spec.securityContext.seccompProfile.type=Unconfined.",
	},
	{
		ID:               RuleRootIdentity,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow root UID/GID overrides in spec.securityContext.",
	},
	{
		ID:               RuleRootSupplementalGroups,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow root supplemental groups in spec.securityContext.",
	},
	{
		ID:               RulePodSysctls,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow pod sysctl overrides in spec.securityContext.",
	},
	{
		ID:               RuleWindowsSecurityOptions,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile does not allow Windows pod security options in spec.securityContext.",
	},
	{
		ID:               RuleListenerTLS,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy, LayerRuntimeReadiness},
		AdmissionMessage: "Hardened profile does not allow spec.configuration.listener.tlsDisable=true.",
	},
	{
		ID:               RuleStorageTLSVerification,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy, LayerRuntimeReadiness},
		AdmissionMessage: "Hardened profile does not allow spec.backup.target.insecureSkipVerify=true.",
	},
	{
		ID:     RuleStorageExplicitIdentity,
		Layers: []EnforcementLayer{LayerAdmissionPolicy, LayerRuntimeReadiness},
		AdmissionMessage: "Hardened profile does not allow backup storage to rely on ambient credentials; configure " +
			"target.credentialsSecretRef, target.workloadIdentity, or target.s3.roleArn (target.roleArn for compatibility) for S3 targets.",
	},
	{
		ID:               RuleServiceMonitorTLSVerification,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy, LayerRuntimeReadiness},
		AdmissionMessage: "Hardened profile does not allow ServiceMonitor TLS insecureSkipVerify.",
	},
	{
		ID:               RuleGatewayBackendTLS,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy, LayerRuntimeReadiness},
		AdmissionMessage: "Hardened profile requires Gateway backend TLS unless spec.gateway.tlsPassthrough=true.",
	},
	{
		ID:     RuleDangerousRuntimeFlags,
		Layers: []EnforcementLayer{LayerAdmissionPolicy, LayerRuntimeReadiness},
		AdmissionMessage: "Hardened profile does not allow dangerous runtime flags: detectDeadlocks, " +
			"rawStorageEndpoint, introspectionEndpoint, or unsafeAllowAPIAuditCreation.",
	},
	{
		ID:               RuleRawIngressRules,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy, LayerRuntimeReadiness},
		AdmissionMessage: "Hardened profile does not allow spec.network.ingressRules; use spec.network.trustedIngressPeers.",
	},
	{
		ID:     RuleTrustedIngressPeers,
		Layers: []EnforcementLayer{LayerAdmissionPolicy, LayerRuntimeReadiness},
		AdmissionMessage: "Hardened profile requires spec.network.trustedIngressPeers entries to select explicit " +
			"non-wildcard sources.",
	},
	{
		ID:     RuleEgressRules,
		Layers: []EnforcementLayer{LayerAdmissionPolicy, LayerRuntimeReadiness},
		AdmissionMessage: "Hardened profile requires spec.network.egressRules entries to be port-scoped and target " +
			"explicit non-wildcard peers.",
	},
	{
		ID:               RuleOperationEgressRequired,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile requires non-empty spec.network.egressRules when backups or pre-upgrade snapshots are enabled.",
	},
	{
		ID:               RuleBackupEndpointScheme,
		Layers:           []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Backup endpoint must use HTTPS or S3 scheme in Hardened profile.",
	},
	{
		ID:     RuleMinimumReplicas,
		Layers: []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Hardened profile requires at least 3 replicas for Raft quorum HA. " +
			"Use Profile=Development for non-HA deployments.",
	},
	{
		ID:     RuleCustomImageTrustRootsAuthorization,
		Layers: []EnforcementLayer{LayerAdmissionPolicy},
		AdmissionMessage: "Users configuring custom image verification trust roots in Hardened profile must be " +
			"authorized to use image trust roots on this OpenBaoCluster.",
	},
	{
		ID:     RuleRuntimeTLSEnabled,
		Layers: []EnforcementLayer{LayerRuntimeReadiness},
	},
}

// Rules returns a defensive copy of the Hardened rule catalog.
func Rules() []Rule {
	result := make([]Rule, len(rules))
	for index, rule := range rules {
		result[index] = cloneRule(rule)
	}
	return result
}

// RuleFor returns the catalog entry for id.
func RuleFor(id RuleID) (Rule, bool) {
	for _, rule := range rules {
		if rule.ID == id {
			return cloneRule(rule), true
		}
	}
	return Rule{}, false
}

func cloneRule(rule Rule) Rule {
	rule.Layers = append([]EnforcementLayer(nil), rule.Layers...)
	return rule
}
