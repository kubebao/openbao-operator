package constants

const (
	// ReasonPolicyReconciliationFailed reports an unsuccessful policy read or repair.
	ReasonPolicyReconciliationFailed = "PolicyReconciliationFailed"
	// ReasonPoliciesNotReady indicates an operation's required policy is not verified.
	ReasonPoliciesNotReady = "PoliciesNotReady"
)

// Shared condition reasons used across app, service, and controller layers for
// OpenBaoCluster workflows.
const (
	ReasonGatewayAPIMissing                    = "GatewayAPIMissing"
	ReasonOIDCBootstrapConfigurationInvalid    = "OIDCBootstrapConfigurationInvalid"
	ReasonAPIServerNetworkConfigurationInvalid = "APIServerNetworkConfigurationInvalid"
	ReasonServiceIPFamilyChangeNotSupported    = "ServiceIPFamilyChangeNotSupported"

	ReasonACMEDomainNotResolvable                = "ACMEDomainNotResolvable"
	ReasonACMEGatewayNotConfiguredForPassthrough = "ACMEGatewayNotConfiguredForPassthrough"
	ReasonACMEIntegrationReady                   = "ACMEIntegrationReady"

	ReasonGatewayIntegrationReady          = "GatewayIntegrationReady"
	ReasonIngressIntegrationReady          = "IngressIntegrationReady"
	ReasonGatewayReferenceMissing          = "GatewayReferenceMissing"
	ReasonGatewayClassMissing              = "GatewayClassMissing"
	ReasonGatewayClassPending              = "GatewayClassPending"
	ReasonGatewayClassNotAccepted          = "GatewayClassNotAccepted"
	ReasonGatewayVersionUnsupported        = "GatewayVersionUnsupported"
	ReasonGatewayFeatureUnsupported        = "GatewayFeatureUnsupported"
	ReasonGatewayCapabilitiesUnknown       = "GatewayCapabilitiesUnknown"
	ReasonGatewayNotProgrammed             = "GatewayNotProgrammed"
	ReasonGatewayProgrammingPending        = "GatewayProgrammingPending"
	ReasonGatewayListenerIncompatible      = "GatewayListenerIncompatible"
	ReasonGatewayRoutePending              = "GatewayRoutePending"
	ReasonGatewayRouteNotAccepted          = "GatewayRouteNotAccepted"
	ReasonGatewayRouteReferencesUnresolved = "GatewayRouteReferencesUnresolved"
	ReasonIngressClassMissing              = "IngressClassMissing"
	ReasonIngressCapabilitiesUnknown       = "IngressCapabilitiesUnknown"
	ReasonIngressObjectPending             = "IngressObjectPending"
	ReasonIngressLoadBalancerPending       = "IngressLoadBalancerPending"
	ReasonAPIServerNetworkReady            = "APIServerNetworkReady"
	ReasonAPIServerEndpointIPsRecommended  = "APIServerEndpointIPsRecommended"

	ReasonStorageInvalidSize             = "StorageInvalidSize"
	ReasonStorageShrinkNotSupported      = "StorageShrinkNotSupported"
	ReasonStorageResizeNotSupported      = "StorageResizeNotSupported"
	ReasonStorageClassChangeNotSupported = "StorageClassChangeNotSupported"
	ReasonStorageRestartRequired         = "StorageRestartRequired"

	ReasonAuditFileStorageStatefulSetRecreateRequired = "AuditFileStorageStatefulSetRecreateRequired"
)
