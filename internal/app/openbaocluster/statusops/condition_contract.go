package statusops

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// ConditionResult contains the evaluated state of a status condition.
type ConditionResult struct {
	Status  metav1.ConditionStatus
	Reason  string
	Message string
}

type conditionContractEntry struct {
	reason string
	status metav1.ConditionStatus
}

type conditionContract map[string]metav1.ConditionStatus

const (
	reasonPaused                            = "Paused"
	reasonTLSSecretMissing                  = "TLSSecretMissing"
	reasonTLSSecretInvalid                  = "TLSSecretInvalid"
	reasonACMECacheReady                    = "ACMECacheReady"
	reasonACMECacheNotConfigured            = "ACMECacheNotConfigured"
	reasonACMECacheMissing                  = "ACMECacheMissing"
	reasonACMECacheInvalidAccessMode        = "ACMECacheInvalidAccessMode"
	reasonAuditFileStorageReady             = "AuditFileStorageReady"
	reasonAuditFileStorageMissing           = "AuditFileStorageMissing"
	reasonAuditFileStoragePending           = "AuditFileStoragePending"
	reasonAuditFileStorageInvalidAccessMode = "AuditFileStorageInvalidAccessMode"
)

func newConditionContract(entries ...conditionContractEntry) conditionContract {
	contract := make(conditionContract, len(entries))
	for _, entry := range entries {
		contract[entry.reason] = entry.status
	}
	return contract
}

func (c conditionContract) allows(result ConditionResult) bool {
	wantStatus, ok := c[result.Reason]
	return ok && wantStatus == result.Status
}

var acmeIntegrationReadyConditionContract = newConditionContract(
	conditionContractEntry{reason: constants.ReasonACMEIntegrationReady, status: metav1.ConditionTrue},
	conditionContractEntry{reason: constants.ReasonGatewayAPIMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonACMEGatewayNotConfiguredForPassthrough, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonACMEDomainNotResolvable, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonPrerequisitesMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonPaused, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: ReasonProfileNotSet, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: reasonUnknown, status: metav1.ConditionUnknown},
)

var gatewayIntegrationReadyConditionContract = newConditionContract(
	conditionContractEntry{reason: constants.ReasonGatewayIntegrationReady, status: metav1.ConditionTrue},
	conditionContractEntry{reason: constants.ReasonGatewayAPIMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonGatewayReferenceMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonGatewayClassMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonGatewayListenerIncompatible, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonGatewayClassNotAccepted, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonGatewayVersionUnsupported, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonGatewayFeatureUnsupported, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonGatewayNotProgrammed, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonGatewayRouteNotAccepted, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonGatewayRouteReferencesUnresolved, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonGatewayClassPending, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: constants.ReasonGatewayCapabilitiesUnknown, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: constants.ReasonGatewayProgrammingPending, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: constants.ReasonGatewayRoutePending, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: reasonPaused, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: ReasonProfileNotSet, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: reasonUnknown, status: metav1.ConditionUnknown},
)

var ingressIntegrationReadyConditionContract = newConditionContract(
	conditionContractEntry{reason: constants.ReasonIngressIntegrationReady, status: metav1.ConditionTrue},
	conditionContractEntry{reason: constants.ReasonIngressClassMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonIngressCapabilitiesUnknown, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: constants.ReasonIngressObjectPending, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: constants.ReasonIngressLoadBalancerPending, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: reasonPaused, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: ReasonProfileNotSet, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: reasonUnknown, status: metav1.ConditionUnknown},
)

var apiServerNetworkReadyConditionContract = newConditionContract(
	conditionContractEntry{reason: constants.ReasonAPIServerEgressPolicyPending, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: constants.ReasonAPIServerNetworkManagedExternally, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: constants.ReasonAPIServerNetworkReady, status: metav1.ConditionTrue},
	conditionContractEntry{reason: constants.ReasonAPIServerEndpointIPsRecommended, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: constants.ReasonAPIServerNetworkConfigurationInvalid, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonPaused, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: ReasonProfileNotSet, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: reasonUnknown, status: metav1.ConditionUnknown},
)

