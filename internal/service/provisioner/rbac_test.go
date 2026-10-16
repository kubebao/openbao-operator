package provisioner

import (
	"slices"
	"testing"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	rbacv1 "k8s.io/api/rbac/v1"
)

//nolint:gocyclo // Table-driven test with multiple assertions
func TestGenerateTenantRole(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		wantName  string
		wantRules int
	}{
		{
			name:      "default namespace",
			namespace: "default",
			wantName:  TenantRoleName,
			wantRules: 18, // Expected number of PolicyRules
		},
		{
			name:      "custom namespace",
			namespace: "tenant-1",
			wantName:  TenantRoleName,
			wantRules: 18,
		},
		{
			name:      "namespace with special characters",
			namespace: "my-namespace-123",
			wantName:  TenantRoleName,
			wantRules: 18,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role := GenerateTenantRole(tt.namespace)

			if role == nil {
				t.Fatalf("GenerateTenantRole() returned nil")
			}

			if role.Name != tt.wantName {
				t.Errorf("GenerateTenantRole() name = %v, want %v", role.Name, tt.wantName)
			}

			if role.Namespace != tt.namespace {
				t.Errorf("GenerateTenantRole() namespace = %v, want %v", role.Namespace, tt.namespace)
			}

			if len(role.Rules) != tt.wantRules {
				t.Errorf("GenerateTenantRole() rules count = %v, want %v", len(role.Rules), tt.wantRules)
			}

			// Verify labels
			expectedLabels := map[string]string{
				constants.LabelAppName:          constants.LabelValueAppNameOpenBaoOperator,
				constants.LabelAppComponent:     "provisioner",
				constants.LabelAppManagedBy:     constants.LabelValueAppManagedByOpenBaoOperator,
				constants.LabelOpenBaoComponent: "provisioner",
			}
			for k, v := range expectedLabels {
				if role.Labels[k] != v {
					t.Errorf("GenerateTenantRole() label[%s] = %v, want %v", k, role.Labels[k], v)
				}
			}

			// Verify key rules exist
			hasOpenBaoClusterRule := false
			hasOpenBaoClusterDelegationRule := false
			hasStatefulSetRule := false
			hasPodRule := false
			hasEventsK8sRule := false
			hasServiceMonitorRule := false
			hasCiliumRule := false
			hasPrometheusRuleRule := false
			hasQuotaOrLimitRangeRule := false

			for _, rule := range role.Rules {
				// Check for OpenBaoCluster rule (uses commonVerbs, not "*")
				if slices.Contains(rule.APIGroups, "openbao.org") &&
					slices.Contains(rule.Resources, "openbaoclusters") &&
					slices.Contains(rule.Verbs, "get") &&
					slices.Contains(rule.Verbs, "create") {
					hasOpenBaoClusterRule = true
				}

				if slices.Contains(rule.APIGroups, "openbao.org") &&
					len(rule.Resources) == 1 &&
					slices.Contains(rule.Resources, "openbaoclusters") &&
					slices.Contains(rule.Verbs, "publishnetworking") &&
					slices.Contains(rule.Verbs, "restore") &&
					slices.Contains(rule.Verbs, "usecloudidentities") &&
					slices.Contains(rule.Verbs, "usecustomexecutables") &&
					slices.Contains(rule.Verbs, "useimagetrustroots") {
					hasOpenBaoClusterDelegationRule = true
				}

				// Check for StatefulSet rule (uses commonVerbs, not "*")
				if slices.Contains(rule.APIGroups, "apps") &&
					slices.Contains(rule.Resources, "statefulsets") &&
					slices.Contains(rule.Verbs, "get") &&
					slices.Contains(rule.Verbs, "create") {
					hasStatefulSetRule = true
				}

				// Check for Pod rule (includes delete for cleanup)
				if slices.Contains(rule.APIGroups, "") &&
					slices.Contains(rule.Resources, "pods") &&
					slices.Contains(rule.Verbs, "get") &&
					slices.Contains(rule.Verbs, "list") &&
					slices.Contains(rule.Verbs, "delete") {
					hasPodRule = true
				}

				// controller-runtime v0.23 emits events via events.k8s.io.
				if slices.Contains(rule.APIGroups, "events.k8s.io") &&
					slices.Contains(rule.Resources, "events") &&
					slices.Contains(rule.Verbs, "create") &&
					slices.Contains(rule.Verbs, "patch") {
					hasEventsK8sRule = true
				}

				if slices.Contains(rule.APIGroups, "cilium.io") {
					hasCiliumRule = true
					if !slices.Equal(rule.Resources, []string{"ciliumnetworkpolicies"}) ||
						!slices.Equal(rule.Verbs, []string{"create", "delete", "get", "patch"}) {
						t.Errorf("unexpected Cilium permissions: %#v", rule)
					}
				}
				if slices.Contains(rule.APIGroups, "monitoring.coreos.com") &&
					slices.Contains(rule.Resources, "servicemonitors") &&
					slices.Contains(rule.Verbs, "get") &&
					slices.Contains(rule.Verbs, "create") &&
					slices.Contains(rule.Verbs, "patch") &&
					slices.Contains(rule.Verbs, "delete") {
					for _, forbiddenVerb := range []string{"list", "update", "watch"} {
						if slices.Contains(rule.Verbs, forbiddenVerb) {
							t.Errorf("GenerateTenantRole() ServiceMonitor rule unexpectedly grants %q", forbiddenVerb)
						}
					}
					hasServiceMonitorRule = true
				}

				if slices.Contains(rule.APIGroups, "monitoring.coreos.com") &&
					slices.Contains(rule.Resources, "prometheusrules") {
					hasPrometheusRuleRule = true
				}

				if slices.Contains(rule.Resources, "resourcequotas") || slices.Contains(rule.Resources, "limitranges") {
					hasQuotaOrLimitRangeRule = true
				}
			}

			if !hasOpenBaoClusterRule {
				t.Error("GenerateTenantRole() missing OpenBaoCluster rule")
			}
			if !hasOpenBaoClusterDelegationRule {
				t.Error("GenerateTenantRole() missing OpenBaoCluster controller delegation rule")
			}
			if !hasStatefulSetRule {
				t.Error("GenerateTenantRole() missing StatefulSet rule")
			}
			if !hasPodRule {
				t.Error("GenerateTenantRole() missing Pod rule")
			}
			if !hasEventsK8sRule {
				t.Error("GenerateTenantRole() missing events.k8s.io Events rule")
			}
			if !hasCiliumRule {
				t.Error("missing namespaced CiliumNetworkPolicy permissions")
			}
			if !hasServiceMonitorRule {
				t.Error("GenerateTenantRole() missing monitoring.coreos.com ServiceMonitor rule")
			}
			if hasPrometheusRuleRule {
				t.Error("GenerateTenantRole() unexpectedly includes PrometheusRule permissions")
			}
			if hasQuotaOrLimitRangeRule {
				t.Error("GenerateTenantRole() unexpectedly includes ResourceQuota/LimitRange permissions")
			}
		})
	}
}

