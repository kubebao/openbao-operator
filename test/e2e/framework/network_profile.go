//go:build e2e

package framework

import (
	"os"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// APIServerEgressForNetworkProfile opts isolated Cilium network labs into the
// operator-owned API-server policy. Other E2E environments keep the default.
func APIServerEgressForNetworkProfile() *api.APIServerEgressConfig {
	if os.Getenv("E2E_NETWORK_PROFILE") == "" {
		return nil
	}
	return &api.APIServerEgressConfig{Mode: "Cilium", Port: 6443}
}
