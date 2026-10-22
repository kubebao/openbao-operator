package openbaocluster

import (
	"context"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func (r *OpenBaoClusterReconciler) pauseForTenantOnboarding(ctx context.Context, logger logr.Logger, controllerName, namespace string) (ctrl.Result, error, bool) {
	if r.SingleTenantMode {
		return ctrl.Result{}, nil, false
	}

	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}

	roleBinding := &rbacv1.RoleBinding{}
	err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: constants.TenantRoleBindingName}, roleBinding)
	if err == nil {
		if tenantRoleBindingReady(roleBinding) {
			return ctrl.Result{}, nil, false
		}
		logger.V(1).Info("Tenant onboarding RoleBinding does not match the controller identity and tenant Role; pausing reconciliation", "controller", controllerName, "roleBinding", constants.TenantRoleBindingName)
		return ctrl.Result{RequeueAfter: constants.RequeueShort}, nil, true
	}
	if apierrors.IsNotFound(err) {
		logger.V(1).Info("Tenant onboarding not finished; pausing reconciliation", "controller", controllerName, "roleBinding", constants.TenantRoleBindingName)
		return ctrl.Result{RequeueAfter: constants.RequeueShort}, nil, true
	}

	return ctrl.Result{}, fmt.Errorf("failed to verify tenant onboarding for namespace %s: %w", namespace, err), true
}

func tenantRoleBindingReady(binding *rbacv1.RoleBinding) bool {
	if !binding.DeletionTimestamp.IsZero() || binding.RoleRef != (rbacv1.RoleRef{
		APIGroup: rbacv1.GroupName, Kind: "Role", Name: constants.TenantRoleName,
	}) || len(binding.Subjects) != 1 {
		return false
	}

	// Resolve the trusted installation identity with the same defaults as the
	// Provisioner. Do not derive the expected subject from the handoff object.
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		namespace = os.Getenv("OPERATOR_NAMESPACE")
	}
	if namespace == "" {
		namespace = "openbao-operator-system"
	}
	name := os.Getenv("OPERATOR_SERVICE_ACCOUNT_NAME")
	if name == "" {
		name = "openbao-operator-controller"
	}
	return binding.Subjects[0] == (rbacv1.Subject{
		Kind: "ServiceAccount", Namespace: namespace, Name: name,
	})
}