func TestGenerateTenantRoleBinding(t *testing.T) {
	tests := []struct {
		name       string
		namespace  string
		operatorSA OperatorServiceAccount
		wantName   string
	}{
		{
			name:      "default service account",
			namespace: "default",
			operatorSA: OperatorServiceAccount{
				Name:      "controller-manager",
				Namespace: "openbao-operator-system",
			},
			wantName: TenantRoleBindingName,
		},
		{
			name:      "custom service account",
			namespace: "tenant-1",
			operatorSA: OperatorServiceAccount{
				Name:      "custom-operator",
				Namespace: "custom-namespace",
			},
			wantName: TenantRoleBindingName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roleBinding := GenerateTenantRoleBinding(tt.namespace, tt.operatorSA)

			if roleBinding == nil {
				t.Fatalf("GenerateTenantRoleBinding() returned nil")
			}

			if roleBinding.Name != tt.wantName {
				t.Errorf("GenerateTenantRoleBinding() name = %v, want %v", roleBinding.Name, tt.wantName)
			}

			if roleBinding.Namespace != tt.namespace {
				t.Errorf("GenerateTenantRoleBinding() namespace = %v, want %v", roleBinding.Namespace, tt.namespace)
			}

			// Verify RoleRef
			if roleBinding.RoleRef.APIGroup != "rbac.authorization.k8s.io" {
				t.Errorf("GenerateTenantRoleBinding() RoleRef.APIGroup = %v, want rbac.authorization.k8s.io", roleBinding.RoleRef.APIGroup)
			}
			if roleBinding.RoleRef.Kind != "Role" {
				t.Errorf("GenerateTenantRoleBinding() RoleRef.Kind = %v, want Role", roleBinding.RoleRef.Kind)
			}
			if roleBinding.RoleRef.Name != TenantRoleName {
				t.Errorf("GenerateTenantRoleBinding() RoleRef.Name = %v, want %v", roleBinding.RoleRef.Name, TenantRoleName)
			}

			// Verify Subjects
			if len(roleBinding.Subjects) != 1 {
				t.Fatalf("GenerateTenantRoleBinding() subjects count = %v, want 1", len(roleBinding.Subjects))
			}

			subject := roleBinding.Subjects[0]
			if subject.Kind != "ServiceAccount" {
				t.Errorf("GenerateTenantRoleBinding() subject.Kind = %v, want ServiceAccount", subject.Kind)
			}
			if subject.Name != tt.operatorSA.Name {
				t.Errorf("GenerateTenantRoleBinding() subject.Name = %v, want %v", subject.Name, tt.operatorSA.Name)
			}
			if subject.Namespace != tt.operatorSA.Namespace {
				t.Errorf("GenerateTenantRoleBinding() subject.Namespace = %v, want %v", subject.Namespace, tt.operatorSA.Namespace)
			}

			// Verify labels
			expectedLabels := map[string]string{
				constants.LabelAppName:      constants.LabelValueAppNameOpenBaoOperator,
				constants.LabelAppComponent: "provisioner",
				constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
			}
			for k, v := range expectedLabels {
				if roleBinding.Labels[k] != v {
					t.Errorf("GenerateTenantRoleBinding() label[%s] = %v, want %v", k, roleBinding.Labels[k], v)
				}
			}
		})
	}
}

