//go:build integration

package integration

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestCRD_OpenBaoCluster_ProxyProtocol(t *testing.T) {
	tests := []struct {
		name      string
		listener  map[string]any
		wantError string
	}{
		{name: "omitted settings", listener: map[string]any{}},
		{name: "use always", listener: map[string]any{"proxyProtocolBehavior": "use_always"}},
		{name: "legacy schema compatibility", listener: map[string]any{"proxyProtocolBehavior": "allow_any"}},
		{name: "allow authorized", listener: map[string]any{
			"proxyProtocolBehavior": "allow_authorized", "proxyProtocolAuthorizedAddrs": []any{"192.0.2.10", "2001:db8::/64"},
		}},
		{name: "deny unauthorized", listener: map[string]any{
			"proxyProtocolBehavior": "deny_unauthorized", "proxyProtocolAuthorizedAddrs": []any{"192.0.2.0/24"},
		}},
		{name: "unknown behavior", listener: map[string]any{"proxyProtocolBehavior": "use_proxy_protocol"},
			wantError: "proxyProtocolBehavior"},
		{name: "empty address list", listener: map[string]any{"proxyProtocolAuthorizedAddrs": []any{}},
			wantError: "proxyProtocolAuthorizedAddrs"},
		{name: "empty address", listener: map[string]any{"proxyProtocolAuthorizedAddrs": []any{""}},
			wantError: "proxyProtocolAuthorizedAddrs"},
		{name: "duplicate addresses", listener: map[string]any{
			"proxyProtocolAuthorizedAddrs": []any{"192.0.2.10", "192.0.2.10"},
		},
			wantError: "Duplicate"},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(newTestNamespace(t), fmt.Sprintf("proxy-schema-%d", index))
			object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cluster)
			require.NoError(t, err)
			candidate := &unstructured.Unstructured{Object: object}
			candidate.SetGroupVersionKind(openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))
			require.NoError(t, unstructured.SetNestedMap(candidate.Object, tt.listener, "spec", "configuration", "listener"))
			err = k8sClient.Create(ctx, candidate, client.DryRunAll)
			if tt.wantError != "" {
				requireInvalidRequest(t, err)
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestVAP_OpenBaoCluster_ProxyProtocol(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	tests := []struct {
		name      string
		behavior  string
		addresses []string
		wantError string
	}{
		{name: "disabled"},
		{name: "use always", behavior: "use_always"},
		{name: "legacy schema compatibility", behavior: "allow_any"},
		{name: "allow authorized", behavior: "allow_authorized",
			addresses: []string{"192.0.2.10", "198.51.100.0/24", "2001:db8::10", "2001:db8:1::/64"}},
		{name: "CIDR with host bits", behavior: "allow_authorized", addresses: []string{"192.0.2.10/24"}},
		{name: "deny unauthorized", behavior: "deny_unauthorized", addresses: []string{"192.0.2.10"}},
		{name: "allow authorized missing addresses", behavior: "allow_authorized", wantError: "is required"},
		{name: "deny unauthorized missing addresses", behavior: "deny_unauthorized", wantError: "is required"},
		{name: "hostname", behavior: "allow_authorized", addresses: []string{"proxy.example.com"},
			wantError: "IP addresses or CIDRs"},
		{name: "invalid IPv4", behavior: "allow_authorized", addresses: []string{"192.0.2.256"},
			wantError: "IP addresses or CIDRs"},
		{name: "invalid prefix", behavior: "allow_authorized", addresses: []string{"192.0.2.0/33"},
			wantError: "IP addresses or CIDRs"},
		{name: "IPv6 zone", behavior: "allow_authorized", addresses: []string{"fe80::1%eth0"},
			wantError: "IP addresses or CIDRs"},
		{name: "whitespace", behavior: "allow_authorized", addresses: []string{" 192.0.2.10"},
			wantError: "IP addresses or CIDRs"},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(namespace, fmt.Sprintf("proxy-policy-%d", index))
			cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{Listener: &openbaov1alpha1.ListenerConfig{
				ProxyProtocolBehavior: tt.behavior, ProxyProtocolAuthorizedAddrs: tt.addresses,
			}}
			err := k8sClient.Create(ctx, cluster, client.DryRunAll)
			if tt.wantError != "" {
				requireAdmissionDenied(t, err)
				require.ErrorContains(t, err, tt.wantError)
				require.NotContains(t, err.Error(), "no such key")
				return
			}
			require.NoError(t, err)
		})
	}
}