var tlsReadyConditionContract = newConditionContract(
	conditionContractEntry{reason: ReasonDisabled, status: metav1.ConditionTrue},
	conditionContractEntry{reason: reasonReady, status: metav1.ConditionTrue},
	conditionContractEntry{reason: reasonTLSSecretMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonTLSSecretInvalid, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonPaused, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: ReasonProfileNotSet, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: reasonUnknown, status: metav1.ConditionUnknown},
)

var acmeCacheReadyConditionContract = newConditionContract(
	conditionContractEntry{reason: reasonACMECacheReady, status: metav1.ConditionTrue},
	conditionContractEntry{reason: reasonACMECacheNotConfigured, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonACMECacheMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonACMECachePending, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonACMECacheInvalidAccessMode, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonUnknown, status: metav1.ConditionUnknown},
)

var auditFileStorageReadyConditionContract = newConditionContract(
	conditionContractEntry{reason: reasonAuditFileStorageReady, status: metav1.ConditionTrue},
	conditionContractEntry{reason: reasonAuditFileStorageMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonAuditFileStoragePending, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonAuditFileStorageInvalidAccessMode, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonAuditFileStorageStatefulSetRecreateRequired, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonPaused, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: ReasonProfileNotSet, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: reasonUnknown, status: metav1.ConditionUnknown},
)

var backupConfigurationReadyConditionContract = newConditionContract(
	conditionContractEntry{reason: reasonReady, status: metav1.ConditionTrue},
	conditionContractEntry{reason: constants.ReasonAmbientIdentityAssumed, status: metav1.ConditionTrue},
	conditionContractEntry{reason: constants.ReasonWorkloadIdentityConfigured, status: metav1.ConditionTrue},
	conditionContractEntry{reason: constants.ReasonAuthenticationRequired, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonTokenSecretMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonCredentialsSecretMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonNetworkEgressRulesRequired, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonSecurityViolation, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonPaused, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: ReasonProfileNotSet, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: reasonUnknown, status: metav1.ConditionUnknown},
)

var cloudUnsealIdentityReadyConditionContract = newConditionContract(
	conditionContractEntry{reason: reasonReady, status: metav1.ConditionTrue},
	conditionContractEntry{reason: constants.ReasonAmbientIdentityAssumed, status: metav1.ConditionTrue},
	conditionContractEntry{reason: constants.ReasonWorkloadIdentityConfigured, status: metav1.ConditionTrue},
	conditionContractEntry{reason: constants.ReasonCredentialsSecretMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: constants.ReasonPrerequisitesMissing, status: metav1.ConditionFalse},
	conditionContractEntry{reason: reasonPaused, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: ReasonProfileNotSet, status: metav1.ConditionUnknown},
	conditionContractEntry{reason: reasonUnknown, status: metav1.ConditionUnknown},
)

// ApplyACMEIntegrationReadyCondition validates and applies the ACME integration condition.
func ApplyACMEIntegrationReadyCondition(cluster *openbaov1alpha1.OpenBaoCluster, result ConditionResult) {
	applyEvaluatedCondition(
		cluster,
		openbaov1alpha1.ConditionACMEIntegrationReady,
		result,
		acmeIntegrationReadyConditionContract,
	)
}

// ApplyGatewayIntegrationReadyCondition validates and applies the Gateway integration condition.
func ApplyGatewayIntegrationReadyCondition(cluster *openbaov1alpha1.OpenBaoCluster, result ConditionResult) {
	applyEvaluatedCondition(
		cluster,
		openbaov1alpha1.ConditionGatewayIntegrationReady,
		result,
		gatewayIntegrationReadyConditionContract,
	)
}

// ApplyIngressIntegrationReadyCondition validates and applies the Ingress integration condition.
func ApplyIngressIntegrationReadyCondition(cluster *openbaov1alpha1.OpenBaoCluster, result ConditionResult) {
	applyEvaluatedCondition(
		cluster,
		openbaov1alpha1.ConditionIngressIntegrationReady,
		result,
		ingressIntegrationReadyConditionContract,
	)
}

