//go:build integration

package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestCRD_RestoreTargetUnsealProviders(t *testing.T) {
	namespace := newTestNamespace(t)
	setRestoreDestinationApproval(t, namespace, "true")
	providers := []api.UnsealConfig{
		{Type: "static", CredentialsSecretRef: &corev1.LocalObjectReference{Name: "original-key"}},
		{Type: "transit", Transit: &api.TransitSealConfig{Address: "https://seal.example", KeyName: "original", MountPath: "transit"}, CredentialsSecretRef: &corev1.LocalObjectReference{Name: "transit"}},
		{Type: "awskms", AWSKMS: &api.AWSKMSSealConfig{Region: "eu-west-1", KMSKeyID: "original"}},
		{Type: "azurekeyvault", AzureKeyVault: &api.AzureKeyVaultSealConfig{VaultName: "vault", KeyName: "original"}},
		{Type: "gcpckms", GCPCloudKMS: &api.GCPCloudKMSSealConfig{Project: "project", Region: "global", KeyRing: "ring", CryptoKey: "original"}},
		{Type: "kmip", KMIP: &api.KMIPSealConfig{Endpoint: "hsm.example:5696", KMSKeyID: "original", ClientCert: "/etc/bao/seal-creds/client.crt", ClientKey: "/etc/bao/seal-creds/client.key"}},
		{Type: "ocikms", OCIKMS: &api.OCIKMSSealConfig{KeyID: "original", CryptoEndpoint: "https://crypto.example", ManagementEndpoint: "https://management.example"}},
		{Type: "pkcs11", PKCS11: &api.PKCS11SealConfig{Lib: "/usr/lib/vendor.so", TokenLabel: "token", KeyLabel: "original"}},
		{Type: "kms", KMS: &api.KMSPluginSealConfig{PluginName: "hsm"}},
	}
	for _, unseal := range providers {
		t.Run(unseal.Type, func(t *testing.T) {
			request := newFreshRestoreRequest(namespace, unseal.Type)
			request.Spec.ClusterTemplate.Unseal = unseal
			switch unseal.Type {
			case "kms":
				request.Spec.ClusterTemplate.Plugins = []api.Plugin{{Type: "kms", Name: "hsm", Command: "hsm-plugin"}}
			case "awskms", "azurekeyvault", "gcpckms", "ocikms", "pkcs11":
				require.ErrorContains(t, k8sClient.Create(ctx, request.DeepCopy(), client.DryRunAll), "matching KMS plugin")
				request.Spec.ClusterTemplate.Plugins = []api.Plugin{{Type: "kms", Name: unseal.Type, Command: "seal-plugin"}}
			}
			require.NoError(t, k8sClient.Create(ctx, request, client.DryRunAll))
			source := restoreTestSource(namespace, request.Spec.ClusterTemplate)
			require.NoError(t, k8sClient.Create(ctx, source, client.DryRunAll))
		})
	}

	request := newFreshRestoreRequest(namespace, "missing-key")
	request.Spec.ClusterTemplate.Unseal = api.UnsealConfig{Type: "static"}
	require.ErrorContains(t, k8sClient.Create(ctx, request, client.DryRunAll), "static restore targets require")
	request.Spec.ClusterTemplate.Unseal.Type = "awskms"
	require.ErrorContains(t, k8sClient.Create(ctx, request, client.DryRunAll), "matching configuration block")
}

