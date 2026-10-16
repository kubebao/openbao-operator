package provisioner

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

var (
	verbsReadOnly             = []string{"get", "list", "watch"}
	verbsManage               = []string{"create", "delete", "get", "list", "patch", "update", "watch"}
	verbsClusterDelegation    = []string{"publishnetworking", "restore", "usecloudidentities", "usecustomexecutables", "useimagetrustroots"}
	verbsEventWrite           = []string{"create", "patch"}
	verbsPodManage            = []string{"delete", "get", "list", "patch", "update", "watch"}
	verbsOptionalPolicyManage = []string{"create", "delete", "get", "patch"}
	verbsServiceMonitorManage = []string{"create", "delete", "get", "patch"}
	verbsSecretRead           = []string{"get"}
	verbsSecretCreate         = []string{"create"}
	verbsSecretMutate         = []string{"delete", "get", "patch", "update"}
)

func cloneStrings(in []string) []string {
	return append([]string(nil), in...)
}

const (
	// TenantRoleName is the name of the Role created in each tenant namespace.
	TenantRoleName = constants.TenantRoleName
	// TenantRoleBindingName is the name of the RoleBinding created in each tenant namespace.
	TenantRoleBindingName = constants.TenantRoleBindingName

	// TenantSecretsReaderRoleName grants read-only access to explicitly enumerated Secret names.
	// This is used to reduce the blast radius of Secret access in tenant namespaces.
	TenantSecretsReaderRoleName = "openbao-operator-tenant-secrets-reader"
	// TenantSecretsReaderRoleBindingName binds TenantSecretsReaderRoleName to the controller ServiceAccount.
	TenantSecretsReaderRoleBindingName = "openbao-operator-tenant-secrets-reader-rolebinding"
	// TenantSecretsWriterRoleName grants write access to explicitly enumerated Secret names owned by the operator.
	TenantSecretsWriterRoleName = "openbao-operator-tenant-secrets-writer"
	// TenantSecretsWriterRoleBindingName binds TenantSecretsWriterRoleName to the controller ServiceAccount.
	TenantSecretsWriterRoleBindingName = "openbao-operator-tenant-secrets-writer-rolebinding"
)

// OperatorServiceAccount represents the operator's ServiceAccount identity.
type OperatorServiceAccount struct {
	// Name is the name of the ServiceAccount (e.g., "controller-manager").
	Name string
	// Namespace is the namespace where the ServiceAccount exists (e.g., "openbao-operator-system").
	Namespace string
}

// GenerateTenantRole generates a namespaced Role that grants the operator
// the permissions needed to manage OpenBaoCluster resources in a tenant namespace.
func GenerateTenantRole(namespace string) *rbacv1.Role {
	rules := []rbacv1.PolicyRule{
		{
			APIGroups: []string{"openbao.org"},
			Resources: []string{"openbaoclusters", "openbaoclusters/status", "openbaoclusters/finalizers"},
			Verbs:     cloneStrings(verbsManage),
		},
		{
			APIGroups: []string{"openbao.org"},
			Resources: []string{"openbaoclusters"},
			Verbs:     cloneStrings(verbsClusterDelegation),
		},
		{
			APIGroups: []string{"openbao.org"},
			Resources: []string{"openbaorestores", "openbaorestores/status", "openbaorestores/finalizers"},
			Verbs:     cloneStrings(verbsManage),
		},
		{
			APIGroups: []string{"apps"},
			Resources: []string{"statefulsets"},
			Verbs:     cloneStrings(verbsManage),
		},
		{
			APIGroups: []string{""},
			Resources: []string{"services", "configmaps", "serviceaccounts"},
			Verbs:     cloneStrings(verbsManage),
		},
		{
			APIGroups: []string{""},
			Resources: []string{"events"},
			Verbs:     cloneStrings(verbsEventWrite),
		},
		{
			APIGroups: []string{"events.k8s.io"},
			Resources: []string{"events"},
			Verbs:     cloneStrings(verbsEventWrite),
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods"},
			Verbs:     cloneStrings(verbsPodManage),
		},
		{
			APIGroups: []string{""},
			Resources: []string{"persistentvolumeclaims"},
			Verbs:     cloneStrings(verbsManage),
		},
		{
			APIGroups: []string{"batch"},
			Resources: []string{"jobs"},
			Verbs:     cloneStrings(verbsManage),
		},
		{
			APIGroups: []string{"policy"},
			Resources: []string{"poddisruptionbudgets"},
			Verbs:     cloneStrings(verbsManage),
		},
		{
			APIGroups: []string{"networking.k8s.io"},
			Resources: []string{"ingresses", "networkpolicies"},
			Verbs:     cloneStrings(verbsManage),
		},
		{
			APIGroups: []string{"cilium.io"},
			Resources: []string{"ciliumnetworkpolicies"},
			Verbs:     cloneStrings(verbsOptionalPolicyManage),
		},
		{
			APIGroups: []string{"gateway.networking.k8s.io"},
			Resources: []string{"httproutes", "tlsroutes", "backendtlspolicies"},
			Verbs:     cloneStrings(verbsManage),
		},
		{
			APIGroups: []string{"monitoring.coreos.com"},
			Resources: []string{"servicemonitors"},
			Verbs:     cloneStrings(verbsServiceMonitorManage),
		},
		{
			APIGroups: []string{"rbac.authorization.k8s.io"},
			Resources: []string{"roles", "rolebindings"},
			Verbs:     cloneStrings(verbsManage),
		},
		{
			APIGroups: []string{""},
			Resources: []string{"endpoints"},
			Verbs:     cloneStrings(verbsReadOnly),
		},
		{
			APIGroups: []string{"discovery.k8s.io"},
			Resources: []string{"endpointslices"},
			Verbs:     cloneStrings(verbsReadOnly),
		},
	}

	return &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      TenantRoleName,
			Namespace: namespace,
			Labels:    provisionerManagedLabels(),
		},
		Rules: rules,
	}
}

