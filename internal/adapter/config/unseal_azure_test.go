package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func azureKeyVaultAuthMethodCluster(version, currentVersion, authMethod string) *openbaov1alpha1.OpenBaoCluster {
	cluster := newMinimalCluster("azure-auth-method", "default")
	cluster.Spec.Version = version
	cluster.Status.CurrentVersion = currentVersion
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "azurekeyvault",
		AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
			VaultName:  "my-vault",
			KeyName:    "my-key",
			ClientID:   "client-456",
			AuthMethod: authMethod,
		},
	}
	return cluster
}

func TestValidateAzureKeyVaultAuthMethod(t *testing.T) {
	tests := []struct {
		name           string
		version        string
		currentVersion string
		authMethod     string
		wantError      string
	}{
		{name: "2.6.0", version: "2.6.0", authMethod: "workload_identity"},
		{name: "2.6.3 with leading v", version: "v2.6.3", authMethod: "workload_identity"},
		{name: "upgrade completed", version: "2.6.3", currentVersion: "2.6.3", authMethod: "workload_identity"},
		{name: "unset on an older version", version: "2.4.4"},
		{name: "2.5.5", version: "2.5.5", authMethod: "workload_identity",
			wantError: "requires OpenBao >= 2.6.0"},
		{name: "2.4.4", version: "2.4.4", authMethod: "managed_identity",
			wantError: "requires OpenBao >= 2.6.0"},
		{name: "2.6 prerelease", version: "2.6.0-beta1", authMethod: "workload_identity",
			wantError: "requires OpenBao >= 2.6.0"},
		{name: "upgrade in progress", version: "2.6.3", currentVersion: "2.5.5", authMethod: "workload_identity",
			wantError: `complete the upgrade before configuring it (version="2.5.5")`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := azureKeyVaultAuthMethodCluster(tt.version, tt.currentVersion, tt.authMethod)
			err := validateAzureKeyVaultAuthMethod(cluster)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateAzureKeyVaultAuthMethod_WorkloadIdentity(t *testing.T) {
	for _, tt := range []struct {
		name      string
		version   string
		wantError string
	}{
		{name: "2.6.0", version: "2.6.0"},
		{name: "2.5.5", version: "2.5.5", wantError: "spec.unseal.azureKeyVault.workloadIdentity requires OpenBao >= 2.6.0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cluster := azureKeyVaultAuthMethodCluster(tt.version, "", "")
			cluster.Spec.Unseal.AzureKeyVault.ClientID = ""
			cluster.Spec.Unseal.AzureKeyVault.WorkloadIdentity = &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "client-456"}
			err := validateAzureKeyVaultAuthMethod(cluster)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRenderHCLRejectsAzureKeyVaultAuthMethodBefore260(t *testing.T) {
	cluster := azureKeyVaultAuthMethodCluster("2.5.5", "", "workload_identity")
	_, err := RenderHCL(cluster, InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	})
	require.ErrorContains(t, err, "spec.unseal.azureKeyVault.authMethod requires OpenBao >= 2.6.0")
}
