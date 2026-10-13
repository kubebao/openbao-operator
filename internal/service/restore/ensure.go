package restore

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceapply"
	"github.com/kubebao/openbao-operator/internal/service/workloadidentity"
)

// EnsureRestoreServiceAccount creates or updates the ServiceAccount for restore Jobs.
func EnsureRestoreServiceAccount(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	cluster *openbaov1alpha1.OpenBaoCluster,
	target openbaov1alpha1.BackupTarget,
) error {
	saName := restoreServiceAccountName(cluster)
	sa := &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ServiceAccount",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        saName,
			Namespace:   cluster.Namespace,
			Labels:      restoreServiceAccountLabels(cluster),
			Annotations: workloadidentity.ServiceAccountAnnotations(target),
		},
	}

	if err := resourceapply.ApplyOwned(ctx, c, scheme, cluster, sa); err != nil {
		return fmt.Errorf("failed to ensure restore ServiceAccount %s/%s: %w", cluster.Namespace, saName, err)
	}

	return nil
}

// EnsureRestoreRBAC creates or updates Role/RoleBinding required by restore Jobs for pod discovery.
func EnsureRestoreRBAC(ctx context.Context, c client.Client, scheme *runtime.Scheme, cluster *openbaov1alpha1.OpenBaoCluster, requests ...string) error {
	return ensureRestoreRBACForServiceAccount(ctx, c, scheme, cluster, restoreServiceAccountName(cluster), requests...)
}

func ensureRestoreRBACForServiceAccount(ctx context.Context, c client.Client, scheme *runtime.Scheme,
	cluster *openbaov1alpha1.OpenBaoCluster, saName string, requests ...string,
) error {
	// Keep grants owned by each target; a shared reader account must not make
	// one target overwrite another target's request-specific Role or binding.
	roleName := restoreServiceAccountName(cluster) + "-role"
	roleBindingName := restoreServiceAccountName(cluster) + "-rolebinding"
	resourceLabels := restoreLabels(cluster)

	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Role",
			APIVersion: "rbac.authorization.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: cluster.Namespace,
			Labels:    resourceLabels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}

	if len(requests) != 0 {
		role.Rules = append(role.Rules,
			rbacv1.PolicyRule{APIGroups: []string{"openbao.org"}, Resources: []string{"openbaorestores"}, ResourceNames: requests, Verbs: []string{"get"}},
			rbacv1.PolicyRule{APIGroups: []string{"openbao.org"}, Resources: []string{"openbaorestores/status"}, ResourceNames: requests, Verbs: []string{"patch"}},
		)
	}
	if err := resourceapply.ApplyOwned(ctx, c, scheme, cluster, role); err != nil {
		return fmt.Errorf("failed to ensure Role %s/%s: %w", cluster.Namespace, roleName, err)
	}

	roleBinding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RoleBinding",
			APIVersion: "rbac.authorization.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleBindingName,
			Namespace: cluster.Namespace,
			Labels:    resourceLabels,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: cluster.Namespace,
			},
		},
	}

	if err := resourceapply.ApplyOwned(ctx, c, scheme, cluster, roleBinding); err != nil {
		return fmt.Errorf("failed to ensure RoleBinding %s/%s: %w", cluster.Namespace, roleBindingName, err)
	}

	return nil
}

// restoreServiceAccountName returns the name for the restore ServiceAccount.
func restoreServiceAccountName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + RestoreServiceAccountSuffix
}

// restoreLabels returns standard labels for restore resources.
func restoreLabels(cluster *openbaov1alpha1.OpenBaoCluster) map[string]string {
	return map[string]string{
		constants.LabelAppManagedBy:     constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoCluster:   cluster.Name,
		constants.LabelOpenBaoComponent: ComponentRestore,
	}
}

func restoreServiceAccountLabels(cluster *openbaov1alpha1.OpenBaoCluster) map[string]string {
	labels := restoreLabels(cluster)
	labels[constants.LabelOpenBaoServiceAccountRole] = constants.ServiceAccountRoleRestore
	return labels
}
