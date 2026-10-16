package networking

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
)

func (m *Manager) ensureNetworkPolicy(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	name := networkPolicyName(cluster)

	apiServerInfo, err := m.detectAPIServerInfo(ctx, logger, cluster)
	if err != nil {
		return wrapAPIServerNetworkConfigurationError("primary", err)
	}
	if apiServerInfo == nil || (apiServerInfo.ServiceNetworkCIDR == "" && len(apiServerInfo.EndpointIPs) == 0) {
		return wrapAPIServerNetworkConfigurationError("primary", nil)
	}

	desired, err := buildNetworkPolicy(cluster, apiServerInfo, m.operatorNamespace)
	if err != nil {
		return fmt.Errorf("failed to build NetworkPolicy: %w", err)
	}

	desired.TypeMeta = metav1.TypeMeta{
		Kind:       "NetworkPolicy",
		APIVersion: "networking.k8s.io/v1",
	}

	if err := m.applyResource(ctx, desired, cluster); err != nil {
		return fmt.Errorf("failed to ensure NetworkPolicy %s/%s: %w", cluster.Namespace, name, err)
	}

	return nil
}

// ensureJobNetworkPolicy creates or updates a NetworkPolicy that applies to
// backup/restore/upgrade-snapshot Jobs. These pods are excluded from the main
// OpenBao pod NetworkPolicy because they often need different egress (e.g. object
// storage), but they should still run under explicit network constraints.
func (m *Manager) ensureJobNetworkPolicy(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	name := jobNetworkPolicyName(cluster)

	apiServerInfo, err := m.detectAPIServerInfo(ctx, logger, cluster)
	if err != nil {
		return wrapAPIServerNetworkConfigurationError("job", err)
	}
	if apiServerInfo == nil || (apiServerInfo.ServiceNetworkCIDR == "" && len(apiServerInfo.EndpointIPs) == 0) {
		return wrapAPIServerNetworkConfigurationError("job", nil)
	}

	desired, err := buildJobNetworkPolicy(cluster, apiServerInfo)
	if err != nil {
		return fmt.Errorf("failed to build Job NetworkPolicy: %w", err)
	}

	desired.TypeMeta = metav1.TypeMeta{
		Kind:       "NetworkPolicy",
		APIVersion: "networking.k8s.io/v1",
	}

	if err := m.applyResource(ctx, desired, cluster); err != nil {
		return fmt.Errorf("failed to ensure Job NetworkPolicy %s/%s: %w", cluster.Namespace, name, err)
	}

	return nil
}

func wrapAPIServerNetworkConfigurationError(policyScope string, cause error) error {
	scope := "OpenBao"
	if strings.TrimSpace(policyScope) == "job" {
		scope = "job"
	}

	msg := fmt.Sprintf(
		"%s NetworkPolicy requires explicit Kubernetes API egress targets. Configure spec.network.apiServerCIDR. "+
			"If your CNI enforces egress on post-DNAT traffic, also configure spec.network.apiServerEndpointIPs with the control-plane endpoint IPs. "+
			"On Cilium, configure spec.network.apiServerEgress with mode Cilium and the API backend port",
		scope,
	)
	if cause != nil {
		return operatorerrors.WrapPermanentConfig(
			fmt.Errorf("%w: %s: %w", ErrAPIServerNetworkConfigurationInvalid, msg, cause),
		)
	}
	return operatorerrors.WrapPermanentConfig(
		fmt.Errorf("%w: %s", ErrAPIServerNetworkConfigurationInvalid, msg),
	)
}
