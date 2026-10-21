package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func (m *Manager) validateAWSKMSUnsealPrerequisites(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) error {
	cfg := cluster.Spec.Unseal
	if cfg == nil || cfg.AWSKMS == nil || cfg.CredentialsSecretRef == nil {
		return nil
	}

	secret, err := m.credentialsSecret(ctx, cluster, "awskms")
	if err != nil {
		return providerPrerequisitesError(err)
	}

	if err := requireSecretKeysTogether(secret.Data, cluster.Namespace, cfg.CredentialsSecretRef.Name, "awskms credentials", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"); err != nil {
		return providerPrerequisitesError(err)
	}

	return nil
}

func (m *Manager) validateAzureKeyVaultUnsealPrerequisites(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) error {
	cfg := cluster.Spec.Unseal
	if cfg == nil || cfg.AzureKeyVault == nil || cfg.CredentialsSecretRef == nil {
		return nil
	}

	secret, err := m.credentialsSecret(ctx, cluster, "azurekeyvault")
	if err != nil {
		return providerPrerequisitesError(err)
	}
	// With workloadIdentity the Secret only supplies optional settings such as
	// AZURE_ENVIRONMENT; the Pods authenticate with the projected token.
	if cfg.AzureKeyVault.WorkloadIdentity != nil {
		return nil
	}

	if err := requireSecretKeys(secret.Data, cluster.Namespace, cfg.CredentialsSecretRef.Name, "azurekeyvault credentials", "AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET"); err != nil {
		return providerPrerequisitesError(err)
	}

	return nil
}

func (m *Manager) validateOCIKMSUnsealPrerequisites(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) error {
	cfg := cluster.Spec.Unseal
	if cfg == nil || cfg.OCIKMS == nil {
		return nil
	}

	usesAPIKey := cfg.OCIKMS.AuthTypeAPIKey != nil && *cfg.OCIKMS.AuthTypeAPIKey
	if cfg.CredentialsSecretRef != nil && !usesAPIKey {
		return providerPrerequisitesError(
			fmt.Errorf("ocikms credentials Secret requires spec.unseal.ocikms.authTypeAPIKey=true"),
		)
	}
	if !usesAPIKey || cfg.CredentialsSecretRef == nil {
		return nil
	}

	secret, err := m.credentialsSecret(ctx, cluster, "ocikms")
	if err != nil {
		return providerPrerequisitesError(err)
	}

	configData := secret.Data["config"]
	if len(strings.TrimSpace(string(configData))) == 0 {
		return providerPrerequisitesError(missingSecretKeyError(cluster.Namespace, cfg.CredentialsSecretRef.Name, "ocikms", "config"))
	}

	configFields, err := parseOCIKMSDefaultProfile(configData)
	if err != nil {
		return providerPrerequisitesError(
			fmt.Errorf("ocikms credentials Secret %s/%s has invalid OCI SDK config in key %q: %w", cluster.Namespace, cfg.CredentialsSecretRef.Name, "config", err),
		)
	}

	for _, key := range []string{"user", "fingerprint", "tenancy", "region", "key_file"} {
		if strings.TrimSpace(configFields[key]) == "" {
			return providerPrerequisitesError(
				fmt.Errorf("ocikms credentials Secret %s/%s OCI SDK config must define %q in profile [DEFAULT]", cluster.Namespace, cfg.CredentialsSecretRef.Name, key),
			)
		}
	}

	keyFileKey, ok := mountedSealCredentialsKey(configFields["key_file"])
	if !ok {
		return providerPrerequisitesError(
			fmt.Errorf("ocikms credentials Secret %s/%s OCI SDK config key_file must reference a file under %s", cluster.Namespace, cfg.CredentialsSecretRef.Name, sealCredsVolumeMountPath),
		)
	}
	if len(strings.TrimSpace(string(secret.Data[keyFileKey]))) == 0 {
		return providerPrerequisitesError(
			fmt.Errorf("ocikms credentials Secret %s/%s is missing required key %q referenced by OCI SDK config key_file", cluster.Namespace, cfg.CredentialsSecretRef.Name, keyFileKey),
		)
	}

	return nil
}

func (m *Manager) validateGCPCKMSUnsealPrerequisites(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) error {
	cfg := cluster.Spec.Unseal
	if cfg == nil || cfg.GCPCloudKMS == nil {
		return nil
	}

	credentialsPath := strings.TrimSpace(cfg.GCPCloudKMS.Credentials)
	if credentialsPath == "" {
		return nil
	}

	key, usesMountedSecret := mountedSealCredentialsKey(credentialsPath)
	if !usesMountedSecret {
		return nil
	}

	secret, err := m.credentialsSecret(ctx, cluster, "gcpckms")
	if err != nil {
		return providerPrerequisitesError(err)
	}

	data, ok := secret.Data[key]
	if !ok || len(strings.TrimSpace(string(data))) == 0 {
		return providerPrerequisitesError(missingSecretKeyError(cluster.Namespace, cfg.CredentialsSecretRef.Name, "gcpckms", key))
	}

	if !json.Valid(data) {
		return providerPrerequisitesError(
			fmt.Errorf("spec.unseal.gcpCloudKMS.credentials (%s) must contain valid JSON credentials", key),
		)
	}

	return nil
}
