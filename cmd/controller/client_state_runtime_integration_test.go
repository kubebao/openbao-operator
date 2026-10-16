//go:build integration

package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
	"github.com/kubebao/openbao-operator/internal/platform/testutil/managerprobe"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestOpenBaoClientStateCleanupManagerLifecycle(t *testing.T) {
	config := managerprobe.Environment(t)
	apiClient, err := client.New(config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	cluster := clientStateCluster("default", "recreated", "")
	cluster.Spec.Profile = openbaov1alpha1.ProfileDevelopment
	cluster.Spec.Version = "2.6.3"
	cluster.Spec.Replicas = 1
	cluster.Spec.Storage.Size = "1Gi"
	cluster.Spec.TLS.RotationPeriod = "720h"
	require.NoError(t, apiClient.Create(t.Context(), cluster))
	oldUID := string(cluster.UID)
	require.NoError(t, apiClient.Delete(t.Context(), cluster))
	cluster = cluster.DeepCopy()
	cluster.ResourceVersion = ""
	cluster.UID = ""
	require.NoError(t, apiClient.Create(t.Context(), cluster))
	newUID := string(cluster.UID)
	require.NotEqual(t, oldUID, newUID)

	clients := openbao.NewClientManager(portopenbao.ClientConfig{})
	require.NotNil(t, clients.FactoryFor(oldUID, nil))
	require.NotNil(t, clients.FactoryFor(newUID, nil))
	options := newManagerOptions(scheme, metricsserver.Options{BindAddress: "0"}, "0", false, "default")
	mgr, err := ctrl.NewManager(config, options)
	require.NoError(t, err)
	require.NoError(t, addOpenBaoClientStateCleanup(mgr, clients, "default"))
	// Registered before Start so the helper stops the manager before this assertion.
	t.Cleanup(func() {
		require.Empty(t, clients.ClusterKeys())
		require.Nil(t, clients.FactoryFor(newUID, nil))
	})
	managerprobe.Start(t, mgr)
	require.Eventually(t, func() bool {
		keys := clients.ClusterKeys()
		return len(keys) == 1 && keys[0] == newUID
	}, 10*time.Second, 20*time.Millisecond)
}
