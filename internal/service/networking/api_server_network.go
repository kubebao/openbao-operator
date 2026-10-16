package networking

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// APIServerNetworkReadiness reports how strongly the operator can validate the
// Kubernetes API egress contract used by operator-managed NetworkPolicies.
type APIServerNetworkReadiness struct {
	Status  metav1.ConditionStatus
	Reason  string
	Message string
}

// EvaluateAPIServerNetworkReadiness validates the operator-known Kubernetes API
// egress configuration for main and Job policies. It does not probe connectivity.
// Unknown identifies incomplete configuration evidence or external restore isolation.
func (m *Manager) EvaluateAPIServerNetworkReadiness(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) APIServerNetworkReadiness {
	if cluster.Annotations[constants.AnnotationRestoreOrigin] != "" {
		return APIServerNetworkReadiness{Status: metav1.ConditionUnknown, Reason: constants.ReasonAPIServerNetworkManagedExternally,
			Message: "Restore destination isolation is administrator-managed; ordinary API egress policies are not applied."}
	}
	info, err := m.detectAPIServerInfo(ctx, logger, cluster)
	if err != nil {
		wrapped := wrapAPIServerNetworkConfigurationError("primary", err)
		return APIServerNetworkReadiness{
			Status:  metav1.ConditionFalse,
			Reason:  constants.ReasonAPIServerNetworkConfigurationInvalid,
			Message: wrapped.Error(),
		}
	}
	if info == nil || (info.ServiceNetworkCIDR == "" && len(info.EndpointIPs) == 0) {
		wrapped := wrapAPIServerNetworkConfigurationError("primary", nil)
		return APIServerNetworkReadiness{
			Status:  metav1.ConditionFalse,
			Reason:  constants.ReasonAPIServerNetworkConfigurationInvalid,
			Message: wrapped.Error(),
		}
	}

	if ciliumAPIServerEnabled(cluster) {
		return m.evaluateCiliumAPIServerPolicy(ctx, cluster)
	}
	if len(info.EndpointIPs) == 0 {
		return APIServerNetworkReadiness{
			Status: metav1.ConditionUnknown,
			Reason: constants.ReasonAPIServerEndpointIPsRecommended,
			Message: fmt.Sprintf(
				"Kubernetes API egress is configured through the service VIP (%s). For IP-based post-DNAT enforcement, configure spec.network.apiServerEndpointIPs. On Cilium, use spec.network.apiServerEgress with mode Cilium and the API backend port. This condition does not probe live connectivity.",
				info.ServiceNetworkCIDR,
			),
		}
	}

	return APIServerNetworkReadiness{
		Status: metav1.ConditionTrue,
		Reason: constants.ReasonAPIServerNetworkReady,
		Message: fmt.Sprintf(
			"Kubernetes API egress is configured with service VIP %s and explicit endpoint IPs %v. This validates IP-based configuration, not live connectivity. Cilium requires spec.network.apiServerEgress with mode Cilium when API server identities do not match CIDR rules.",
			info.ServiceNetworkCIDR,
			info.EndpointIPs,
		),
	}
}
