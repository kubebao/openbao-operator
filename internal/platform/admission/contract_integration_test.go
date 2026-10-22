//go:build integration

package admission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// A dedicated API server keeps policy weakening isolated from other suites.
func TestAdmissionContractDriftAndRepairWithAPIServer(t *testing.T) {
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "")
	for _, key := range []string{"POD_NAMESPACE", "OPERATOR_NAMESPACE", "OPERATOR_NAME_PREFIX", "OPERATOR_SERVICE_ACCOUNT_NAME", "OPERATOR_PROVISIONER_SERVICE_ACCOUNT_NAME", "OPENBAO_TENANT_NAMESPACE_POD_SECURITY_LABELS_MODE"} {
		t.Setenv(key, "")
	}
	environment := &envtest.Environment{}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	kube, err := client.New(config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	ctx := context.Background()
	for _, dep := range DefaultDependencies() {
		for _, fixture := range []struct {
			name   string
			object client.Object
		}{
			{dep.PolicyName, &admissionv1.ValidatingAdmissionPolicy{}},
			{dep.BindingName, &admissionv1.ValidatingAdmissionPolicyBinding{}},
		} {
			data, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "policy", fixture.name+".yaml"))
			require.NoError(t, err)
			require.NoError(t, yaml.UnmarshalStrict(data, fixture.object))
			require.NoError(t, kube.Create(ctx, fixture.object))
		}
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "contract-tenant"}}
	require.NoError(t, kube.Create(ctx, namespace))
	tracker := NewTracker(kube, DefaultDependencies(), []string{""}, time.Hour)
	t.Cleanup(func() { SetAdmissionDependenciesReady(false) })
	check := func(ready bool) {
		t.Helper()
		status, err := tracker.Refresh(ctx)
		require.NoError(t, err)
		require.Equal(t, ready, status.OverallReady, status.SummaryMessage())
		require.Equal(t, ready, AdmissionDependenciesReady())
	}
	check(true) // Includes the API server's defaulted specifications.

	impersonated := func(username string) client.Client {
		cfg := rest.CopyConfig(config)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: username, Groups: []string{"system:masters"}}
		result, err := client.New(cfg, client.Options{Scheme: scheme})
		require.NoError(t, err)
		return result
	}
	controller := impersonated("system:serviceaccount:openbao-operator-system:openbao-operator-controller")
	provisioner := impersonated("system:serviceaccount:openbao-operator-system:openbao-operator-provisioner")
	secretProbe := func() error {
		return controller.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unmanaged-secret", Namespace: namespace.Name}}, client.DryRunAll)
	}
	awaitProbe := func(denied bool) {
		t.Helper()
		require.Eventually(t, func() bool {
			err := secretProbe()
			if !denied {
				return err == nil
			}
			return err != nil && strings.Contains(err.Error(), "fixed cluster-scoped names and labels")
		}, 10*time.Second, 50*time.Millisecond)
	}
	awaitProbe(true)
	require.Eventually(t, func() bool {
		err := provisioner.Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "unapproved-role", Namespace: namespace.Name}}, client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "Provisioner can only create Roles")
	}, 10*time.Second, 50*time.Millisecond)

	policy := &admissionv1.ValidatingAdmissionPolicy{}
	binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
	require.NoError(t, kube.Get(ctx, client.ObjectKey{Name: dependencyOpenBaoRestrictControllerSecretWrites}, policy))
	require.NoError(t, kube.Get(ctx, client.ObjectKey{Name: dependencyBindingRestrictControllerSecretWrites}, binding))
	originalPolicy, originalBinding := policy.Spec.DeepCopy(), binding.Spec.DeepCopy()
	// The annotation is retained during every mutation, including on policies that
	// historically had no annotation. Content and scope provide the readiness signal.
	for _, tt := range []struct {
		name   string
		mutate func(*admissionv1.ValidatingAdmissionPolicy, *admissionv1.ValidatingAdmissionPolicyBinding)
	}{
		{"validation", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
			p.Spec.Validations[0].Expression = allowAllValidation
		}},
		{"identity", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
			p.Spec.Variables[1].Expression = "'different-controller'"
		}},
		{"policy operations", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
			p.Spec.MatchConstraints.ResourceRules[0].Operations = []admissionv1.OperationType{admissionv1.Update}
		}},
		{"match condition", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
			p.Spec.MatchConditions = []admissionv1.MatchCondition{{Name: "skip", Expression: "false"}}
		}},
		{"binding selector", func(_ *admissionv1.ValidatingAdmissionPolicy, b *admissionv1.ValidatingAdmissionPolicyBinding) {
			b.Spec.MatchResources.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"guard": "selected"}}
		}},
		{"binding operations", func(_ *admissionv1.ValidatingAdmissionPolicy, b *admissionv1.ValidatingAdmissionPolicyBinding) {
			b.Spec.MatchResources.ResourceRules[0].Operations = []admissionv1.OperationType{admissionv1.Update}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(policy), policy))
			require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(binding), binding))
			tt.mutate(policy, binding)
			require.NoError(t, kube.Update(ctx, policy))
			require.NoError(t, kube.Update(ctx, binding))
			check(false)
			awaitProbe(false)
			require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(policy), policy))
			require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(binding), binding))
			policy.Spec = *originalPolicy.DeepCopy()
			binding.Spec = *originalBinding.DeepCopy()
			require.NoError(t, kube.Update(ctx, policy))
			require.NoError(t, kube.Update(ctx, binding))
			check(true)
			awaitProbe(true)
		})
	}
}
