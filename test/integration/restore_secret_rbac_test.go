//go:build integration

package integration

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/provisioner"
)

func TestRestoreTenantSecretRBAC_WithWorkloadIdentityUnseal(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "openbao-operator-system")
	t.Setenv("OPERATOR_SERVICE_ACCOUNT_NAME", "openbao-operator-controller")
	ensureDefaultAdmissionPoliciesApplied(t)
	ensureProvisionerRBACApplied(t)
	namespace := newTestNamespace(t)
	setRestoreDestinationApproval(t, namespace, "true")

	cluster := newMinimalClusterObj(namespace, "existing")
	cluster.Spec.Unseal = &api.UnsealConfig{
		Type: "static", CredentialsSecretRef: &corev1.LocalObjectReference{Name: "existing-seal"},
	}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	request := newFreshRestoreRequest(namespace, "identity-target")
	request.Spec.Source.Target.CredentialsSecretRef = &corev1.LocalObjectReference{Name: "snapshot-creds"}
	template := request.Spec.ClusterTemplate
	template.Unseal = api.UnsealConfig{Type: "azurekeyvault", AzureKeyVault: &api.AzureKeyVaultSealConfig{
		VaultName: "recovery", KeyName: "snapshot-key", AuthMethod: "workload_identity",
	}}
	template.Plugins = []api.Plugin{{Type: "kms", Name: "azurekeyvault", Image: "registry.example.com/azure-kms:v1"}}
	template.ServiceAccount = &api.ServiceAccountConfig{
		Annotations: map[string]string{"azure.workload.identity/client-id": "00000000-0000-0000-0000-000000000001"},
	}
	template.PodMetadata = &api.PodMetadataConfig{Labels: map[string]string{"azure.workload.identity/use": "true"}}
	template.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "registry-creds"}}
	require.NoError(t, k8sClient.Create(ctx, request))

	// Reload the admitted request so the provisioner sees the stored API shape.
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request))
	require.Nil(t, request.Spec.ClusterTemplate.Unseal.CredentialsSecretRef)
	manager, err := provisioner.NewManager(newImpersonatedClient(t, provisionerUsername), logr.Discard())
	require.NoError(t, err)
	require.NoError(t, manager.EnsureTenantSecretRBAC(ctx, namespace))
	// A second reconciliation must preserve both clusters' allowlists.
	require.NoError(t, manager.EnsureTenantSecretRBAC(ctx, namespace))

	reader := &rbacv1.Role{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{
		Namespace: namespace, Name: provisioner.TenantSecretsReaderRoleName,
	}, reader))
	require.Equal(t, []rbacv1.PolicyRule{{
		APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"},
		ResourceNames: []string{"existing-seal", "registry-creds", "snapshot-creds"},
	}}, reader.Rules)
	writer := &rbacv1.Role{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{
		Namespace: namespace, Name: provisioner.TenantSecretsWriterRoleName,
	}, writer))
	require.Len(t, writer.Rules, 2)
	require.Contains(t, writer.Rules[1].ResourceNames, cluster.Name+constants.SuffixTLSServer)
	for roleName, bindingName := range map[string]string{
		provisioner.TenantSecretsReaderRoleName: provisioner.TenantSecretsReaderRoleBindingName,
		provisioner.TenantSecretsWriterRoleName: provisioner.TenantSecretsWriterRoleBindingName,
	} {
		binding := &rbacv1.RoleBinding{}
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: bindingName}, binding))
		require.Equal(t, roleName, binding.RoleRef.Name)
		require.Equal(t, []rbacv1.Subject{{
			Kind: "ServiceAccount", Name: "openbao-operator-controller", Namespace: "openbao-operator-system",
		}}, binding.Subjects)
	}
}
