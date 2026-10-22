package openbaocluster

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestPauseForTenantOnboardingValidatesHandoff(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "operator-system")
	t.Setenv("OPERATOR_SERVICE_ACCOUNT_NAME", "controller")
	tests := []struct {
		name   string
		mutate func(*rbacv1.RoleBinding)
		ready  bool
	}{
		{name: "expected binding", ready: true},
		{name: "wrong role name", mutate: func(b *rbacv1.RoleBinding) { b.RoleRef.Name = "other-role" }},
		{name: "ClusterRole reference", mutate: func(b *rbacv1.RoleBinding) { b.RoleRef.Kind = "ClusterRole" }},
		{name: "wrong role API group", mutate: func(b *rbacv1.RoleBinding) { b.RoleRef.APIGroup = "other.example" }},
		{name: "no subjects", mutate: func(b *rbacv1.RoleBinding) { b.Subjects = nil }},
		{name: "wrong account name", mutate: func(b *rbacv1.RoleBinding) { b.Subjects[0].Name = "other-controller" }},
		{name: "wrong account namespace", mutate: func(b *rbacv1.RoleBinding) { b.Subjects[0].Namespace = "other-system" }},
		{name: "user subject", mutate: func(b *rbacv1.RoleBinding) { b.Subjects[0].Kind = "User" }},
		{name: "wrong subject API group", mutate: func(b *rbacv1.RoleBinding) { b.Subjects[0].APIGroup = rbacv1.GroupName }},
		{name: "extra subject", mutate: func(b *rbacv1.RoleBinding) { b.Subjects = append(b.Subjects, b.Subjects[0]) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binding := tenantOnboardingBinding("operator-system", "controller")
			if tt.mutate != nil {
				tt.mutate(binding)
			}
			c := fake.NewClientBuilder().WithScheme(newOpenBaoClusterTestScheme(t)).WithObjects(binding).Build()
			parent := &OpenBaoClusterReconciler{Client: c}
			result, err, blocked := parent.pauseForTenantOnboarding(context.Background(), logr.Discard(), "test", "tenant")
			require.NoError(t, err)
			require.Equal(t, !tt.ready, blocked)
			if !tt.ready {
				require.Equal(t, constants.RequeueShort, result.RequeueAfter)
				binding.RoleRef = tenantOnboardingBinding("operator-system", "controller").RoleRef
				binding.Subjects = tenantOnboardingBinding("operator-system", "controller").Subjects
				require.NoError(t, c.Update(context.Background(), binding))
				_, err, blocked = parent.pauseForTenantOnboarding(context.Background(), logr.Discard(), "test", "tenant")
				require.NoError(t, err)
				require.False(t, blocked, "repair must resume reconciliation")
			}
		})
	}
}

func TestTenantRoleBindingReadyInstallationIdentity(t *testing.T) {
	tests := []struct {
		name, podNamespace, operatorNamespace, accountName, wantNamespace, wantName string
	}{
		{name: "defaults", wantNamespace: "openbao-operator-system", wantName: "openbao-operator-controller"},
		{name: "operator namespace fallback", operatorNamespace: "fallback", wantNamespace: "fallback", wantName: "openbao-operator-controller"},
		{name: "Pod namespace wins", podNamespace: "pod-system", operatorNamespace: "fallback", accountName: "custom", wantNamespace: "pod-system", wantName: "custom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("POD_NAMESPACE", tt.podNamespace)
			t.Setenv("OPERATOR_NAMESPACE", tt.operatorNamespace)
			t.Setenv("OPERATOR_SERVICE_ACCOUNT_NAME", tt.accountName)
			binding := tenantOnboardingBinding(tt.wantNamespace, tt.wantName)
			require.True(t, tenantRoleBindingReady(binding))
			deletedAt := metav1.Now()
			binding.DeletionTimestamp = &deletedAt
			require.False(t, tenantRoleBindingReady(binding), "a deleting binding is not a completed handoff")
		})
	}
}

func TestPauseForTenantOnboardingReadAndBypass(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "operator-system")
	t.Setenv("OPERATOR_SERVICE_ACCOUNT_NAME", "controller")
	c := fake.NewClientBuilder().WithScheme(newOpenBaoClusterTestScheme(t)).Build()
	parent := &OpenBaoClusterReconciler{Client: c}
	result, err, blocked := parent.pauseForTenantOnboarding(context.Background(), logr.Discard(), "test", "tenant")
	require.NoError(t, err)
	require.True(t, blocked)
	require.Equal(t, constants.RequeueShort, result.RequeueAfter)

	injected := errors.New("onboarding read failed")
	parent.APIReader = interceptor.NewClient(c, interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return injected
		},
	})
	_, err, blocked = parent.pauseForTenantOnboarding(context.Background(), logr.Discard(), "test", "tenant")
	require.ErrorIs(t, err, injected)
	require.True(t, blocked)

	parent.SingleTenantMode = true
	_, err, blocked = parent.pauseForTenantOnboarding(context.Background(), logr.Discard(), "test", "tenant")
	require.NoError(t, err)
	require.False(t, blocked)

	parent.SingleTenantMode = false
	parent.APIReader = fake.NewClientBuilder().WithScheme(newOpenBaoClusterTestScheme(t)).
		WithObjects(tenantOnboardingBinding("operator-system", "controller")).Build()
	_, err, blocked = parent.pauseForTenantOnboarding(context.Background(), logr.Discard(), "test", "tenant")
	require.NoError(t, err)
	require.False(t, blocked, "handoff must use the APIReader instead of a stale client")
}

func tenantOnboardingBinding(namespace, name string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: constants.TenantRoleBindingName, Namespace: "tenant"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: constants.TenantRoleName},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: namespace, Name: name}},
	}
}
