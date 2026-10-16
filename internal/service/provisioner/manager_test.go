package provisioner

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// testScheme is a shared scheme used across tests.
var testScheme = func() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)
	return scheme
}()

const testNamespace = "test-namespace"
const podSecurityRestrictedLevel = "restricted"

func newTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(testScheme)
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	return builder.Build()
}

type namespaceUpdateDenyingClient struct {
	client.Client
}

func (c namespaceUpdateDenyingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*corev1.Namespace); ok {
		return apierrors.NewForbidden(
			corev1.Resource("namespaces"),
			obj.GetName(),
			errors.New("namespace updates are managed by platform policy"),
		)
	}
	return c.Client.Update(ctx, obj, opts...)
}

func newTestTenant(namespace string) *openbaov1alpha1.OpenBaoTenant {
	return &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-tenant",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: namespace,
		},
	}
}

func TestNewManager_InvalidNamespacePodSecurityLabelsMode(t *testing.T) {
	t.Setenv(constants.EnvTenantNamespacePodSecurityLabelsMode, "unsupported")

	_, err := NewManager(newTestClient(t), logr.Discard())
	if err == nil {
		t.Fatal("NewManager() error = nil, want invalid namespace Pod Security labels mode error")
	}
	for _, want := range []string{
		constants.EnvTenantNamespacePodSecurityLabelsMode,
		NamespacePodSecurityLabelsModeEnforce,
		NamespacePodSecurityLabelsModeExternal,
		"unsupported",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("NewManager() error = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestEnsureTenantRBAC_CreatesRoleAndRoleBinding(t *testing.T) {
	namespace := testNamespace
	// Create namespace for Pod Security labels test
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	k8sClient := newTestClient(t, ns)
	logger := logr.Discard()
	manager, err := NewManager(k8sClient, logger)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()

	// Create namespace if it doesn't exist (for Pod Security labels)
	existingNS := &corev1.Namespace{}
	err = k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, existingNS)
	if err != nil && apierrors.IsNotFound(err) {
		newNS := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		if createErr := k8sClient.Create(ctx, newNS); createErr != nil {
			t.Fatalf("failed to create namespace: %v", createErr)
		}
	}

	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-tenant",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: namespace,
		},
	}

	err = manager.EnsureTenantRBAC(ctx, tenant)
	if err != nil {
		t.Fatalf("EnsureTenantRBAC() error = %v", err)
	}

	// Verify Role was created
	role := &rbacv1.Role{}
	err = k8sClient.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      TenantRoleName,
	}, role)
	if err != nil {
		t.Fatalf("expected Role to exist: %v", err)
	}

	if role.Name != TenantRoleName {
		t.Errorf("Role name = %v, want %v", role.Name, TenantRoleName)
	}
	if role.Namespace != namespace {
		t.Errorf("Role namespace = %v, want %v", role.Namespace, namespace)
	}

	// Verify RoleBinding was created
	roleBinding := &rbacv1.RoleBinding{}
	err = k8sClient.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      TenantRoleBindingName,
	}, roleBinding)
	if err != nil {
		t.Fatalf("expected RoleBinding to exist: %v", err)
	}

	if roleBinding.Name != TenantRoleBindingName {
		t.Errorf("RoleBinding name = %v, want %v", roleBinding.Name, TenantRoleBindingName)
	}
	if roleBinding.Namespace != namespace {
		t.Errorf("RoleBinding namespace = %v, want %v", roleBinding.Namespace, namespace)
	}

	// Verify Pod Security labels were applied to namespace
	nsForLabels := &corev1.Namespace{}
	err = k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, nsForLabels)
	if err != nil {
		t.Fatalf("expected Namespace to exist: %v", err)
	}

	expectedLabels := map[string]string{
		"pod-security.kubernetes.io/enforce": podSecurityRestrictedLevel,
		"pod-security.kubernetes.io/audit":   podSecurityRestrictedLevel,
		"pod-security.kubernetes.io/warn":    podSecurityRestrictedLevel,
	}

	for key, expectedValue := range expectedLabels {
		if actualValue, exists := nsForLabels.Labels[key]; !exists {
			t.Errorf("Namespace missing Pod Security label %q", key)
		} else if actualValue != expectedValue {
			t.Errorf("Namespace label %q = %q, want %q", key, actualValue, expectedValue)
		}
	}
}

