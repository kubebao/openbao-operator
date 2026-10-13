package config

import (
	"fmt"
	"net/netip"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func validateListenerProxyProtocol(cluster *openbaov1alpha1.OpenBaoCluster) error {
	if cluster.Spec.Configuration == nil || cluster.Spec.Configuration.Listener == nil {
		return nil
	}
	listener := cluster.Spec.Configuration.Listener
	switch listener.ProxyProtocolBehavior {
	case "", "use_always":
	case "allow_authorized", "deny_unauthorized":
		if len(listener.ProxyProtocolAuthorizedAddrs) == 0 {
			return fmt.Errorf("spec.configuration.listener.proxyProtocolAuthorizedAddrs is required for %s", listener.ProxyProtocolBehavior)
		}
	default:
		return fmt.Errorf("spec.configuration.listener.proxyProtocolBehavior %q is unsupported; use use_always, allow_authorized, or deny_unauthorized", listener.ProxyProtocolBehavior)
	}
	if listener.ProxyProtocolAuthorizedAddrs != nil && len(listener.ProxyProtocolAuthorizedAddrs) == 0 {
		return fmt.Errorf("spec.configuration.listener.proxyProtocolAuthorizedAddrs must not be empty")
	}
	seen := make(map[string]bool, len(listener.ProxyProtocolAuthorizedAddrs))
	for _, address := range listener.ProxyProtocolAuthorizedAddrs {
		if seen[address] {
			return fmt.Errorf("spec.configuration.listener.proxyProtocolAuthorizedAddrs contains duplicate address %q", address)
		}
		seen[address] = true
		if ip, err := netip.ParseAddr(address); err == nil && ip.Zone() == "" {
			continue
		}
		if _, err := netip.ParsePrefix(address); err != nil {
			return fmt.Errorf("spec.configuration.listener.proxyProtocolAuthorizedAddrs contains invalid IP address or CIDR %q: %w", address, err)
		}
	}
	return nil
}
