package provisioner

import (
	"context"
	"fmt"
	"sort"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	clusterpkg "github.com/kubebao/openbao-operator/internal/adapter/cluster"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
)

// EnsureTenantSecretRBAC ensures tenant Secret access is reduced to explicit allowlists.
//
// This creates/updates two Roles in the tenant namespace:
// - TenantSecretsWriterRoleName: write access to operator-owned Secret names.
// - TenantSecretsReaderRoleName: read-only access to user-provided Secret names referenced by specs.
//
// Both Roles are bound to the operator controller ServiceAccount via RoleBindings.
func (m *Manager) EnsureTenantSecretRBAC(ctx context.Context, namespace string) error {
	if namespace == "" {
		return fmt.Errorf("namespace is required")
	}

	clusterList := &openbaov1alpha1.OpenBaoClusterList{}
	if err := m.client.List(ctx, clusterList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list OpenBaoClusters in namespace %s: %w", namespace, err)
	}

	restoreList := &openbaov1alpha1.OpenBaoRestoreList{}
	if err := m.client.List(ctx, restoreList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list OpenBaoRestores in namespace %s: %w", namespace, err)
	}

	writerNames := map[string]struct{}{}
	readerNames := map[string]struct{}{}
	clustersByName := map[string]*openbaov1alpha1.OpenBaoCluster{}
	for i := range clusterList.Items {
		accumulateTenantSecretNames(&clusterList.Items[i], writerNames, readerNames)
		clustersByName[clusterList.Items[i].Name] = &clusterList.Items[i]
	}
	for i := range restoreList.Items {
		accumulateRestoreTenantSecretNames(&restoreList.Items[i], clustersByName, readerNames)
	}

	type secretsRBACSpec struct {
		roleName           string
		roleBindingName    string
		secretNames        []string
		roleFactory        secretsRoleFactory
		roleBindingFactory secretsRoleBindingFactory
	}

	desired := []secretsRBACSpec{
		{
			roleName:           TenantSecretsWriterRoleName,
			roleBindingName:    TenantSecretsWriterRoleBindingName,
			secretNames:        sortedUniqueSecretNames(writerNames),
			roleFactory:        GenerateTenantSecretsWriterRole,
			roleBindingFactory: GenerateTenantSecretsWriterRoleBinding,
		},
		{
			roleName:           TenantSecretsReaderRoleName,
			roleBindingName:    TenantSecretsReaderRoleBindingName,
			secretNames:        sortedUniqueSecretNames(readerNames),
			roleFactory:        GenerateTenantSecretsReaderRole,
			roleBindingFactory: GenerateTenantSecretsReaderRoleBinding,
		},
	}

	for _, spec := range desired {
		if err := m.ensureSecretsRole(ctx, namespace, spec.roleName, spec.secretNames, spec.roleFactory); err != nil {
			return err
		}
		if err := m.ensureSecretsRoleBinding(ctx, namespace, spec.roleBindingName, spec.roleName, len(spec.secretNames) > 0, spec.roleBindingFactory); err != nil {
			return err
		}
	}

	return nil
}

func accumulateTenantSecretNames(cluster *openbaov1alpha1.OpenBaoCluster, writer, reader map[string]struct{}) {
	if cluster == nil || cluster.Name == "" {
		return
	}
	for _, perm := range clusterpkg.GetRequiredSecretPermissions(cluster) {
		if perm.Permission == clusterpkg.PermissionWrite {
			writer[perm.Name] = struct{}{}
			continue
		}
		reader[perm.Name] = struct{}{}
	}
}

func accumulateRestoreTenantSecretNames(restore *openbaov1alpha1.OpenBaoRestore, clustersByName map[string]*openbaov1alpha1.OpenBaoCluster, reader map[string]struct{}) {
	if restore == nil {
		return
	}
	if ref := restore.Spec.Source.Target.CredentialsSecretRef; ref != nil {
		reader[ref.Name] = struct{}{}
	}
	if template := restore.Spec.ClusterTemplate; template != nil {
		if ref := template.Unseal.CredentialsSecretRef; ref != nil {
			reader[ref.Name] = struct{}{}
		}
		for _, ref := range template.ImagePullSecrets {
			reader[ref.Name] = struct{}{}
		}
	}
	if restoreUsesStaticTokenAuth(restore, clustersByName[restore.Spec.Cluster]) {
		reader[restore.Spec.TokenSecretRef.Name] = struct{}{}
	}
}

