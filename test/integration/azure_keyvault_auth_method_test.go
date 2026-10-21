//go:build integration

package integration

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func azureKeyVaultUnseal(authMethod string) *openbaov1alpha1.UnsealConfig {
	return &openbaov1alpha1.UnsealConfig{
		Type: "azurekeyvault",
		AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
			VaultName:  "vault",
			KeyName:    "key",
			ClientID:   "client-456",
			AuthMethod: authMethod,
		},
	}
}

func TestVAP_OpenBaoCluster_AzureKeyVaultAuthMethod(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	tests := []struct {
		name       string
		version    string
		authMethod string
		wantError  string
	}{
		{name: "2.6.0", version: "2.6.0", authMethod: "workload_identity"},
		{name: "leading v and metadata", version: "v2.6.3+build.1", authMethod: "workload_identity"},
		{name: "unset on 2.4.4", version: "2.4.4"},
		{name: "2.5.5", version: "2.5.5", authMethod: "workload_identity", wantError: "requires OpenBao >= 2.6.0"},
		{name: "2.4.4", version: "2.4.4", authMethod: "managed_identity", wantError: "requires OpenBao >= 2.6.0"},
		{name: "2.6 prerelease", version: "2.6.0-beta1", authMethod: "workload_identity",
			wantError: "requires OpenBao >= 2.6.0"},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(namespace, fmt.Sprintf("azure-auth-method-%d", index))
			cluster.Spec.Version = tt.version
			cluster.Spec.Unseal = azureKeyVaultUnseal(tt.authMethod)
			err := k8sClient.Create(ctx, cluster, client.DryRunAll)
			if tt.wantError != "" {
				requireAdmissionDenied(t, err)
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestVAP_OpenBaoCluster_AzureKeyVaultAuthMethodUpgrade(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	cluster := newMinimalClusterObj(namespace, "azure-auth-method-upgrade")
	cluster.Spec.Version = "2.5.5"
	cluster.Spec.Unseal = azureKeyVaultUnseal("")
	require.NoError(t, k8sClient.Create(ctx, cluster))
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) { status.CurrentVersion = "2.5.5" })
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cluster.Name}, cluster))

	cluster.Spec.Version = "2.6.3"
	cluster.Spec.Unseal.AzureKeyVault.AuthMethod = "workload_identity"
	err := k8sClient.Update(ctx, cluster, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "Complete the OpenBao >= 2.6.0 upgrade")

	cluster.Spec.Unseal.AzureKeyVault.AuthMethod = ""
	require.NoError(t, k8sClient.Update(ctx, cluster))
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) { status.CurrentVersion = "2.6.3" })
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cluster.Name}, cluster))
	cluster.Spec.Unseal.AzureKeyVault.AuthMethod = "workload_identity"
	require.NoError(t, k8sClient.Update(ctx, cluster))
}

type azureKeyVaultMutation = func(*openbaov1alpha1.AzureKeyVaultSealConfig)

func azureKeyVaultWorkloadIdentityUnseal(mutate azureKeyVaultMutation) *openbaov1alpha1.UnsealConfig {
	cfg := &openbaov1alpha1.AzureKeyVaultSealConfig{
		VaultName:        "vault",
		KeyName:          "key",
		TenantID:         "tenant-123",
		WorkloadIdentity: &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "client-456"},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return &openbaov1alpha1.UnsealConfig{Type: "azurekeyvault", AzureKeyVault: cfg}
}