func TestEnsureTenantRBAC_ExternalNamespacePodSecurityLabelsModeSkipsNamespaceUpdate(t *testing.T) {
	t.Setenv(constants.EnvTenantNamespacePodSecurityLabelsMode, NamespacePodSecurityLabelsModeExternal)

	namespace := testNamespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"existing-label": "value",
			},
		},
	}
	baseClient := newTestClient(t, ns)
	k8sClient := namespaceUpdateDenyingClient{Client: baseClient}
	manager, err := NewManager(k8sClient, logr.Discard())
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()
	if err := manager.EnsureTenantRBAC(ctx, newTestTenant(namespace)); err != nil {
		t.Fatalf("EnsureTenantRBAC() error = %v", err)
	}

	role := &rbacv1.Role{}
	if err := baseClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: TenantRoleName}, role); err != nil {
		t.Fatalf("expected Role to exist: %v", err)
	}
	roleBinding := &rbacv1.RoleBinding{}
	if err := baseClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: TenantRoleBindingName}, roleBinding); err != nil {
		t.Fatalf("expected RoleBinding to exist: %v", err)
	}

	updatedNS := &corev1.Namespace{}
	if err := baseClient.Get(ctx, types.NamespacedName{Name: namespace}, updatedNS); err != nil {
		t.Fatalf("expected Namespace to exist: %v", err)
	}
	if updatedNS.Labels["existing-label"] != "value" {
		t.Errorf("Namespace pre-existing label was not preserved")
	}
	for _, key := range []string{
		"pod-security.kubernetes.io/enforce",
		"pod-security.kubernetes.io/audit",
		"pod-security.kubernetes.io/warn",
	} {
		if _, exists := updatedNS.Labels[key]; exists {
			t.Errorf("Namespace has Pod Security label %q in external mode", key)
		}
	}
}

func TestEnsureTenantRBAC_EnforceNamespacePodSecurityLabelsModeTreatsNamespaceUpdateDenialAsFatal(t *testing.T) {
	t.Setenv(constants.EnvTenantNamespacePodSecurityLabelsMode, NamespacePodSecurityLabelsModeEnforce)

	namespace := testNamespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	k8sClient := namespaceUpdateDenyingClient{Client: newTestClient(t, ns)}
	manager, err := NewManager(k8sClient, logr.Discard())
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	err = manager.EnsureTenantRBAC(context.Background(), newTestTenant(namespace))
	if err == nil {
		t.Fatal("EnsureTenantRBAC() error = nil, want namespace update denial")
	}
	for _, want := range []string{
		"failed to update namespace",
		"namespace updates are managed by platform policy",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("EnsureTenantRBAC() error = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestEnsureTenantRBAC_UpdatesRoleWhenRulesChange(t *testing.T) {
	namespace := testNamespace
	// Create namespace first (required for Pod Security label updates)
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	existingRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TenantRoleName,
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"openbao.org"},
				Resources: []string{"openbaoclusters"},
				Verbs:     []string{"get"}, // Different from expected
			},
		},
	}

	k8sClient := newTestClient(t, ns, existingRole)
	logger := logr.Discard()
	manager, err := NewManager(k8sClient, logger)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()

	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-tenant",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: namespace,
		},
	}
	err = manager.EnsureTenantRBAC(ctx, tenant)
	if err != nil {
		t.Fatalf("EnsureTenantRBAC() error = %v", err)
	}

	// Verify Role was updated
	role := &rbacv1.Role{}
	err = k8sClient.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      TenantRoleName,
	}, role)
	if err != nil {
		t.Fatalf("expected Role to exist: %v", err)
	}

	// Verify rules were updated
	expectedRules := len(GenerateTenantRole(namespace).Rules)
	if len(role.Rules) != expectedRules {
		t.Errorf("Role rules count = %v, want %v", len(role.Rules), expectedRules)
	}

	// Verify at least one rule has the expected OpenBaoCluster permissions
	hasExpectedRule := false
	for _, rule := range role.Rules {
		if slices.Contains(rule.APIGroups, "openbao.org") &&
			slices.Contains(rule.Resources, "openbaoclusters") &&
			slices.Contains(rule.Verbs, "get") &&
			slices.Contains(rule.Verbs, "create") {
			hasExpectedRule = true
			break
		}
	}
	if !hasExpectedRule {
		t.Error("Role was not updated with expected OpenBaoCluster rule")
	}
}

