//go:build integration

package integration

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/service/certs"
)

func TestMalformedCAKeyReturnsErrorAndRecovers(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)
	cluster := newMinimalClusterObj(namespace, "invalid-ca-key")
	require.NoError(t, k8sClient.Create(ctx, cluster))
	controller := newControllerClient(t)
	manager := certs.NewManagerWithReloader(controller, k8sScheme, nil)
	_, err := manager.Reconcile(ctx, logr.Discard(), cluster)
	require.NoError(t, err)
	caKey := client.ObjectKey{Namespace: namespace, Name: cluster.Name + "-tls-ca"}
	serverKey := client.ObjectKey{Namespace: namespace, Name: cluster.Name + "-tls-server"}
	ca := &corev1.Secret{}
	server := &corev1.Secret{}
	require.NoError(t, controller.Get(ctx, caKey, ca))
	require.NoError(t, controller.Get(ctx, serverKey, server))
	originalKey := ca.Data["ca.key"]
	ca.Data["ca.key"] = []byte("malformed-private-key-canary")
	require.NoError(t, controller.Update(ctx, ca))
	require.NotPanics(t, func() {
		_, err = manager.Reconcile(ctx, logr.Discard(), cluster)
	})
	require.ErrorContains(t, err, "failed to parse CA secret "+namespace+"/"+ca.Name)
	require.ErrorContains(t, err, "failed to parse CA private key: failed to decode ECDSA private key PEM")
	require.NotContains(t, err.Error(), "malformed-private-key-canary")
	currentServer := &corev1.Secret{}
	require.NoError(t, controller.Get(ctx, serverKey, currentServer))
	require.Equal(t, server.ResourceVersion, currentServer.ResourceVersion, "invalid CA input must not reissue the server certificate")
	require.NoError(t, controller.Get(ctx, caKey, ca))
	ca.Data["ca.key"] = originalKey
	require.NoError(t, controller.Update(ctx, ca))
	_, err = manager.Reconcile(ctx, logr.Discard(), cluster)
	require.NoError(t, err, "reconciliation must recover after the CA key is repaired")
}
