package config

import (
	"fmt"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	platformsemver "github.com/kubebao/openbao-operator/internal/platform/semver"
)

// azureKeyVaultAuthMethod returns the seal's auth_method: the one set in
// spec.unseal.azureKeyVault.authMethod, or workload_identity when the operator
// wires the workload identity itself (spec.unseal.azureKeyVault.workloadIdentity).
func azureKeyVaultAuthMethod(cfg *openbaov1alpha1.AzureKeyVaultSealConfig) string {
	if cfg.AuthMethod == "" && cfg.WorkloadIdentity != nil {
		return "workload_identity"
	}
	return cfg.AuthMethod
}

// validateAzureKeyVaultAuthMethod rejects an auth_method, set through
// spec.unseal.azureKeyVault.authMethod or implied by its workloadIdentity, for
// OpenBao versions whose Azure Key Vault seal ignores auth_method. Before
// 2.6.0, the seal selects managed identity whenever a client ID is present
// without a client secret, so rendering the selector would not change the
// credential. The running version must support it too, so an in-progress
// upgrade never hands the selector to an older server.
func validateAzureKeyVaultAuthMethod(cluster *openbaov1alpha1.OpenBaoCluster) error {
	if cluster.Spec.Unseal == nil || cluster.Spec.Unseal.AzureKeyVault == nil ||
		azureKeyVaultAuthMethod(cluster.Spec.Unseal.AzureKeyVault) == "" {
		return nil
	}
	field := "spec.unseal.azureKeyVault.authMethod"
	if cluster.Spec.Unseal.AzureKeyVault.AuthMethod == "" {
		field = "spec.unseal.azureKeyVault.workloadIdentity"
	}
	versions := []string{cluster.Spec.Version}
	if cluster.Status.CurrentVersion != "" {
		versions = append(versions, cluster.Status.CurrentVersion)
	}
	for _, version := range versions {
		supported, err := platformsemver.AtLeast(version, 2, 6, 0)
		if err != nil {
			return fmt.Errorf("validate Azure Key Vault auth method version: %w", err)
		}
		if !supported {
			return fmt.Errorf("%s requires OpenBao >= 2.6.0; complete the upgrade before configuring it (version=%q)", field, version)
		}
	}
	return nil
}
