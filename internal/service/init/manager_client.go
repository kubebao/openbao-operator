package init

import (
	"context"
	"fmt"
	"strings"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/openbaotls"
)

// newOpenBaoClient constructs a minimal OpenBao client for talking to the pod-0 instance
// of the StatefulSet using the cluster's client trust contract.
func (m *Manager) newOpenBaoClient(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (*openbao.Client, error) {
	if strings.TrimSpace(cluster.Name) == "" || strings.TrimSpace(cluster.Namespace) == "" {
		return nil, fmt.Errorf("cluster name and namespace are required to build OpenBao client")
	}

	baseURL := fmt.Sprintf("https://%s-0.%s.%s.svc:%d", cluster.Name, cluster.Name, cluster.Namespace, constants.PortAPI)

	trust, err := openbaotls.ReadClientTrustBundle(ctx, m.clientset, cluster)
	if err != nil {
		return nil, err
	}

	clusterKey := string(cluster.UID)
	factory := m.clientMgr.FactoryFor(clusterKey, trust.CACert, trust.TLSServerName)
	if factory == nil {
		return nil, fmt.Errorf("client manager returned nil factory for cluster %s", clusterKey)
	}

	client, err := factory.New(baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create OpenBao client for %s: %w", baseURL, err)
	}

	return client, nil
}
