package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
	"github.com/kubebao/openbao-operator/internal/adapter/raft"
	appopenbaocluster "github.com/kubebao/openbao-operator/internal/app/openbaocluster"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestSetupControllersRequiresRaftRuntime(t *testing.T) {
	err := setupControllers(t.Context(), nil, controllerProcessRuntime{})

	assert.EqualError(t, err, "OpenBaoCluster Raft runtime is required")
}

func TestSetupControllersRequiresInitializationManager(t *testing.T) {
	err := setupControllers(t.Context(), nil, controllerProcessRuntime{
		openBaoRuntime: appopenbaocluster.RuntimeOpenBaoConfig{
			Raft: raft.NewManager(kubernetesfake.NewClientset(), nil, nil),
		},
	})

	assert.EqualError(t, err, "OpenBaoCluster initialization manager is required")
}

func TestRaftClientFactoryProviderBuildsAuthenticatedClient(t *testing.T) {
	provider := raftClientFactoryProvider{
		clientManager: openbao.NewClientManager(portopenbao.ClientConfig{}),
	}

	factory := provider.FactoryFor("tenant-a/example", nil, "openbao.example.internal")
	require.NotNil(t, factory)
	client, err := factory.NewWithToken("https://openbao.example.internal:8200", "test-token")
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestOpenBaoClusterPodClientFactoryConfiguresPodEndpoint(t *testing.T) {
	baseConfig := portopenbao.ClientConfig{BaseURL: "https://unused.example"}
	var received portopenbao.ClientConfig
	factory := openBaoClusterPodClientFactory(
		nil,
		baseConfig,
		func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
			received = config
			return nil, nil
		},
	)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "tenant-a"},
	}

	_, err := factory(context.Background(), cluster, "example-2")
	require.NoError(t, err)
	assert.Equal(t, "https://example-2.example.tenant-a.svc:8200", received.BaseURL)
	assert.Empty(t, received.TLSServerName)
	assert.Empty(t, received.CACert)
	assert.Equal(t, "https://unused.example", baseConfig.BaseURL)
}

func TestOpenBaoClusterPodClientFactoryRequiresFactory(t *testing.T) {
	factory := openBaoClusterPodClientFactory(nil, portopenbao.ClientConfig{}, nil)

	_, err := factory(context.Background(), &openbaov1alpha1.OpenBaoCluster{}, "example-0")
	assert.EqualError(t, err, "OpenBao client factory is not configured")
}
