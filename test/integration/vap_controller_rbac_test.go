//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func TestVAP_ControllerRBAC_DeniesNullListsWithoutEvaluationErrors(t *testing.T) {
	const prefix = "nullable-lists-"
	for _, object := range []client.Object{
		&admissionv1.ValidatingAdmissionPolicy{},
		&admissionv1.ValidatingAdmissionPolicyBinding{},
	} {
		name := "openbao-restrict-controller-rbac"
		if _, binding := object.(*admissionv1.ValidatingAdmissionPolicyBinding); binding {
			name += "-binding"
		}
		data, err := os.ReadFile(filepath.Join("..", "..", "config", "policy", name+".yaml"))
		require.NoError(t, err)
		require.NoError(t, yaml.Unmarshal(data, object))
		object.SetName(prefix + object.GetName())
		if binding, ok := object.(*admissionv1.ValidatingAdmissionPolicyBinding); ok {
			binding.Spec.PolicyName = prefix + binding.Spec.PolicyName
		}
		require.NoError(t, k8sClient.Create(ctx, object))
		t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), object) })
	}

	namespace := newTestNamespace(t)
	// Bypass RBAC authorization only, so the controller-specific policy decides each dry run.
	controller := newImpersonatedClientWithGroups(t, controllerUsername, "system:masters")
	for _, empty := range []bool{false, true} {
		name := "nil"
		var rules []rbacv1.PolicyRule
		var subjects []rbacv1.Subject
		if empty {
			name, rules, subjects = "empty", []rbacv1.PolicyRule{}, []rbacv1.Subject{}
		}
		t.Run(name+" rules", func(t *testing.T) {
			role := &rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{Name: "unexpected-role", Namespace: namespace},
				Rules:      rules,
			}
			require.Eventually(t, func() bool {
				err := controller.Create(ctx, role.DeepCopy(), client.DryRunAll)
				return err != nil
			}, 10*time.Second, 100*time.Millisecond, "wait for the policy to deny the dry run")
			err := controller.Create(ctx, role.DeepCopy(), client.DryRunAll)
			requireAdmissionDenied(t, err)
			require.Contains(t, err.Error(), "controller can only create/update Roles")
			require.NotContains(t, err.Error(), "resulted in error")
		})
		t.Run(name+" subjects", func(t *testing.T) {
			binding := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "unexpected-rolebinding", Namespace: namespace},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "unused-role"},
				Subjects:   subjects,
			}
			err := controller.Create(ctx, binding, client.DryRunAll)
			requireAdmissionDenied(t, err)
			require.Contains(t, err.Error(), "controller can only create/update RoleBindings")
			require.NotContains(t, err.Error(), "resulted in error")
		})
	}
}
