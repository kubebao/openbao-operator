//go:build integration
// +build integration

package identity

import (
	"context"
	"slices"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
)

const (
	apiVersion = "kubebao.org/v1alpha1"
	kind       = "OpenBaoCluster"
)

func TestEnsureServiceAccountCreatesAndUpdates(t *testing.T) {
	k8sClient, scheme := envtestClientForPackage(t)
	manager := NewManager(k8sClient, scheme)

	ns := testNamespace(t)
	cluster := newMinimalCluster("infra-sa", ns)
	createClusterCRForTest(t, k8sClient, cluster)

	ctx := context.Background()

	if err := manager.Reconcile(ctx, logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	sa := &corev1.ServiceAccount{}
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      resourceidentity.ServiceAccountName(cluster),
	}, sa)
	if err != nil {
		t.Fatalf("expected ServiceAccount to exist: %v", err)
	}

	// Verify labels
	expectedLabels := serviceAccountLabels(cluster)
	for key, expectedVal := range expectedLabels {
		if sa.Labels[key] != expectedVal {
			t.Errorf("expected ServiceAccount label %s=%s, got %s", key, expectedVal, sa.Labels[key])
		}
	}
}

func TestEnsureServiceAccount_IsIdempotent(t *testing.T) {
	k8sClient, scheme := envtestClientForPackage(t)
	manager := NewManager(k8sClient, scheme)

	ns := testNamespace(t)
	cluster := newMinimalCluster("infra-sa-idempotent", ns)
	createClusterCRForTest(t, k8sClient, cluster)

	ctx := context.Background()

	// First reconcile creates the ServiceAccount
	if err := manager.Reconcile(ctx, logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Get the ServiceAccount after first reconcile
	sa1 := &corev1.ServiceAccount{}
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      resourceidentity.ServiceAccountName(cluster),
	}, sa1)
	if err != nil {
		t.Fatalf("expected ServiceAccount to exist after first reconcile: %v", err)
	}

	// Second reconcile with same cluster should be idempotent (SSA)
	if err := manager.Reconcile(ctx, logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() second call error = %v", err)
	}

	// Get the ServiceAccount after second reconcile
	sa2 := &corev1.ServiceAccount{}
	err = k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      resourceidentity.ServiceAccountName(cluster),
	}, sa2)
	if err != nil {
		t.Fatalf("expected ServiceAccount to exist after second reconcile: %v", err)
	}

	// Verify labels are still correct (SSA maintains desired state)
	expectedLabels := serviceAccountLabels(cluster)
	for key, expectedVal := range expectedLabels {
		if sa2.Labels[key] != expectedVal {
			t.Errorf("expected ServiceAccount label %s=%s after idempotent apply, got %s", key, expectedVal, sa2.Labels[key])
		}
	}
}

func TestEnsureRBACCreatesRoleAndRoleBinding(t *testing.T) {
	k8sClient, scheme := envtestClientForPackage(t)
	manager := NewManager(k8sClient, scheme)

	ns := testNamespace(t)
	cluster := newMinimalCluster("infra-rbac", ns)
	createClusterCRForTest(t, k8sClient, cluster)

	ctx := context.Background()

	if err := manager.Reconcile(ctx, logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify Role exists
	role := &rbacv1.Role{}
	roleName := resourceidentity.ServiceAccountName(cluster) + "-role"
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      roleName,
	}, role)
	if err != nil {
		t.Fatalf("expected Role to exist: %v", err)
	}

	// Verify Role has correct rules
	if len(role.Rules) == 0 {
		t.Fatalf("expected Role to have rules")
	}

	foundPodRule := false
	foundPodMutationRule := false
	for _, rule := range role.Rules {
		if len(rule.APIGroups) > 0 && rule.APIGroups[0] == "" &&
			len(rule.Resources) > 0 && rule.Resources[0] == "pods" {
			// Discovery rule (list/watch) should not allow mutation.
			hasListOrWatch := false
			hasPatchOrUpdate := false
			for _, verb := range rule.Verbs {
				if verb == "list" || verb == "watch" {
					hasListOrWatch = true
				}
				if verb == "patch" || verb == "update" {
					hasPatchOrUpdate = true
				}
			}

			if hasListOrWatch {
				foundPodRule = true
				if hasPatchOrUpdate {
					t.Errorf("expected pod discovery rule to not include patch/update, got verbs=%v", rule.Verbs)
				}
				for _, expectedVerb := range []string{"get", "list", "watch"} {
					found := false
					for _, verb := range rule.Verbs {
						if verb == expectedVerb {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected Role to have verb %q in discovery rule", expectedVerb)
					}
				}
			}

			if hasPatchOrUpdate {
				foundPodMutationRule = true
				if len(rule.ResourceNames) == 0 {
					t.Errorf("expected pod mutation rule to restrict resourceNames, got none")
				}
				for _, expectedName := range []string{cluster.Name + "-0", cluster.Name + "-1", cluster.Name + "-2"} {
					found := false
					for _, n := range rule.ResourceNames {
						if n == expectedName {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected pod mutation rule to include resourceName %q", expectedName)
					}
				}
				for _, expectedVerb := range []string{"patch", "update"} {
					found := false
					for _, verb := range rule.Verbs {
						if verb == expectedVerb {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected Role to have verb %q in mutation rule", expectedVerb)
					}
				}
			}
		}
	}
	if !foundPodRule {
		t.Fatalf("expected Role to have pod discovery rule")
	}
	if !foundPodMutationRule {
		t.Fatalf("expected Role to have pod mutation rule with resourceNames")
	}

	// Verify RoleBinding exists
	roleBinding := &rbacv1.RoleBinding{}
	roleBindingName := resourceidentity.ServiceAccountName(cluster) + "-rolebinding"
	err = k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      roleBindingName,
	}, roleBinding)
	if err != nil {
		t.Fatalf("expected RoleBinding to exist: %v", err)
	}

	// Verify RoleBinding references the Role
	if roleBinding.RoleRef.Name != roleName {
		t.Fatalf("expected RoleBinding to reference Role %q, got %q", roleName, roleBinding.RoleRef.Name)
	}

	// Verify RoleBinding references the ServiceAccount
	if len(roleBinding.Subjects) == 0 {
		t.Fatalf("expected RoleBinding to have subjects")
	}

	saName := resourceidentity.ServiceAccountName(cluster)
	foundSubject := false
	for _, subject := range roleBinding.Subjects {
		if subject.Kind == "ServiceAccount" && subject.Name == saName && subject.Namespace == cluster.Namespace {
			foundSubject = true
			break
		}
	}
	if !foundSubject {
		t.Fatalf("expected RoleBinding to reference ServiceAccount %q", saName)
	}
}