func TestVAP_RestoreTargetUnsealAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, group, resource, verb, message string
		edit                                 func(*api.RestoreClusterTemplate)
	}{
		{name: "credentials", resource: "secrets", verb: "get", message: "unseal credential Secret", edit: func(c *api.RestoreClusterTemplate) {
			c.Unseal.CredentialsSecretRef = &corev1.LocalObjectReference{Name: "seal-auth"}
		}},
		{name: "service-account", resource: "serviceaccounts", verb: "use", message: "destination ServiceAccount", edit: func(c *api.RestoreClusterTemplate) {
			c.ServiceAccount = &api.ServiceAccountConfig{Name: "seal-identity"}
		}},
		{name: "service-account-identity", group: "openbao.org", resource: "openbaoclusters", verb: "usecloudidentities", message: "cloud identity delegation", edit: func(c *api.RestoreClusterTemplate) {
			c.ServiceAccount = &api.ServiceAccountConfig{Annotations: map[string]string{"eks.amazonaws.com/role-arn": "arn:aws:iam::123456789012:role/recovery"}}
		}},
		{name: "pod-identity", group: "openbao.org", resource: "openbaoclusters", verb: "usecloudidentities", message: "cloud identity delegation", edit: func(c *api.RestoreClusterTemplate) {
			c.PodMetadata = &api.PodMetadataConfig{Labels: map[string]string{"azure.workload.identity/use": "true"}}
		}},
		{name: "plugin", group: "openbao.org", resource: "openbaoclusters", verb: "usecustomexecutables", message: "executable delegation", edit: func(c *api.RestoreClusterTemplate) {
			c.Unseal = api.UnsealConfig{Type: "kms", KMS: &api.KMSPluginSealConfig{PluginName: "hsm"}}
			c.Plugins = []api.Plugin{{Type: "kms", Name: "hsm", Command: "hsm-plugin"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sourceNS, destination := newTestNamespace(t), newTestNamespace(t)
			setRestoreDestinationApproval(t, destination, "true")
			waitForOpenBaoClusterAdmissionPolicies(t, sourceNS)
			username := "seal-author-" + sourceNS
			grantTenantOpenBaoWriteAccess(t, sourceNS, username)
			grantTenantOpenBaoWriteAccess(t, destination, username)
			grantNamespacedResourceVerbs(t, destination, username, "restore", "openbao.org", "openbaoclusters", nil, "restore")
			grantNamespacedResourceVerbs(t, destination, username, "storage-reader", "", "serviceaccounts", []string{"snapshot-reader"}, "use")
			actor := newImpersonatedClient(t, username)
			request := newFreshRestoreRequest(destination, "recovery")
			request.Spec.ClusterTemplate.Unseal = api.UnsealConfig{Type: "kmip", KMIP: &api.KMIPSealConfig{Endpoint: "hsm.example:5696", KMSKeyID: "original", ClientCert: "/certs/client.crt", ClientKey: "/certs/client.key"}}
			// Providers using ambient or workload identity credentials do not need a Secret grant.
			require.NoError(t, actor.Create(ctx, request.DeepCopy(), client.DryRunAll))
			tc.edit(request.Spec.ClusterTemplate)
			source := restoreTestSource(sourceNS, request.Spec.ClusterTemplate)
			source.Spec.Backup.RestoreTest.Namespace = destination
			for _, object := range []client.Object{request, source} {
				require.ErrorContains(t, actor.Create(ctx, object, client.DryRunAll), tc.message)
			}

			grantNamespacedResourceVerbs(t, destination, username, "seal-access", tc.group, tc.resource, nil, tc.verb)
			for _, object := range []client.Object{request, source} {
				require.NoError(t, actor.Create(ctx, object, client.DryRunAll))
			}
		})
	}
}

func restoreTestSource(namespace string, template *api.RestoreClusterTemplate) *api.OpenBaoCluster {
	source := newMinimalClusterObj(namespace, "source")
	source.Spec.InitContainer = nil
	source.Spec.Backup = &api.BackupSchedule{
		Schedule: "0 0 * * *", JWTAuthRole: "backup",
		Target: api.BackupTarget{Bucket: "snapshots", Endpoint: "https://storage.example"},
		RestoreTest: &api.RestoreTest{EverySuccessfulBackups: 1, Namespace: namespace,
			ServiceAccountName: "snapshot-reader", ClusterTemplate: *template.DeepCopy()},
	}
	return source
}
