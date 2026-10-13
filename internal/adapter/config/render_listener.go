package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclwrite"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func buildListenerBlocks(cluster *openbaov1alpha1.OpenBaoCluster) ([]*hclwrite.Block, error) {
	primary, err := buildListenerBlock(cluster)
	if err != nil {
		return nil, err
	}
	blocks := []*hclwrite.Block{primary}
	if metricsOnlyListenerEnabled(cluster) {
		metricsListener, err := buildMetricsOnlyListenerBlock(cluster)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, metricsListener)
	}
	return blocks, nil
}

func buildListenerBlock(cluster *openbaov1alpha1.OpenBaoCluster) (*hclwrite.Block, error) {
	listener := hclListenerTCP{
		Type:               "tcp",
		Address:            fmt.Sprintf("[::]:%d", openBaoAPIPort),
		ClusterAddress:     stringPtr(fmt.Sprintf("[::]:%d", openBaoClusterPort)),
		TLSDisable:         0,
		MaxRequestDuration: configMaxRequestDuration,
	}

	if cluster.Spec.Configuration != nil && cluster.Spec.Configuration.Listener != nil {
		listener.ProxyProtocolBehavior = stringPtr(cluster.Spec.Configuration.Listener.ProxyProtocolBehavior)
		if len(cluster.Spec.Configuration.Listener.ProxyProtocolAuthorizedAddrs) > 0 {
			listener.ProxyProtocolAuthorizedAddrs = &cluster.Spec.Configuration.Listener.ProxyProtocolAuthorizedAddrs
		}
		if cluster.Spec.Configuration.Listener.TLSDisable != nil {
			if *cluster.Spec.Configuration.Listener.TLSDisable {
				listener.TLSDisable = 1
			} else {
				listener.TLSDisable = 0
			}
		}
	}

	tlsMode := cluster.Spec.TLS.Mode
	if tlsMode == "" {
		tlsMode = openbaov1alpha1.TLSModeOperatorManaged
	}

	if tlsMode == openbaov1alpha1.TLSModeACME {
		if cluster.Spec.TLS.ACME == nil {
			return nil, fmt.Errorf("ACME configuration is required when tls.mode is ACME")
		}
		listener.TLSACMECADir = stringPtr(cluster.Spec.TLS.ACME.DirectoryURL)
		domains := portopenbao.ComputeACMEDomains(cluster)
		listener.TLSACMEDomains = &domains
		listener.TLSACMEEmail = stringPtr(cluster.Spec.TLS.ACME.Email)
		listener.TLSACMECachePath = stringPtr(portopenbao.ACMESharedCachePath(cluster))
		listener.TLSACMEDisableHTTPChallenge = boolPtrValue(true)
		if cluster.Spec.Configuration != nil {
			listener.TLSACMECARoot = stringPtr(cluster.Spec.Configuration.ACMECARoot)
		}
	} else {
		listener.TLSCertFile = stringPtr(openBaoPathTLSServerCert)
		listener.TLSKeyFile = stringPtr(openBaoPathTLSServerKey)
		listener.TLSClientCAFile = stringPtr(openBaoPathTLSCACert)
		configureNativeTLSAutoReload(&listener, cluster)
	}

	configureListenerTLSPolicy(&listener, cluster)
	block := gohcl.EncodeAsBlock(listener, "listener")
	if metricsOnlyListenerEnabled(cluster) {
		block.Body().AppendBlock(gohcl.EncodeAsBlock(hclListenerTelemetry{
			DisallowMetrics: boolPtrValue(true),
		}, "telemetry"))
	}
	return block, nil
}

