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

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// FinalizeNamespace removes existing tenant resources after all OpenBao resources
// finish finalization. It never creates permissions in a terminating namespace.
func (m *Manager) FinalizeNamespace(ctx context.Context, namespace string, uid types.UID) (bool, error) {
	if namespace == "" || uid == "" {
		return false, fmt.Errorf("namespace name and UID are required")
	}
	if err := m.validateTerminatingNamespace(ctx, namespace, uid); err != nil {
		return false, err
	}
	clusters := &openbaov1alpha1.OpenBaoClusterList{}
	if err := m.reader.List(ctx, clusters, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("list clusters before namespace finalization: %w", err)
	}
	restores := &openbaov1alpha1.OpenBaoRestoreList{}
	if err := m.reader.List(ctx, restores, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("list restores before namespace finalization: %w", err)
	}
	if len(clusters.Items) != 0 || len(restores.Items) != 0 {
		return false, nil
	}

	// Keep the core Role until the last delete. Each object is read before the
	// namespace is revalidated, then deleted with UID and resource-version tests.
	objects := []client.Object{
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: TenantResourceQuotaName, Namespace: namespace}},
		&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: TenantLimitRangeName, Namespace: namespace}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: TenantSecretsReaderRoleBindingName, Namespace: namespace}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: TenantSecretsWriterRoleBindingName, Namespace: namespace}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: TenantSecretsReaderRoleName, Namespace: namespace}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: TenantSecretsWriterRoleName, Namespace: namespace}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: TenantRoleBindingName, Namespace: namespace}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: TenantRoleName, Namespace: namespace}},
	}
	done := true
	for _, obj := range objects {
		if err := m.reader.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, fmt.Errorf("read tenant resource before namespace finalization: %w", err)
		}
		managed := true
		for key, value := range provisionerManagedLabels() {
			managed = managed && obj.GetLabels()[key] == value
		}
		if !managed {
			continue
		}
		if err := m.validateTerminatingNamespace(ctx, namespace, uid); err != nil {
			return false, err
		}
		objectUID, rv := obj.GetUID(), obj.GetResourceVersion()
		if objectUID == "" || rv == "" {
			return false, fmt.Errorf("tenant resource %s lacks UID or resource version", obj.GetName())
		}
		if err := m.client.Delete(ctx, obj, client.Preconditions{UID: &objectUID, ResourceVersion: &rv}); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete tenant resource during namespace finalization: %w", err)
		}
		done = done && len(obj.GetFinalizers()) == 0
	}
	return done, nil
}

func (m *Manager) validateTerminatingNamespace(ctx context.Context, namespace string, uid types.UID) error {
	ns := &corev1.Namespace{}
	if err := m.reader.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		return fmt.Errorf("read terminating namespace: %w", err)
	}
	if ns.UID != uid || ns.DeletionTimestamp.IsZero() {
		return fmt.Errorf("namespace %s is replaced or is not terminating", namespace)
	}
	return nil
}
