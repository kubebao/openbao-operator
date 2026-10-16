package provisioner

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IsTenantNamespaceProvisioned returns true if the tenant namespace has been provisioned
// (i.e., the core tenant RoleBinding exists).
//
// SECURITY: RBAC objects are not cached, so this avoids requiring list/watch permissions.
func (m *Manager) IsTenantNamespaceProvisioned(ctx context.Context, namespace string) (bool, error) {
	if namespace == "" {
		return false, fmt.Errorf("namespace is required")
	}

	existing := &rbacv1.RoleBinding{}
	if err := m.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: TenantRoleBindingName}, existing); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get tenant RoleBinding %s/%s: %w", namespace, TenantRoleBindingName, err)
	}
	return true, nil
}

// CleanupTenantResources removes the operator-managed governance and RBAC resources from the given namespace.
func (m *Manager) CleanupTenantResources(ctx context.Context, namespace string) error {
	governanceResources := []struct {
		kind   string
		name   string
		object client.Object
	}{
		{
			kind: "ResourceQuota",
			name: TenantResourceQuotaName,
			object: &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      TenantResourceQuotaName,
			}},
		},
		{
			kind: "LimitRange",
			name: TenantLimitRangeName,
			object: &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      TenantLimitRangeName,
			}},
		},
	}
	for _, resource := range governanceResources {
		if err := m.client.Get(ctx, client.ObjectKeyFromObject(resource.object), resource.object); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("failed to get %s %s/%s before deletion: %w", resource.kind, namespace, resource.name, err)
		}
		uid := resource.object.GetUID()
		m.logger.Info("Deleting tenant "+resource.kind, "namespace", namespace, "name", resource.name)
		if err := m.client.Delete(ctx, resource.object, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete %s %s/%s: %w", resource.kind, namespace, resource.name, err)
		}
	}

	for _, name := range []string{
		TenantSecretsReaderRoleBindingName,
		TenantSecretsWriterRoleBindingName,
	} {
		if err := m.deleteRoleBindingIfExists(ctx, namespace, name, "tenant secrets RoleBinding"); err != nil {
			return err
		}
	}

	for _, name := range []string{
		TenantSecretsReaderRoleName,
		TenantSecretsWriterRoleName,
	} {
		if err := m.deleteRoleIfExists(ctx, namespace, name, "tenant secrets Role"); err != nil {
			return err
		}
	}

	if err := m.deleteRoleBindingIfExists(ctx, namespace, TenantRoleBindingName, "tenant RoleBinding"); err != nil {
		return err
	}
	if err := m.deleteRoleIfExists(ctx, namespace, TenantRoleName, "tenant Role"); err != nil {
		return err
	}

	return nil
}

func (m *Manager) deleteRoleBindingIfExists(ctx context.Context, namespace, name, logLabel string) error {
	roleBinding := &rbacv1.RoleBinding{}
	if err := m.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, roleBinding); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get RoleBinding %s/%s: %w", namespace, name, err)
	}

	m.logger.Info("Deleting "+logLabel, "namespace", namespace, "rolebinding", name)
	if err := m.client.Delete(ctx, roleBinding, client.Preconditions{UID: &roleBinding.UID}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete RoleBinding %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (m *Manager) deleteRoleIfExists(ctx context.Context, namespace, name, logLabel string) error {
	role := &rbacv1.Role{}
	if err := m.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, role); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get Role %s/%s: %w", namespace, name, err)
	}

	m.logger.Info("Deleting "+logLabel, "namespace", namespace, "role", name)
	if err := m.client.Delete(ctx, role, client.Preconditions{UID: &role.UID}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete Role %s/%s: %w", namespace, name, err)
	}
	return nil
}
