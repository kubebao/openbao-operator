package provisioner

import (
	"context"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestAccumulateRestoreTenantSecretNames_OptionalUnsealCredentials(t *testing.T) {
	for _, seal := range []api.UnsealConfig{
		{Type: "azurekeyvault", AzureKeyVault: &api.AzureKeyVaultSealConfig{
			VaultName: "recovery", KeyName: "snapshot-key", AuthMethod: "workload_identity",
		}},
		{Type: "awskms", AWSKMS: &api.AWSKMSSealConfig{Region: "eu-west-1", KMSKeyID: "snapshot-key"}},
		{Type: "gcpckms", GCPCloudKMS: &api.GCPCloudKMSSealConfig{
			Project: "recovery", Region: "europe-west1", KeyRing: "recovery", CryptoKey: "snapshot-key",
		}},
		{Type: "kms", KMS: &api.KMSPluginSealConfig{
			PluginName: "custom-kms", Config: map[string]string{"key_id": "snapshot-key"},
		}},
	} {
		for _, credentials := range []struct {
			name string
			ref  *corev1.LocalObjectReference
		}{
			{name: "workload-identity"},
			{name: "secret", ref: &corev1.LocalObjectReference{Name: "seal-creds"}},
		} {
			t.Run(seal.Type+"/"+credentials.name, func(t *testing.T) {
				unseal := seal.DeepCopy()
				unseal.CredentialsSecretRef = credentials.ref
				request := &api.OpenBaoRestore{Spec: api.OpenBaoRestoreSpec{
					Cluster: "target",
					Source: api.RestoreSource{Target: api.BackupTarget{
						CredentialsSecretRef: &corev1.LocalObjectReference{Name: "snapshot-creds"},
					}},
					TokenSecretRef: &corev1.LocalObjectReference{Name: "restore-token"},
					ClusterTemplate: &api.RestoreClusterTemplate{
						Unseal: *unseal, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry-creds"}},
					},
				}}
				reader := map[string]struct{}{}
				accumulateRestoreTenantSecretNames(request, nil, reader)
				want := []string{"registry-creds", "restore-token", "snapshot-creds"}
				if credentials.ref != nil {
					want = []string{"registry-creds", "restore-token", "seal-creds", "snapshot-creds"}
				}
				if got := sortedUniqueSecretNames(reader); !reflect.DeepEqual(got, want) {
					t.Fatalf("restore reader allowlist = %v, want %v", got, want)
				}
			})
		}
	}
}

func TestEnsureTenantSecretRBAC_MixedRestoreCredentials(t *testing.T) {
	cluster := &api.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: testNamespace},
		Spec: api.OpenBaoClusterSpec{Unseal: &api.UnsealConfig{
			Type: "static", CredentialsSecretRef: &corev1.LocalObjectReference{Name: "existing-seal"},
		}},
	}
	identityRestore := &api.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "identity-restore", Namespace: testNamespace},
		Spec: api.OpenBaoRestoreSpec{Cluster: "identity-target", ClusterTemplate: &api.RestoreClusterTemplate{
			Unseal: api.UnsealConfig{Type: "azurekeyvault", AzureKeyVault: &api.AzureKeyVaultSealConfig{
				VaultName: "recovery", KeyName: "snapshot-key", AuthMethod: "workload_identity",
			}},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry-creds"}},
		}},
	}
	secretRestore := &api.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "secret-restore", Namespace: testNamespace},
		Spec: api.OpenBaoRestoreSpec{
			Cluster: "secret-target",
			ClusterTemplate: &api.RestoreClusterTemplate{Unseal: api.UnsealConfig{
				Type: "static", CredentialsSecretRef: &corev1.LocalObjectReference{Name: "snapshot-seal"},
			}},
			Source: api.RestoreSource{Target: api.BackupTarget{
				CredentialsSecretRef: &corev1.LocalObjectReference{Name: "snapshot-creds"},
			}},
			TokenSecretRef: &corev1.LocalObjectReference{Name: "restore-token"},
		},
	}
	otherNamespace := secretRestore.DeepCopy()
	otherNamespace.Namespace = "other-namespace"
	otherNamespace.Spec.Source.Target.CredentialsSecretRef.Name = "unrelated-creds"

	k8sClient := newTestClient(t, cluster, identityRestore, secretRestore, otherNamespace)
	manager, err := NewManager(k8sClient, logr.Discard())
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}
	if err := manager.EnsureTenantSecretRBAC(context.Background(), testNamespace); err != nil {
		t.Fatalf("EnsureTenantSecretRBAC() error = %v", err)
	}

	role := &rbacv1.Role{}
	key := types.NamespacedName{Namespace: testNamespace, Name: TenantSecretsReaderRoleName}
	if err := k8sClient.Get(context.Background(), key, role); err != nil {
		t.Fatalf("get reader Role: %v", err)
	}
	want := []string{"existing-seal", "registry-creds", "restore-token", "snapshot-creds", "snapshot-seal"}
	if got := extractSecretResourceNames(role.Rules); !reflect.DeepEqual(got, want) {
		t.Fatalf("reader Role allowlist = %v, want %v", got, want)
	}
}