func TestEnsureTenantRBAC_UpdatesRoleBindingWhenSubjectsChange(t *testing.T) {
	namespace := testNamespace
	// Create namespace first (required for Pod Security label updates)
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	existingRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TenantRoleBindingName,
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     TenantRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "old-operator",
				Namespace: "old-namespace",
			},
		},
	}

	k8sClient := newTestClient(t, ns, existingRoleBinding)
	logger := logr.Discard()
	manager, err := NewManager(k8sClient, logger)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()

	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-tenant",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: namespace,
		},
	}
	err = manager.EnsureTenantRBAC(ctx, tenant)
	if err != nil {
		t.Fatalf("EnsureTenantRBAC() error = %v", err)
	}

	// Verify RoleBinding was updated
	roleBinding := &rbacv1.RoleBinding{}
	err = k8sClient.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      TenantRoleBindingName,
	}, roleBinding)
	if err != nil {
		t.Fatalf("expected RoleBinding to exist: %v", err)
	}

	// Verify subject was updated
	if len(roleBinding.Subjects) != 1 {
		t.Fatalf("RoleBinding subjects count = %v, want 1", len(roleBinding.Subjects))
	}

	subject := roleBinding.Subjects[0]
	// NewManager uses default "openbao-operator-controller" if OPERATOR_SERVICE_ACCOUNT_NAME is not set
	expectedName := "openbao-operator-controller"
	if subject.Name != expectedName {
		t.Errorf("RoleBinding subject.Name = %v, want %v", subject.Name, expectedName)
	}
	if subject.Namespace != "openbao-operator-system" {
		t.Errorf("RoleBinding subject.Namespace = %v, want openbao-operator-system", subject.Namespace)
	}
}

func TestEnsureTenantRBAC_HandlesAlreadyExistsGracefully(t *testing.T) {
	namespace := testNamespace
	// Create namespace first (required for Pod Security label updates)
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	existingRole := GenerateTenantRole(namespace)
	existingRoleBinding := GenerateTenantRoleBinding(namespace, OperatorServiceAccount{
		Name:      "controller-manager",
		Namespace: "openbao-operator-system",
	})

	k8sClient := newTestClient(t, ns, existingRole, existingRoleBinding)
	logger := logr.Discard()
	manager, err := NewManager(k8sClient, logger)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()

	// Should not error when resources already exist with correct content
	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-tenant",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: namespace,
		},
	}
	err = manager.EnsureTenantRBAC(ctx, tenant)
	if err != nil {
		t.Fatalf("EnsureTenantRBAC() error = %v", err)
	}
}