func restoreUsesStaticTokenAuth(restore *openbaov1alpha1.OpenBaoRestore, cluster *openbaov1alpha1.OpenBaoCluster) bool {
	if restore == nil || restore.Spec.TokenSecretRef == nil || restore.Spec.TokenSecretRef.Name == "" {
		return false
	}

	return portauth.EffectiveJWTRole(
		restore.Spec.JWTAuthRole,
		cluster != nil && portauth.OperatorJWTBootstrapEnabled(cluster),
		portauth.RoleNameRestore,
	) == ""
}

func sortedUniqueSecretNames(names map[string]struct{}) []string {
	if len(names) == 0 {
		return nil
	}

	out := make([]string, 0, len(names))
	for name := range names {
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

type secretsRoleFactory func(namespace string, secretNames []string) *rbacv1.Role

func (m *Manager) ensureSecretsRole(ctx context.Context, namespace string, roleName string, secretNames []string, factory secretsRoleFactory) error {
	if roleName == "" {
		return fmt.Errorf("role name is required")
	}

	if len(secretNames) == 0 {
		existing := &rbacv1.Role{}
		if err := m.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: roleName}, existing); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to get Role %s/%s: %w", namespace, roleName, err)
		}
		m.logger.Info("Deleting tenant secrets Role (no tenant resources reference Secrets)", "namespace", namespace, "role", roleName)
		if err := m.client.Delete(ctx, existing, client.Preconditions{UID: &existing.UID}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete Role %s/%s: %w", namespace, roleName, err)
		}
		return nil
	}

	role := factory(namespace, secretNames)
	if role == nil {
		return fmt.Errorf("failed to build Role %s/%s: factory returned nil", namespace, roleName)
	}
	if role.GetName() != roleName || role.GetNamespace() != namespace {
		return fmt.Errorf("failed to build Role %s/%s: factory returned %s/%s", namespace, roleName, role.GetNamespace(), role.GetName())
	}
	m.logger.V(1).Info("Applying tenant secrets Role", "namespace", namespace, "role", roleName)
	if err := m.applyResource(ctx, role); err != nil {
		return fmt.Errorf("failed to apply secrets Role %s/%s: %w", namespace, roleName, err)
	}

	return nil
}

type secretsRoleBindingFactory func(namespace string, operatorSA OperatorServiceAccount) *rbacv1.RoleBinding

func (m *Manager) ensureSecretsRoleBinding(ctx context.Context, namespace string, roleBindingName string, roleName string, shouldExist bool, factory secretsRoleBindingFactory) error {
	if roleBindingName == "" || roleName == "" {
		return fmt.Errorf("role binding name and role name are required")
	}

	if !shouldExist {
		existing := &rbacv1.RoleBinding{}
		if err := m.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: roleBindingName}, existing); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to get RoleBinding %s/%s: %w", namespace, roleBindingName, err)
		}
		m.logger.Info("Deleting tenant secrets RoleBinding (no tenant resources reference Secrets)", "namespace", namespace, "rolebinding", roleBindingName)
		if err := m.client.Delete(ctx, existing, client.Preconditions{UID: &existing.UID}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete RoleBinding %s/%s: %w", namespace, roleBindingName, err)
		}
		return nil
	}

	roleBinding := factory(namespace, m.operatorSA)
	if roleBinding == nil {
		return fmt.Errorf("failed to build RoleBinding %s/%s: factory returned nil", namespace, roleBindingName)
	}
	if roleBinding.GetName() != roleBindingName || roleBinding.GetNamespace() != namespace {
		return fmt.Errorf("failed to build RoleBinding %s/%s: factory returned %s/%s", namespace, roleBindingName, roleBinding.GetNamespace(), roleBinding.GetName())
	}
	if roleBinding.RoleRef.Name != roleName {
		return fmt.Errorf("failed to build RoleBinding %s/%s: roleRef.name=%q want %q", namespace, roleBindingName, roleBinding.RoleRef.Name, roleName)
	}
	m.logger.V(1).Info("Applying tenant secrets RoleBinding", "namespace", namespace, "rolebinding", roleBindingName)
	if err := m.applyResource(ctx, roleBinding); err != nil {
		return fmt.Errorf("failed to apply secrets RoleBinding %s/%s: %w", namespace, roleBindingName, err)
	}

	return nil
}