func TestGenerateTenantRole_DoesNotGrantSecretsAccess(t *testing.T) {
	role := GenerateTenantRole("tenant-a")

	for i := range role.Rules {
		rule := role.Rules[i]
		if isCoreSecretsRule(rule) {
			t.Fatalf("tenant Role must not grant Secrets access; got rule %#v", rule)
		}
	}
}

func TestGenerateTenantSecretsWriterRole_RestrictsMutationAccess(t *testing.T) {
	secretNames := []string{"cluster-a-tls-ca", "cluster-a-tls-server"}
	role := GenerateTenantSecretsWriterRole("tenant-a", secretNames)

	if role.Name != TenantSecretsWriterRoleName {
		t.Fatalf("writer Role name = %q, want %q", role.Name, TenantSecretsWriterRoleName)
	}
	if len(role.Rules) != 2 {
		t.Fatalf("writer Role rules = %#v, want create and named mutation rules", role.Rules)
	}

	createRule := findCoreSecretsRule(role.Rules, func(rule rbacv1.PolicyRule) bool {
		return slices.Contains(rule.Verbs, "create") && len(rule.ResourceNames) == 0
	})
	if createRule == nil {
		t.Fatalf("writer Role missing collection-level Secrets create rule: %#v", role.Rules)
	}
	for _, forbiddenVerb := range []string{"get", "list", "watch", "patch", "update", "delete"} {
		if slices.Contains(createRule.Verbs, forbiddenVerb) {
			t.Fatalf("writer create rule must not grant %q: %#v", forbiddenVerb, *createRule)
		}
	}

	mutateRule := findCoreSecretsRule(role.Rules, func(rule rbacv1.PolicyRule) bool {
		return len(rule.ResourceNames) > 0
	})
	if mutateRule == nil {
		t.Fatalf("writer Role missing resourceNames-scoped Secrets mutation rule: %#v", role.Rules)
	}
	for _, wantVerb := range []string{"delete", "get", "patch", "update"} {
		if !slices.Contains(mutateRule.Verbs, wantVerb) {
			t.Fatalf("writer mutation rule missing verb %q: %#v", wantVerb, *mutateRule)
		}
	}
	for _, forbiddenVerb := range []string{"create", "list", "watch"} {
		if slices.Contains(mutateRule.Verbs, forbiddenVerb) {
			t.Fatalf("writer mutation rule must not grant %q: %#v", forbiddenVerb, *mutateRule)
		}
	}
	for _, wantName := range secretNames {
		if !slices.Contains(mutateRule.ResourceNames, wantName) {
			t.Fatalf("writer mutation rule missing resourceName %q: %#v", wantName, *mutateRule)
		}
	}
}

func TestGenerateTenantSecretsReaderRole_IsResourceNameScoped(t *testing.T) {
	secretNames := []string{"cluster-a-credentials"}
	role := GenerateTenantSecretsReaderRole("tenant-a", secretNames)

	if role.Name != TenantSecretsReaderRoleName {
		t.Fatalf("reader Role name = %q, want %q", role.Name, TenantSecretsReaderRoleName)
	}
	if len(role.Rules) != 1 {
		t.Fatalf("reader Role rules = %#v, want one scoped read rule", role.Rules)
	}

	rule := role.Rules[0]
	if !isCoreSecretsRule(rule) {
		t.Fatalf("reader Role rule must target core Secrets: %#v", rule)
	}
	if len(rule.ResourceNames) != 1 || rule.ResourceNames[0] != secretNames[0] {
		t.Fatalf("reader Role resourceNames = %#v, want %#v", rule.ResourceNames, secretNames)
	}
	if len(rule.Verbs) != 1 || rule.Verbs[0] != "get" {
		t.Fatalf("reader Role verbs = %#v, want get only", rule.Verbs)
	}
}

func findCoreSecretsRule(rules []rbacv1.PolicyRule, keep func(rbacv1.PolicyRule) bool) *rbacv1.PolicyRule {
	for i := range rules {
		if isCoreSecretsRule(rules[i]) && keep(rules[i]) {
			return &rules[i]
		}
	}
	return nil
}

func isCoreSecretsRule(rule rbacv1.PolicyRule) bool {
	return slices.Contains(rule.APIGroups, "") && slices.Contains(rule.Resources, "secrets")
}
