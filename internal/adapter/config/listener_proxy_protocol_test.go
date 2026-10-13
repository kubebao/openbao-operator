package config

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/require"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestRenderHCLProxyProtocol(t *testing.T) {
	addresses := []string{"192.0.2.10", "198.51.100.0/24", "2001:db8::10", "2001:db8:1::/64"}
	tests := []struct {
		name      string
		behavior  string
		addresses []string
		metrics   bool
		golden    string
	}{
		{name: "disabled"},
		{name: "use always", behavior: "use_always", golden: "render_hcl_proxy_protocol_use_always"},
		{name: "allow authorized", behavior: "allow_authorized", addresses: addresses, metrics: true,
			golden: "render_hcl_proxy_protocol_authorized"},
		{name: "deny unauthorized", behavior: "deny_unauthorized", addresses: addresses,
			golden: "render_hcl_proxy_protocol_deny_unauthorized"},
		{name: "use always with addresses", behavior: "use_always", addresses: addresses},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("proxy-protocol", "default")
			cluster.Spec.Version = listenerTLSTestVersion
			cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{Listener: &openbaov1alpha1.ListenerConfig{
				ProxyProtocolBehavior:        tt.behavior,
				ProxyProtocolAuthorizedAddrs: tt.addresses,
			}}
			wantListeners := 1
			if tt.metrics {
				wantListeners++
				cluster.Spec.Observability = &openbaov1alpha1.ObservabilityConfig{Metrics: &openbaov1alpha1.MetricsConfig{
					Enabled: true, ScrapeProfile: configScrapeProfileAllNodes,
				}}
			}
			got, err := RenderHCL(cluster, testInfrastructureDetails(cluster))
			require.NoError(t, err)
			file, diagnostics := hclsyntax.ParseConfig(got, "openbao.hcl", hcl.Pos{Line: 1, Column: 1})
			require.False(t, diagnostics.HasErrors(), diagnostics.Error())
			listeners := 0
			for _, block := range file.Body.(*hclsyntax.Body).Blocks {
				if block.Type != "listener" {
					continue
				}
				behavior := block.Body.Attributes["proxy_protocol_behavior"]
				authorized := block.Body.Attributes["proxy_protocol_authorized_addrs"]
				if listeners > 0 || tt.behavior == "" {
					require.Nil(t, behavior)
				} else {
					require.NotNil(t, behavior)
					value, diagnostics := behavior.Expr.Value(nil)
					require.False(t, diagnostics.HasErrors(), diagnostics.Error())
					require.Equal(t, tt.behavior, value.AsString())
				}
				if listeners > 0 || len(tt.addresses) == 0 {
					require.Nil(t, authorized)
				} else {
					require.NotNil(t, authorized)
					value, diagnostics := authorized.Expr.Value(nil)
					require.False(t, diagnostics.HasErrors(), diagnostics.Error())
					var rendered []string
					for _, address := range value.AsValueSlice() {
						rendered = append(rendered, address.AsString())
					}
					require.Equal(t, tt.addresses, rendered)
				}
				listeners++
			}
			require.Equal(t, wantListeners, listeners)
			if tt.golden != "" {
				compareGolden(t, tt.golden, got)
			}
		})
	}
}

func TestRenderHCLProxyProtocolValidation(t *testing.T) {
	tests := []struct {
		name      string
		listener  *openbaov1alpha1.ListenerConfig
		wantError string
	}{
		{name: "omitted listener"},
		{name: "empty listener", listener: &openbaov1alpha1.ListenerConfig{}},
		{name: "allow authorized missing addresses", listener: &openbaov1alpha1.ListenerConfig{
			ProxyProtocolBehavior: "allow_authorized",
		}, wantError: "proxyProtocolAuthorizedAddrs is required"},
		{name: "deny unauthorized missing addresses", listener: &openbaov1alpha1.ListenerConfig{
			ProxyProtocolBehavior: "deny_unauthorized",
		}, wantError: "proxyProtocolAuthorizedAddrs is required"},
		{name: "legacy allow any", listener: &openbaov1alpha1.ListenerConfig{
			ProxyProtocolBehavior: "allow_any",
		}, wantError: "unsupported; use use_always, allow_authorized, or deny_unauthorized"},
		{name: "unknown behavior", listener: &openbaov1alpha1.ListenerConfig{
			ProxyProtocolBehavior: "use_proxy_protocol",
		}, wantError: "unsupported"},
		{name: "empty address list", listener: &openbaov1alpha1.ListenerConfig{
			ProxyProtocolBehavior: "use_always", ProxyProtocolAuthorizedAddrs: []string{},
		}, wantError: "must not be empty"},
		{name: "duplicate address", listener: &openbaov1alpha1.ListenerConfig{
			ProxyProtocolBehavior: "allow_authorized", ProxyProtocolAuthorizedAddrs: []string{"192.0.2.10", "192.0.2.10"},
		}, wantError: "duplicate address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("proxy-validation", "default")
			cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{Listener: tt.listener}
			_, err := RenderHCL(cluster, testInfrastructureDetails(cluster))
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
	for _, address := range []string{"", "proxy.example.com", "192.0.2.256", "192.0.2.0/33", "2001:db8::/129", "fe80::1%eth0", " 192.0.2.10"} {
		t.Run("invalid address "+address, func(t *testing.T) {
			cluster := newMinimalCluster("proxy-validation", "default")
			cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{Listener: &openbaov1alpha1.ListenerConfig{
				ProxyProtocolBehavior: "allow_authorized", ProxyProtocolAuthorizedAddrs: []string{address},
			}}
			_, err := RenderHCL(cluster, testInfrastructureDetails(cluster))
			require.ErrorContains(t, err, "invalid IP address or CIDR")
		})
	}
}