func TestEnsureRBAC_IncludesBlueGreenPodResourceNames(t *testing.T) {
	k8sClient, scheme := envtestClientForPackage(t)
	manager := NewManager(k8sClient, scheme)

	ns := testNamespace(t)
	cluster := newMinimalCluster("infra-rbac-bluegreen", ns)
	cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{
		Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
	}
	cluster.Status.BlueGreen = &openbaov1alpha1.BlueGreenStatus{
		BlueRevision:  "rev-blue",
		GreenRevision: "rev-green",
	}
	createClusterCRForTest(t, k8sClient, cluster)

	ctx := context.Background()
	if err := manager.Reconcile(ctx, logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	role := &rbacv1.Role{}
	roleName := resourceidentity.ServiceAccountName(cluster) + "-role"
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      roleName,
	}, role)
	if err != nil {
		t.Fatalf("expected Role to exist: %v", err)
	}

	var mutationRule *rbacv1.PolicyRule
	for i := range role.Rules {
		rule := &role.Rules[i]
		if len(rule.APIGroups) > 0 && rule.APIGroups[0] == "" &&
			len(rule.Resources) > 0 && rule.Resources[0] == "pods" &&
			slices.Contains(rule.Verbs, "patch") {
			mutationRule = rule
			break
		}
	}
	if mutationRule == nil {
		t.Fatalf("expected pod mutation rule to exist")
	}

	for _, expected := range []string{
		cluster.Name + "-rev-blue-0",
		cluster.Name + "-rev-green-0",
	} {
		if !slices.Contains(mutationRule.ResourceNames, expected) {
			t.Fatalf("expected mutation rule to include %q, got %v", expected, mutationRule.ResourceNames)
		}
	}
}

func TestEnsureRBAC_RetainsReadReplicaPodResourceNamesDuringScaleDown(t *testing.T) {
	k8sClient, scheme := envtestClientForPackage(t)
	manager := NewManager(k8sClient, scheme)

	ns := testNamespace(t)
	cluster := newMinimalCluster("infra-rbac-read", ns)
	cluster.Status.ReadReplicas = &openbaov1alpha1.ReadReplicaStatus{
		DesiredReplicas:    2,
		ReadyReplicas:      2,
		RegisteredReplicas: 2,
	}
	createClusterCRForTest(t, k8sClient, cluster)

	ctx := context.Background()
	if err := manager.Reconcile(ctx, logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	role := &rbacv1.Role{}
	roleName := resourceidentity.ServiceAccountName(cluster) + "-role"
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      roleName,
	}, role)
	if err != nil {
		t.Fatalf("expected Role to exist: %v", err)
	}

	var mutationRule *rbacv1.PolicyRule
	for i := range role.Rules {
		rule := &role.Rules[i]
		if len(rule.APIGroups) > 0 && rule.APIGroups[0] == "" &&
			len(rule.Resources) > 0 && rule.Resources[0] == "pods" &&
			slices.Contains(rule.Verbs, "patch") {
			mutationRule = rule
			break
		}
	}
	if mutationRule == nil {
		t.Fatalf("expected pod mutation rule to exist")
	}

	for _, expected := range []string{
		cluster.Name + "-read-0",
		cluster.Name + "-read-1",
	} {
		if !slices.Contains(mutationRule.ResourceNames, expected) {
			t.Fatalf("expected mutation rule to include %q, got %v", expected, mutationRule.ResourceNames)
		}
	}
}