func TestCleanupTenantResources_DeletesRBACAndGovernanceResources(t *testing.T) {
	namespace := testNamespace
	podSecurityLabels := map[string]string{
		"pod-security.kubernetes.io/enforce": "restricted",
		"pod-security.kubernetes.io/audit":   "restricted",
		"pod-security.kubernetes.io/warn":    "restricted",
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: podSecurityLabels}}
	existingRole := GenerateTenantRole(namespace)
	existingRoleBinding := GenerateTenantRoleBinding(namespace, OperatorServiceAccount{
		Name:      "controller-manager",
		Namespace: "openbao-operator-system",
	})
	existingQuota := GenerateTenantResourceQuota(namespace, nil)
	existingLimitRange := GenerateTenantLimitRange(namespace, nil)

	k8sClient := newTestClient(t, ns, existingRole, existingRoleBinding, existingQuota, existingLimitRange)
	logger := logr.Discard()
	manager, err := NewManager(k8sClient, logger)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()

	err = manager.CleanupTenantResources(ctx, namespace)
	if err != nil {
		t.Fatalf("CleanupTenantResources() error = %v", err)
	}

	for _, obj := range []client.Object{
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: TenantRoleBindingName, Namespace: namespace}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: TenantRoleName, Namespace: namespace}},
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: TenantResourceQuotaName, Namespace: namespace}},
		&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: TenantLimitRangeName, Namespace: namespace}},
	} {
		key := types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}
		if getErr := k8sClient.Get(ctx, key, obj); !apierrors.IsNotFound(getErr) {
			t.Errorf("expected %T %s to be deleted, got error: %v", obj, key, getErr)
		}
	}

	updatedNamespace := &corev1.Namespace{}
	if getErr := k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, updatedNamespace); getErr != nil {
		t.Fatalf("get namespace after cleanup: %v", getErr)
	}
	if !reflect.DeepEqual(updatedNamespace.Labels, podSecurityLabels) {
		t.Fatalf("namespace labels = %#v, want unchanged %#v", updatedNamespace.Labels, podSecurityLabels)
	}
}

func TestCleanupTenantResources_HandlesNotFoundGracefully(t *testing.T) {
	namespace := testNamespace
	k8sClient := newTestClient(t)
	logger := logr.Discard()
	manager, err := NewManager(k8sClient, logger)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()

	// Should not error when resources don't exist
	err = manager.CleanupTenantResources(ctx, namespace)
	if err != nil {
		t.Fatalf("CleanupTenantResources() error = %v", err)
	}
}

func TestCleanupTenantResourcesPinsGovernanceUIDs(t *testing.T) {
	namespace := testNamespace
	quota := GenerateTenantResourceQuota(namespace, nil)
	quota.UID = "quota-uid"
	limitRange := GenerateTenantLimitRange(namespace, nil)
	limitRange.UID = "limits-uid"
	var deleted int
	k8sClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(quota, limitRange).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				options := (&client.DeleteOptions{}).ApplyOptions(opts)
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != obj.GetUID() {
					t.Fatalf("deletion must use the observed UID for %T", obj)
				}
				deleted++
				return c.Delete(ctx, obj, opts...)
			},
		}).Build()
	manager, err := NewManager(k8sClient, logr.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CleanupTenantResources(t.Context(), namespace); err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("deleted %d resources, want 2", deleted)
	}
}

func TestCleanupTenantResources_ReturnsGovernanceDeleteError(t *testing.T) {
	deleteErr := apierrors.NewForbidden(
		corev1.Resource("resourcequotas"),
		TenantResourceQuotaName,
		errors.New("delete denied"),
	)
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(GenerateTenantResourceQuota(testNamespace, nil)).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				return deleteErr
			},
		}).
		Build()
	manager, err := NewManager(k8sClient, logr.Discard())
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	err = manager.CleanupTenantResources(context.Background(), testNamespace)
	if err == nil || !errors.Is(err, deleteErr) {
		t.Fatalf("CleanupTenantResources() error = %v, want delete error", err)
	}
	for _, want := range []string{"ResourceQuota", testNamespace, TenantResourceQuotaName} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("CleanupTenantResources() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestIsTenantNamespaceProvisioned(t *testing.T) {
	t.Run("rejects empty namespace", func(t *testing.T) {
		manager, err := NewManager(newTestClient(t), logr.Discard())
		if err != nil {
			t.Fatalf("NewManager() failed: %v", err)
		}
		if _, err := manager.IsTenantNamespaceProvisioned(context.Background(), ""); err == nil {
			t.Fatal("IsTenantNamespaceProvisioned() error = nil, want namespace validation error")
		}
	})

	t.Run("reports missing RoleBinding", func(t *testing.T) {
		manager, err := NewManager(newTestClient(t), logr.Discard())
		if err != nil {
			t.Fatalf("NewManager() failed: %v", err)
		}
		provisioned, err := manager.IsTenantNamespaceProvisioned(context.Background(), testNamespace)
		if err != nil || provisioned {
			t.Fatalf("IsTenantNamespaceProvisioned() = (%v, %v), want (false, nil)", provisioned, err)
		}
	})

	t.Run("reports existing RoleBinding", func(t *testing.T) {
		binding := GenerateTenantRoleBinding(testNamespace, OperatorServiceAccount{Name: "controller", Namespace: "operator"})
		manager, err := NewManager(newTestClient(t, binding), logr.Discard())
		if err != nil {
			t.Fatalf("NewManager() failed: %v", err)
		}
		provisioned, err := manager.IsTenantNamespaceProvisioned(context.Background(), testNamespace)
		if err != nil || !provisioned {
			t.Fatalf("IsTenantNamespaceProvisioned() = (%v, %v), want (true, nil)", provisioned, err)
		}
	})

	t.Run("returns RoleBinding read error", func(t *testing.T) {
		readErr := errors.New("RoleBinding read failed")
		k8sClient := fake.NewClientBuilder().
			WithScheme(testScheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return readErr
				},
			}).
			Build()
		manager, err := NewManager(k8sClient, logr.Discard())
		if err != nil {
			t.Fatalf("NewManager() failed: %v", err)
		}
		if _, err := manager.IsTenantNamespaceProvisioned(context.Background(), testNamespace); !errors.Is(err, readErr) {
			t.Fatalf("IsTenantNamespaceProvisioned() error = %v, want read error", err)
		}
	})
}