// ApplyAPIServerNetworkReadyCondition validates and applies the API server network condition.
func ApplyAPIServerNetworkReadyCondition(cluster *openbaov1alpha1.OpenBaoCluster, result ConditionResult) {
	applyEvaluatedCondition(
		cluster,
		openbaov1alpha1.ConditionAPIServerNetworkReady,
		result,
		apiServerNetworkReadyConditionContract,
	)
}

// ApplyTLSReadyCondition validates and applies the TLS condition.
func ApplyTLSReadyCondition(cluster *openbaov1alpha1.OpenBaoCluster, result ConditionResult) {
	applyEvaluatedCondition(cluster, openbaov1alpha1.ConditionTLSReady, result, tlsReadyConditionContract)
}

// ApplyACMECacheReadyCondition validates and applies the ACME cache condition.
func ApplyACMECacheReadyCondition(cluster *openbaov1alpha1.OpenBaoCluster, result ConditionResult) {
	applyEvaluatedCondition(cluster, openbaov1alpha1.ConditionACMECacheReady, result, acmeCacheReadyConditionContract)
}

// ApplyAuditFileStorageReadyCondition validates and applies the audit file storage condition.
func ApplyAuditFileStorageReadyCondition(cluster *openbaov1alpha1.OpenBaoCluster, result ConditionResult) {
	applyEvaluatedCondition(
		cluster,
		openbaov1alpha1.ConditionAuditFileStorageReady,
		result,
		auditFileStorageReadyConditionContract,
	)
}

// ApplyBackupConfigurationReadyCondition validates and applies the backup configuration condition.
func ApplyBackupConfigurationReadyCondition(cluster *openbaov1alpha1.OpenBaoCluster, result ConditionResult) {
	applyEvaluatedCondition(
		cluster,
		openbaov1alpha1.ConditionBackupConfigurationReady,
		result,
		backupConfigurationReadyConditionContract,
	)
}

// ApplyCloudUnsealIdentityReadyCondition validates and applies the cloud unseal identity condition.
func ApplyCloudUnsealIdentityReadyCondition(cluster *openbaov1alpha1.OpenBaoCluster, result ConditionResult) {
	applyEvaluatedCondition(
		cluster,
		openbaov1alpha1.ConditionCloudUnsealIdentityReady,
		result,
		cloudUnsealIdentityReadyConditionContract,
	)
}

func applyEvaluatedCondition(
	cluster *openbaov1alpha1.OpenBaoCluster,
	conditionType openbaov1alpha1.ConditionType,
	result ConditionResult,
	contract conditionContract,
) {
	normalized := normalizeConditionContractResult(conditionType, result, contract)
	setClusterConditionResult(cluster, conditionType, normalized)
}

func setClusterConditionResult(
	cluster *openbaov1alpha1.OpenBaoCluster,
	conditionType openbaov1alpha1.ConditionType,
	result ConditionResult,
) {
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               string(conditionType),
		Status:             result.Status,
		ObservedGeneration: cluster.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             result.Reason,
		Message:            result.Message,
	})
}

func normalizeConditionContractResult(
	conditionType openbaov1alpha1.ConditionType,
	result ConditionResult,
	contract conditionContract,
) ConditionResult {
	normalized := ConditionResult{
		Status:  result.Status,
		Reason:  strings.TrimSpace(result.Reason),
		Message: strings.TrimSpace(result.Message),
	}
	if contract.allows(normalized) {
		return normalized
	}

	message := fmt.Sprintf(
		"Controller rejected unexpected %s result: status=%s reason=%q",
		conditionType,
		normalized.Status,
		normalized.Reason,
	)
	if normalized.Message != "" {
		message += ": " + normalized.Message
	}

	return ConditionResult{
		Status:  metav1.ConditionUnknown,
		Reason:  reasonUnknown,
		Message: message,
	}
}