func TestEnsureRBAC_IsIdempotent(t *testing.T) {
	k8sClient, scheme := envtestClientForPackage(t)
	manager := NewManager(k8sClient, scheme)

	ns := testNamespace(t)
	cluster := newMinimalCluster("infra-rbac-idempotent", ns)
	createClusterCRForTest(t, k8sClient, cluster)

	ctx := context.Background()

	// First reconcile creates RBAC
	if err := manager.Reconcile(ctx, logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Second reconcile should be idempotent (SSA)
	if err := manager.Reconcile(ctx, logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() second call error = %v", err)
	}

	// Verify RoleBinding still references correct ServiceAccount
	roleBinding := &rbacv1.RoleBinding{}
	roleBindingName := resourceidentity.ServiceAccountName(cluster) + "-rolebinding"
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      roleBindingName,
	}, roleBinding)
	if err != nil {
		t.Fatalf("expected RoleBinding to exist after idempotent applies: %v", err)
	}

	saName := resourceidentity.ServiceAccountName(cluster)
	if len(roleBinding.Subjects) == 0 || roleBinding.Subjects[0].Name != saName {
		t.Fatalf("expected RoleBinding to reference ServiceAccount %q after idempotent applies", saName)
	}

	// Verify Role still exists and has correct rules
	role := &rbacv1.Role{}
	roleName := resourceidentity.ServiceAccountName(cluster) + "-role"
	err = k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      roleName,
	}, role)
	if err != nil {
		t.Fatalf("expected Role to exist after idempotent applies: %v", err)
	}
	if len(role.Rules) == 0 {
		t.Fatalf("expected Role to have rules after idempotent applies")
	}
}

// TestServiceAccountHasOwnerReferenceForGC verifies that ServiceAccount has an OwnerReference
// to the OpenBaoCluster, ensuring Kubernetes GC will delete it when the cluster is deleted.
func TestServiceAccountHasOwnerReferenceForGC(t *testing.T) {
	k8sClient, scheme := envtestClientForPackage(t)
	manager := NewManager(k8sClient, scheme)

	// Create the cluster in the fake client so it has a UID for OwnerReference
	ns := testNamespace(t)
	cluster := newMinimalCluster("infra-sa-gc", ns)
	cluster.APIVersion = apiVersion
	cluster.Kind = kind

	ctx := context.Background()
	createClusterCRForTest(t, k8sClient, cluster)

	// Create ServiceAccount via Reconcile
	if err := manager.Reconcile(ctx, logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify ServiceAccount has OwnerReference (for GC)
	sa := &corev1.ServiceAccount{}
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      resourceidentity.ServiceAccountName(cluster),
	}, sa)
	if err != nil {
		t.Fatalf("expected ServiceAccount to exist: %v", err)
	}

	foundOwnerRef := false
	for _, ref := range sa.OwnerReferences {
		if ref.Kind == kind && ref.Name == cluster.Name {
			foundOwnerRef = true
			break
		}
	}
	if !foundOwnerRef {
		t.Error("expected ServiceAccount to have OwnerReference to OpenBaoCluster for GC")
	}

}

// TestRBACHasOwnerReferenceForGC verifies that Role and RoleBinding have OwnerReferences
// to the OpenBaoCluster, ensuring Kubernetes GC will delete them when the cluster is deleted.
func TestRBACHasOwnerReferenceForGC(t *testing.T) {
	k8sClient, scheme := envtestClientForPackage(t)
	manager := NewManager(k8sClient, scheme)

	// Create the cluster in the fake client so it has a UID for OwnerReference
	ns := testNamespace(t)
	cluster := newMinimalCluster("infra-rbac-gc", ns)
	cluster.APIVersion = apiVersion
	cluster.Kind = kind

	ctx := context.Background()
	createClusterCRForTest(t, k8sClient, cluster)

	// Create RBAC via Reconcile
	if err := manager.Reconcile(ctx, logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify RoleBinding has OwnerReference (for GC)
	roleBindingName := resourceidentity.ServiceAccountName(cluster) + "-rolebinding"
	roleBinding := &rbacv1.RoleBinding{}
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      roleBindingName,
	}, roleBinding)
	if err != nil {
		t.Fatalf("expected RoleBinding to exist: %v", err)
	}

	foundOwnerRef := false
	for _, ref := range roleBinding.OwnerReferences {
		if ref.Kind == kind && ref.Name == cluster.Name {
			foundOwnerRef = true
			break
		}
	}
	if !foundOwnerRef {
		t.Error("expected RoleBinding to have OwnerReference to OpenBaoCluster for GC")
	}

	// Verify Role has OwnerReference (for GC)
	roleName := resourceidentity.ServiceAccountName(cluster) + "-role"
	role := &rbacv1.Role{}
	err = k8sClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      roleName,
	}, role)
	if err != nil {
		t.Fatalf("expected Role to exist: %v", err)
	}

	foundOwnerRef = false
	for _, ref := range role.OwnerReferences {
		if ref.Kind == kind && ref.Name == cluster.Name {
			foundOwnerRef = true
			break
		}
	}
	if !foundOwnerRef {
		t.Error("expected Role to have OwnerReference to OpenBaoCluster for GC")
	}

}