func TestCleanupTenantResources_ReturnsRBACOperationError(t *testing.T) {
	operatorSA := OperatorServiceAccount{Name: "controller", Namespace: "operator"}
	for _, tc := range []struct {
		name      string
		operation string
		object    client.Object
	}{
		{
			name:      "RoleBinding delete",
			operation: "delete",
			object:    GenerateTenantSecretsReaderRoleBinding(testNamespace, operatorSA),
		},
		{
			name:      "Role delete",
			operation: "delete",
			object:    GenerateTenantSecretsReaderRole(testNamespace, nil),
		},
		{
			name:      "RoleBinding get",
			operation: "get",
			object:    GenerateTenantSecretsReaderRoleBinding(testNamespace, operatorSA),
		},
		{
			name:      "Role get",
			operation: "get",
			object:    GenerateTenantSecretsReaderRole(testNamespace, nil),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rbacErr := errors.New("RBAC operation failed")
			k8sClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithObjects(tc.object).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if tc.operation == "get" && key.Name == tc.object.GetName() {
							return rbacErr
						}
						return c.Get(ctx, key, obj, opts...)
					},
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						if tc.operation == "delete" && obj.GetName() == tc.object.GetName() {
							return rbacErr
						}
						return c.Delete(ctx, obj, opts...)
					},
				}).
				Build()
			manager, err := NewManager(k8sClient, logr.Discard())
			if err != nil {
				t.Fatalf("NewManager() failed: %v", err)
			}

			if err := manager.CleanupTenantResources(context.Background(), testNamespace); !errors.Is(err, rbacErr) {
				t.Fatalf("CleanupTenantResources() error = %v, want RBAC operation error", err)
			}
		})
	}
}

func TestEnsureTenantRBAC_AppliesPodSecurityLabels(t *testing.T) {
	namespace := testNamespace
	// Create namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				// Pre-existing label that should be preserved
				"existing-label": "value",
			},
		},
	}
	k8sClient := newTestClient(t, ns)
	logger := logr.Discard()
	manager, err := NewManager(k8sClient, logger)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()

	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-tenant",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: namespace,
		},
	}
	err = manager.EnsureTenantRBAC(ctx, tenant)
	if err != nil {
		t.Fatalf("EnsureTenantRBAC() error = %v", err)
	}

	// Verify Pod Security labels were applied
	updatedNS := &corev1.Namespace{}
	err = k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, updatedNS)
	if err != nil {
		t.Fatalf("expected Namespace to exist: %v", err)
	}

	expectedLabels := map[string]string{
		"pod-security.kubernetes.io/enforce": podSecurityRestrictedLevel,
		"pod-security.kubernetes.io/audit":   podSecurityRestrictedLevel,
		"pod-security.kubernetes.io/warn":    podSecurityRestrictedLevel,
	}

	for key, expectedValue := range expectedLabels {
		if actualValue, exists := updatedNS.Labels[key]; !exists {
			t.Errorf("Namespace missing Pod Security label %q", key)
		} else if actualValue != expectedValue {
			t.Errorf("Namespace label %q = %q, want %q", key, actualValue, expectedValue)
		}
	}

	// Verify pre-existing labels are preserved
	if updatedNS.Labels["existing-label"] != "value" {
		t.Errorf("Namespace pre-existing label was not preserved")
	}
}