func TestCRD_AzureKeyVaultWorkloadIdentity(t *testing.T) {
	namespace := newTestNamespace(t)
	tests := []struct {
		name      string
		unseal    *openbaov1alpha1.UnsealConfig
		wantError string
	}{
		{name: "released configuration without workloadIdentity", unseal: azureKeyVaultUnseal("")},
		{name: "released configuration with a client secret", unseal: &openbaov1alpha1.UnsealConfig{
			Type: "azurekeyvault",
			AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
				VaultName: "vault", KeyName: "key", TenantID: "tenant-123",
				ClientID: "client-456", ClientSecret: "secret",
			},
		}},
		{name: "workloadIdentity", unseal: azureKeyVaultWorkloadIdentityUnseal(nil)},
		{
			name: "workloadIdentity with authMethod workload_identity",
			unseal: azureKeyVaultWorkloadIdentityUnseal(func(c *openbaov1alpha1.AzureKeyVaultSealConfig) {
				c.AuthMethod = "workload_identity"
			}),
		},
		{
			name: "workloadIdentity without tenantID",
			unseal: azureKeyVaultWorkloadIdentityUnseal(func(c *openbaov1alpha1.AzureKeyVaultSealConfig) {
				c.TenantID = ""
			}),
			wantError: "workloadIdentity requires tenantID",
		},
		{
			name: "workloadIdentity with clientID",
			unseal: azureKeyVaultWorkloadIdentityUnseal(func(c *openbaov1alpha1.AzureKeyVaultSealConfig) {
				c.ClientID = "client-789"
			}),
			wantError: "excludes clientID and clientSecret",
		},
		{
			name: "workloadIdentity with clientSecret",
			unseal: azureKeyVaultWorkloadIdentityUnseal(func(c *openbaov1alpha1.AzureKeyVaultSealConfig) {
				c.ClientSecret = "secret"
			}),
			wantError: "excludes clientID and clientSecret",
		},
		{
			name: "workloadIdentity with another authMethod",
			unseal: azureKeyVaultWorkloadIdentityUnseal(func(c *openbaov1alpha1.AzureKeyVaultSealConfig) {
				c.AuthMethod = "managed_identity"
			}),
			wantError: "requires authMethod workload_identity or unset",
		},
		{
			name: "workloadIdentity with credentialsSecretRef for non-auth settings",
			unseal: func() *openbaov1alpha1.UnsealConfig {
				unseal := azureKeyVaultWorkloadIdentityUnseal(nil)
				unseal.CredentialsSecretRef = &corev1.LocalObjectReference{Name: "azure-settings"}
				return unseal
			}(),
		},
		{
			name: "workloadIdentity without clientID",
			unseal: azureKeyVaultWorkloadIdentityUnseal(func(c *openbaov1alpha1.AzureKeyVaultSealConfig) {
				c.WorkloadIdentity.ClientID = ""
			}),
			wantError: "clientID",
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(namespace, fmt.Sprintf("azure-workload-identity-%d", index))
			cluster.Spec.Version = "2.6.3"
			cluster.Spec.Unseal = tt.unseal
			err := k8sClient.Create(ctx, cluster, client.DryRunAll)
			if tt.wantError != "" {
				requireInvalidRequest(t, err)
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestVAP_OpenBaoCluster_AzureKeyVaultWorkloadIdentityVersion(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	for index, tt := range []struct {
		version   string
		wantError string
	}{
		{version: "2.6.0"},
		{version: "2.5.5", wantError: "requires OpenBao >= 2.6.0"},
	} {
		t.Run(tt.version, func(t *testing.T) {
			cluster := newMinimalClusterObj(namespace, fmt.Sprintf("azure-workload-identity-version-%d", index))
			cluster.Spec.Version = tt.version
			cluster.Spec.Unseal = azureKeyVaultWorkloadIdentityUnseal(nil)
			err := k8sClient.Create(ctx, cluster, client.DryRunAll)
			if tt.wantError != "" {
				requireAdmissionDenied(t, err)
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestVAP_OpenBaoCluster_AzureKeyVaultWorkloadIdentityRequiresCloudIdentityAuthorization(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	username := "azure-seal-identity-editor"
	tenantClient := newImpersonatedClient(t, username)
	grantTenantOpenBaoWriteAccess(t, namespace, username)

	denied := newMinimalClusterObj(namespace, "azure-seal-identity-denied")
	denied.Spec.InitContainer = nil
	denied.Spec.Version = "2.6.3"
	denied.Spec.Unseal = azureKeyVaultWorkloadIdentityUnseal(nil)
	err := tenantClient.Create(ctx, denied, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "use cloud identities")

	clusterName := "azure-seal-identity-allowed"
	grantClusterCloudIdentitiesAccess(t, namespace, clusterName, username)
	allowed := newMinimalClusterObj(namespace, clusterName)
	allowed.Spec.InitContainer = nil
	allowed.Spec.Version = "2.6.3"
	allowed.Spec.Unseal = azureKeyVaultWorkloadIdentityUnseal(nil)
	require.NoError(t, tenantClient.Create(ctx, allowed, client.DryRunAll))
}