func buildMetricsOnlyListenerBlock(cluster *openbaov1alpha1.OpenBaoCluster) (*hclwrite.Block, error) {
	tlsMode := cluster.Spec.TLS.Mode
	if tlsMode == "" {
		tlsMode = openbaov1alpha1.TLSModeOperatorManaged
	}
	if tlsMode == openbaov1alpha1.TLSModeACME {
		return nil, fmt.Errorf("observability.metrics.metricsOnlyListener is not supported with ACME TLS mode")
	}

	listener := hclListenerTCP{
		Type:               "tcp",
		Address:            fmt.Sprintf("[::]:%d", metricsOnlyListenerPort(cluster)),
		TLSDisable:         0,
		MaxRequestDuration: configMaxRequestDuration,
		TLSCertFile:        stringPtr(openBaoPathTLSServerCert),
		TLSKeyFile:         stringPtr(openBaoPathTLSServerKey),
		TLSClientCAFile:    stringPtr(openBaoPathTLSCACert),
	}
	configureNativeTLSAutoReload(&listener, cluster)
	configureListenerTLSPolicy(&listener, cluster)

	block := gohcl.EncodeAsBlock(listener, "listener")
	block.Body().AppendBlock(gohcl.EncodeAsBlock(hclListenerTelemetry{
		UnauthenticatedMetricsAccess: boolPtrValue(metricsOnlyListenerUnauthenticatedAccess(cluster)),
		MetricsOnly:                  boolPtrValue(true),
	}, "telemetry"))
	return block, nil
}

func configureListenerTLSPolicy(listener *hclListenerTCP, cluster *openbaov1alpha1.OpenBaoCluster) {
	if cluster.Spec.Configuration == nil || cluster.Spec.Configuration.Listener == nil {
		return
	}
	policy := cluster.Spec.Configuration.Listener
	listener.TLSMinVersion = stringPtr(string(policy.TLSMinVersion))
	listener.TLSMaxVersion = stringPtr(string(policy.TLSMaxVersion))
	if len(policy.TLSKeyExchangePreferences) > 0 {
		groups := make([]string, len(policy.TLSKeyExchangePreferences))
		for i, group := range policy.TLSKeyExchangePreferences {
			groups[i] = string(group)
		}
		listener.TLSKeyExchangePreferences = &groups
	}
}

func configureNativeTLSAutoReload(listener *hclListenerTCP, cluster *openbaov1alpha1.OpenBaoCluster) {
	if listener.TLSDisable == 0 && portopenbao.UsesNativeTLSAutoReload(cluster) {
		listener.TLSAutoReload = boolPtrValue(true)
		listener.TLSAutoReloadInterval = stringPtr(portopenbao.NativeTLSAutoReloadInterval)
	}
}

func workloadMetricsEnabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster != nil &&
		cluster.Spec.Observability != nil &&
		cluster.Spec.Observability.Metrics != nil &&
		cluster.Spec.Observability.Metrics.Enabled
}

func metricsOnlyListenerEnabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	if !workloadMetricsEnabled(cluster) {
		return false
	}
	listener := cluster.Spec.Observability.Metrics.MetricsOnlyListener
	if listener != nil && listener.Enabled != nil {
		return *listener.Enabled
	}
	return cluster.Spec.Observability.Metrics.ScrapeProfile == configScrapeProfileAllNodes
}

func metricsOnlyListenerPort(cluster *openbaov1alpha1.OpenBaoCluster) int32 {
	if workloadMetricsEnabled(cluster) && cluster.Spec.Observability.Metrics.MetricsOnlyListener != nil {
		port := cluster.Spec.Observability.Metrics.MetricsOnlyListener.Port
		if port > 0 {
			return port
		}
	}
	return constants.PortMetrics
}

func metricsOnlyListenerUnauthenticatedAccess(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	if workloadMetricsEnabled(cluster) && cluster.Spec.Observability.Metrics.MetricsOnlyListener != nil {
		enabled := cluster.Spec.Observability.Metrics.MetricsOnlyListener.UnauthenticatedMetricsAccess
		if enabled != nil {
			return *enabled
		}
	}
	return cluster.Spec.Observability.Metrics.ScrapeProfile == configScrapeProfileAllNodes
}