func TestEnsureTenantRBAC_UpdatesPodSecurityLabels(t *testing.T) {
	namespace := testNamespace
	// Create namespace with incorrect Pod Security labels
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"pod-security.kubernetes.io/enforce": "privileged", // Wrong value
				"pod-security.kubernetes.io/audit":   "baseline",   // Wrong value
				// Missing warn label
			},
		},
	}
	k8sClient := newTestClient(t, ns)
	logger := logr.Discard()
	manager, err := NewManager(k8sClient, logger)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()

	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-tenant",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: namespace,
		},
	}
	err = manager.EnsureTenantRBAC(ctx, tenant)
	if err != nil {
		t.Fatalf("EnsureTenantRBAC() error = %v", err)
	}

	// Verify Pod Security labels were updated to restricted
	updatedNS := &corev1.Namespace{}
	err = k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, updatedNS)
	if err != nil {
		t.Fatalf("expected Namespace to exist: %v", err)
	}

	if updatedNS.Labels["pod-security.kubernetes.io/enforce"] != podSecurityRestrictedLevel {
		t.Errorf("Pod Security enforce label was not updated to restricted")
	}
	if updatedNS.Labels["pod-security.kubernetes.io/audit"] != podSecurityRestrictedLevel {
		t.Errorf("Pod Security audit label was not updated to restricted")
	}
	if updatedNS.Labels["pod-security.kubernetes.io/warn"] != podSecurityRestrictedLevel {
		t.Errorf("Pod Security warn label was not added")
	}
}