// GenerateTenantRoleBinding generates a namespaced RoleBinding that binds
// the operator's ServiceAccount to the tenant Role.
func GenerateTenantRoleBinding(namespace string, operatorSA OperatorServiceAccount) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      TenantRoleBindingName,
			Namespace: namespace,
			Labels:    provisionerManagedLabels(),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     TenantRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      operatorSA.Name,
				Namespace: operatorSA.Namespace,
			},
		},
	}
}

// GenerateTenantSecretsReaderRole generates a namespaced Role that grants read-only access to a
// specific set of Secret names. This is used to avoid granting broad Secret access in tenant namespaces.
func GenerateTenantSecretsReaderRole(namespace string, secretNames []string) *rbacv1.Role {
	rules := []rbacv1.PolicyRule{
		{
			APIGroups:     []string{""},
			Resources:     []string{"secrets"},
			Verbs:         cloneStrings(verbsSecretRead),
			ResourceNames: secretNames,
		},
	}

	return &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      TenantSecretsReaderRoleName,
			Namespace: namespace,
			Labels:    provisionerManagedLabels(),
		},
		Rules: rules,
	}
}

// GenerateTenantSecretsReaderRoleBinding generates a namespaced RoleBinding that binds the
// operator's controller ServiceAccount to the secrets reader Role.
func GenerateTenantSecretsReaderRoleBinding(namespace string, operatorSA OperatorServiceAccount) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      TenantSecretsReaderRoleBindingName,
			Namespace: namespace,
			Labels:    provisionerManagedLabels(),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     TenantSecretsReaderRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      operatorSA.Name,
				Namespace: operatorSA.Namespace,
			},
		},
	}
}

// GenerateTenantSecretsWriterRole generates a namespaced Role that grants write access to a
// specific set of Secret names owned by the operator.
func GenerateTenantSecretsWriterRole(namespace string, secretNames []string) *rbacv1.Role {
	rules := []rbacv1.PolicyRule{}
	if len(secretNames) > 0 {
		// RBAC cannot scope "create" by resourceNames (create is against the collection),
		// so we split create from the name-scoped mutation verbs.
		rules = append(rules,
			rbacv1.PolicyRule{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     cloneStrings(verbsSecretCreate),
			},
			rbacv1.PolicyRule{
				APIGroups:     []string{""},
				Resources:     []string{"secrets"},
				Verbs:         cloneStrings(verbsSecretMutate),
				ResourceNames: secretNames,
			},
		)
	}

	return &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      TenantSecretsWriterRoleName,
			Namespace: namespace,
			Labels:    provisionerManagedLabels(),
		},
		Rules: rules,
	}
}

// GenerateTenantSecretsWriterRoleBinding generates a namespaced RoleBinding that binds the
// operator's controller ServiceAccount to the secrets writer Role.
func GenerateTenantSecretsWriterRoleBinding(namespace string, operatorSA OperatorServiceAccount) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      TenantSecretsWriterRoleBindingName,
			Namespace: namespace,
			Labels:    provisionerManagedLabels(),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     TenantSecretsWriterRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      operatorSA.Name,
				Namespace: operatorSA.Namespace,
			},
		},
	}
}
