//go:build integration

package integration

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

// The API server defaults fields of the projected token volume that the operator leaves
// unset, so the recreation check must accept a StatefulSet read back from it and still
// catch an audience or path that differs from the spec.
func TestAzureWorkloadIdentityStatefulSetRoundTrip(t *testing.T) {
	namespace := newTestNamespace(t)
	cluster := newMinimalClusterObj(namespace, "azure-wi-roundtrip")
	cluster.Spec.Version = "2.6.0"
	cluster.Spec.Image = "openbao/openbao:2.6.0"
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "azurekeyvault",
		AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
			VaultName:        "vault",
			KeyName:          "key",
			TenantID:         "tenant-123",
			WorkloadIdentity: &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "client-456"},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	createTLSSecret(t, namespace, cluster.Name)

	controller := newControllerClient(t)
	require.NoError(t, reconcileClusterResources(ctx, logr.Discard(), controller, k8sScheme,
		cluster, newTestStatefulSetSpec(cluster)))

	stored := &appsv1.StatefulSet{}
	require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(cluster), stored))
	tokenVolume := -1
	for i, volume := range stored.Spec.Template.Spec.Volumes {
		if volume.Name == constants.VolumeAzureIdentityToken {
			tokenVolume = i
		}
	}
	require.GreaterOrEqual(t, tokenVolume, 0, "StatefulSet must project the Azure identity token")
	require.NotNil(t, stored.Spec.Template.Spec.Volumes[tokenVolume].Projected.DefaultMode,
		"the API server is expected to default the projected volume mode")
	require.False(t, workloadsvc.StatefulSetAzureWorkloadIdentityRequiresRecreate(cluster, stored),
		"a healthy StatefulSet read back from the API server must not require recreation")

	otherAudience := stored.DeepCopy()
	otherAudience.Spec.Template.Spec.Volumes[tokenVolume].Projected.Sources[0].ServiceAccountToken.Audience = "api://other"
	require.True(t, workloadsvc.StatefulSetAzureWorkloadIdentityRequiresRecreate(cluster, otherAudience))

	otherPath := stored.DeepCopy()
	otherPath.Spec.Template.Spec.Volumes[tokenVolume].Projected.Sources[0].ServiceAccountToken.Path = "other-token"
	require.True(t, workloadsvc.StatefulSetAzureWorkloadIdentityRequiresRecreate(cluster, otherPath))
}