func TestEnsureTenantSecretRBAC_CreatesRolesAndRoleBindings(t *testing.T) {
	namespace := testNamespace
	clusterName := "bao"

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.4.0",
			Image:    "openbao:2.4.0",
			Replicas: 1,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled: true,
				Mode:    openbaov1alpha1.TLSModeOperatorManaged,
			},
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "static",
				CredentialsSecretRef: &corev1.LocalObjectReference{
					Name: "unseal-creds",
				},
			},
			Backup: &openbaov1alpha1.BackupSchedule{
				Schedule: "0 3 * * *",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "https://s3.example",
					Bucket:   "bucket",
					CredentialsSecretRef: &corev1.LocalObjectReference{
						Name: "backup-creds",
					},
				},
				TokenSecretRef: &corev1.LocalObjectReference{
					Name: "backup-token",
				},
			},
			ImageVerification: &openbaov1alpha1.ImageVerificationConfig{
				Enabled: true,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{Name: "main-registry-creds"},
				},
			},
			OperatorImageVerification: &openbaov1alpha1.ImageVerificationConfig{
				Enabled: true,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{Name: "helper-registry-creds"},
				},
			},
		},
	}
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-a",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: clusterName,
			Source: openbaov1alpha1.RestoreSource{
				Target: openbaov1alpha1.BackupTarget{
					Bucket: "restore-bucket",
					CredentialsSecretRef: &corev1.LocalObjectReference{
						Name: "restore-creds",
					},
				},
				Key: "snapshots/demo.snap",
			},
			TokenSecretRef: &corev1.LocalObjectReference{
				Name: "restore-token",
			},
		},
	}

	k8sClient := newTestClient(t, cluster, restore)
	logger := logr.Discard()
	manager, err := NewManager(k8sClient, logger)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()
	if err := manager.EnsureTenantSecretRBAC(ctx, namespace); err != nil {
		t.Fatalf("EnsureTenantSecretRBAC() error = %v", err)
	}

	expectedWriterNames := []string{
		clusterName + constants.SuffixRootToken,
		clusterName + constants.SuffixTLSCA,
		clusterName + constants.SuffixTLSServer,
		clusterName + constants.SuffixUnsealKey,
	}
	sort.Strings(expectedWriterNames)

	expectedReaderNames := []string{
		"backup-creds",
		"backup-token",
		"helper-registry-creds",
		"main-registry-creds",
		"restore-creds",
		"restore-token",
		"unseal-creds",
	}
	sort.Strings(expectedReaderNames)

	writerRole := &rbacv1.Role{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: TenantSecretsWriterRoleName}, writerRole); err != nil {
		t.Fatalf("expected writer Role to exist: %v", err)
	}
	if got := extractSecretResourceNames(writerRole.Rules); !reflect.DeepEqual(got, expectedWriterNames) {
		t.Errorf("writer Role allowlist = %v, want %v", got, expectedWriterNames)
	}

	writerRoleBinding := &rbacv1.RoleBinding{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: TenantSecretsWriterRoleBindingName}, writerRoleBinding); err != nil {
		t.Fatalf("expected writer RoleBinding to exist: %v", err)
	}
	if writerRoleBinding.RoleRef.Name != TenantSecretsWriterRoleName {
		t.Errorf("writer RoleBinding RoleRef.Name = %v, want %v", writerRoleBinding.RoleRef.Name, TenantSecretsWriterRoleName)
	}

	readerRole := &rbacv1.Role{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: TenantSecretsReaderRoleName}, readerRole); err != nil {
		t.Fatalf("expected reader Role to exist: %v", err)
	}
	if got := extractSecretResourceNames(readerRole.Rules); !reflect.DeepEqual(got, expectedReaderNames) {
		t.Errorf("reader Role allowlist = %v, want %v", got, expectedReaderNames)
	}

	readerRoleBinding := &rbacv1.RoleBinding{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: TenantSecretsReaderRoleBindingName}, readerRoleBinding); err != nil {
		t.Fatalf("expected reader RoleBinding to exist: %v", err)
	}
	if readerRoleBinding.RoleRef.Name != TenantSecretsReaderRoleName {
		t.Errorf("reader RoleBinding RoleRef.Name = %v, want %v", readerRoleBinding.RoleRef.Name, TenantSecretsReaderRoleName)
	}
}

func TestEnsureTenantSecretRBAC_IncludesReservedRetentionNames(t *testing.T) {
	namespace := testNamespace
	clusterName := "self-init-external"
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{Enabled: true},
			Unseal:   &openbaov1alpha1.UnsealConfig{Type: "awskms"},
		},
	}

	k8sClient := newTestClient(t, cluster)
	manager, err := NewManager(k8sClient, logr.Discard())
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	if err := manager.EnsureTenantSecretRBAC(context.Background(), namespace); err != nil {
		t.Fatalf("EnsureTenantSecretRBAC() error = %v", err)
	}

	writerRole := &rbacv1.Role{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Namespace: namespace,
		Name:      TenantSecretsWriterRoleName,
	}, writerRole); err != nil {
		t.Fatalf("expected writer Role to exist: %v", err)
	}

	resourceNames := extractSecretResourceNames(writerRole.Rules)
	for _, name := range []string{
		clusterName + constants.SuffixRootToken,
		clusterName + constants.SuffixUnsealKey,
	} {
		if !slices.Contains(resourceNames, name) {
			t.Errorf("writer Role allowlist = %v, want reserved retention Secret %q", resourceNames, name)
		}
	}
}

func TestAccumulateRestoreTenantSecretNames_SkipsTokenSecretWhenJWTAuthConfigured(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster:     "bao",
			JWTAuthRole: "restore-role",
			Source: openbaov1alpha1.RestoreSource{
				Target: openbaov1alpha1.BackupTarget{
					CredentialsSecretRef: &corev1.LocalObjectReference{Name: "restore-creds"},
				},
			},
			TokenSecretRef: &corev1.LocalObjectReference{Name: "restore-token"},
		},
	}
	readerNames := map[string]struct{}{}

	accumulateRestoreTenantSecretNames(restore, nil, readerNames)

	got := sortedUniqueSecretNames(readerNames)
	want := []string{"restore-creds"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restore reader allowlist = %v, want %v", got, want)
	}
}

func TestEnsureTenantSecretRBAC_DeletesRolesAndRoleBindingsWhenNoClustersRemain(t *testing.T) {
	namespace := testNamespace

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bao",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.4.0",
			Image:    "openbao:2.4.0",
			Replicas: 1,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled: true,
				Mode:    openbaov1alpha1.TLSModeOperatorManaged,
			},
		},
	}

	k8sClient := newTestClient(t, cluster)
	logger := logr.Discard()
	manager, err := NewManager(k8sClient, logger)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()

	if err := manager.EnsureTenantSecretRBAC(ctx, namespace); err != nil {
		t.Fatalf("EnsureTenantSecretRBAC() error = %v", err)
	}

	if err := k8sClient.Delete(ctx, cluster); err != nil {
		t.Fatalf("failed to delete OpenBaoCluster: %v", err)
	}

	if err := manager.EnsureTenantSecretRBAC(ctx, namespace); err != nil {
		t.Fatalf("EnsureTenantSecretRBAC() error = %v", err)
	}

	for _, name := range []string{TenantSecretsReaderRoleBindingName, TenantSecretsWriterRoleBindingName} {
		roleBinding := &rbacv1.RoleBinding{}
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, roleBinding)
		if !apierrors.IsNotFound(err) {
			t.Errorf("expected RoleBinding %s to be deleted, got error: %v", name, err)
		}
	}

	for _, name := range []string{TenantSecretsReaderRoleName, TenantSecretsWriterRoleName} {
		role := &rbacv1.Role{}
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, role)
		if !apierrors.IsNotFound(err) {
			t.Errorf("expected Role %s to be deleted, got error: %v", name, err)
		}
	}
}

func extractSecretResourceNames(rules []rbacv1.PolicyRule) []string {
	var out []string
	for i := range rules {
		rule := rules[i]
		if !slices.Contains(rule.Resources, "secrets") {
			continue
		}
		if len(rule.ResourceNames) == 0 {
			continue
		}
		out = append(out, rule.ResourceNames...)
	}
	sort.Strings(out)
	return out
}

func TestEnsureTenantRBAC_AppliesConfiguredQuotas(t *testing.T) {
	namespace := testNamespace
	// Create namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	k8sClient := newTestClient(t, ns)
	logger := logr.Discard()
	manager, err := NewManager(k8sClient, logger)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	ctx := context.Background()

	// Define custom quota and limit range
	customQuota := &corev1.ResourceQuotaSpec{
		Hard: corev1.ResourceList{
			corev1.ResourcePods: resource.MustParse("10"),
		},
	}
	customLimitRange := &corev1.LimitRangeSpec{
		Limits: []corev1.LimitRangeItem{
			{
				Type: corev1.LimitTypeContainer,
				Default: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("100m"),
				},
			},
		},
	}

	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-tenant",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: namespace,
			Quota:           customQuota,
			LimitRange:      customLimitRange,
		},
	}

	err = manager.EnsureTenantRBAC(ctx, tenant)
	if err != nil {
		t.Fatalf("EnsureTenantRBAC() error = %v", err)
	}

	// Verify ResourceQuota
	quota := &corev1.ResourceQuota{}
	err = k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: TenantResourceQuotaName}, quota)
	if err != nil {
		t.Fatalf("expected ResourceQuota to exist: %v", err)
	}
	pods := quota.Spec.Hard[corev1.ResourcePods]
	if pods.String() != "10" {
		t.Errorf("ResourceQuota pods limit = %v, want 10", pods.String())
	}

	// Verify LimitRange
	limitRange := &corev1.LimitRange{}
	err = k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: TenantLimitRangeName}, limitRange)
	if err != nil {
		t.Fatalf("expected LimitRange to exist: %v", err)
	}
	cpu := limitRange.Spec.Limits[0].Default[corev1.ResourceCPU]
	if cpu.String() != "100m" {
		t.Errorf("LimitRange default CPU = %v, want 100m", cpu.String())
	}
}
