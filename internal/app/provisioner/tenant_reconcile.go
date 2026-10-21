package provisioner

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/admission"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/logging"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
)

const admissionDependencyRequeueAfter = 10 * time.Second

// TenantRuntime captures dependencies needed for OpenBaoTenant provisioning orchestration.
type TenantRuntime struct {
	Client                   client.Client
	APIReader                client.Reader
	AdmissionTracker         *admission.Tracker
	Recorder                 events.EventRecorder
	Provisioner              Provisioner
	OperatorNamespace        string
	ConditionTypeProvisioned string
	RequeueShort             time.Duration
	RequeueStandard          time.Duration
}

// TenantReconcileResult reports requeue timing and the observed tenant lifecycle.
type TenantReconcileResult struct {
	recon.Result
	OwnerAbsent           bool
	FinalizationCompleted bool
}

// ReconcileOpenBaoTenant runs the business flow for namespace provisioning.
func ReconcileOpenBaoTenant(ctx context.Context, key types.NamespacedName, logger logr.Logger, runtime TenantRuntime) (TenantReconcileResult, error) {
	if runtime.Client == nil {
		return TenantReconcileResult{}, fmt.Errorf("client is required")
	}
	if runtime.Provisioner == nil {
		return TenantReconcileResult{}, fmt.Errorf("provisioner manager is required")
	}

	tenant := &openbaov1alpha1.OpenBaoTenant{}
	if err := runtime.Client.Get(ctx, key, tenant); err != nil {
		if apierrors.IsNotFound(err) {
			return TenantReconcileResult{OwnerAbsent: true}, nil
		}
		return TenantReconcileResult{}, fmt.Errorf("failed to get OpenBaoTenant %s: %w", key, err)
	}

	targetNS := tenant.Spec.TargetNamespace
	logger = logger.WithValues("target_namespace", targetNS)

	isTrustedNamespace := tenant.Namespace == runtime.OperatorNamespace
	isSelfTargeting := tenant.Namespace == targetNS
	if !isTrustedNamespace && !isSelfTargeting {
		err := fmt.Errorf("security violation: OpenBaoTenant in namespace %q cannot target namespace %q",
			tenant.Namespace, targetNS)
		logger.Error(err, "Blocking provisioning attempt")
		logging.LogAuditEvent(logger, logging.EventTenantSecurityViolationBlocked, map[string]string{
			"tenant_namespace": tenant.Namespace,
			"tenant_name":      tenant.Name,
			"target_namespace": targetNS,
			"reason":           constants.ReasonSecurityViolation,
		})
		runtime.emitTenantWarningEvent(tenant, ReasonTenantProvisioningBlocked, fmt.Sprintf("Tenant provisioning blocked for namespace %s: %v", targetNS, err))

		original := tenant.DeepCopy()
		tenant.Status.Provisioned = false
		tenant.Status.LastError = err.Error()
		setTenantProvisionedCondition(
			runtime,
			tenant,
			metav1.ConditionFalse,
			constants.ReasonSecurityViolation,
			err.Error(),
		)
		if patchErr := patchStatus(ctx, runtime.Client, tenant, original); patchErr != nil {
			return TenantReconcileResult{}, fmt.Errorf("failed to patch status for security violation: %w", patchErr)
		}

		// Do not requeue. User must fix the CR.
		return TenantReconcileResult{}, nil
	}

	if !tenant.DeletionTimestamp.IsZero() {
		result, err := reconcileDeletion(ctx, logger, runtime, tenant, targetNS, key)
		return TenantReconcileResult{
			Result: result,
			FinalizationCompleted: err == nil &&
				!controllerutil.ContainsFinalizer(tenant, openbaov1alpha1.OpenBaoTenantFinalizer),
		}, err
	}

	if !controllerutil.ContainsFinalizer(tenant, openbaov1alpha1.OpenBaoTenantFinalizer) {
		original := tenant.DeepCopy()
		controllerutil.AddFinalizer(tenant, openbaov1alpha1.OpenBaoTenantFinalizer)
		if err := runtime.Client.Patch(ctx, tenant, client.MergeFrom(original)); err != nil {
			return TenantReconcileResult{}, fmt.Errorf("failed to add finalizer to OpenBaoTenant %s: %w", key, err)
		}
		// Requeue to observe the resource with the finalizer attached.
		return TenantReconcileResult{Result: recon.Result{RequeueAfter: resolveRequeueShort(runtime)}}, nil
	}

	ns := &corev1.Namespace{}
	if err := runtime.Client.Get(ctx, types.NamespacedName{Name: targetNS}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			original := tenant.DeepCopy()
			tenant.Status.Provisioned = false
			message := fmt.Sprintf("target namespace %s not found", targetNS)
			tenant.Status.LastError = message
			setTenantProvisionedCondition(runtime, tenant, metav1.ConditionFalse, ReasonTenantProvisioningBlocked, message)
			runtime.emitTenantWarningEvent(tenant, ReasonTenantProvisioningBlocked, fmt.Sprintf("Tenant provisioning blocked because target namespace %s was not found", targetNS))
			if patchErr := patchStatus(ctx, runtime.Client, tenant, original); patchErr != nil {
				return TenantReconcileResult{}, fmt.Errorf("failed to update OpenBaoTenant status: %w", patchErr)
			}
			logger.Info("Target namespace not found; will retry", "target_namespace", targetNS)
			return TenantReconcileResult{Result: recon.Result{RequeueAfter: resolveRequeueStandard(runtime)}}, nil
		}
		return TenantReconcileResult{}, fmt.Errorf("failed to get namespace %s: %w", targetNS, err)
	}

	ready, result := ensureAdmissionDependenciesReady(ctx, logger, runtime, tenant)
	if !ready {
		return TenantReconcileResult{Result: result}, nil
	}
	if !ns.DeletionTimestamp.IsZero() {
		done, err := runtime.Provisioner.FinalizeNamespace(ctx, targetNS, ns.UID)
		if err != nil {
			return TenantReconcileResult{}, fmt.Errorf("finalize tenant namespace %s: %w", targetNS, err)
		}
		original := tenant.DeepCopy()
		tenant.Status.Provisioned = false
		message := fmt.Sprintf("target namespace %s is terminating; waiting for OpenBao resource finalization", targetNS)
		if done {
			message = fmt.Sprintf("target namespace %s is terminating; tenant RBAC cleanup completed", targetNS)
		}
		tenant.Status.LastError = message
		setTenantProvisionedCondition(runtime, tenant, metav1.ConditionFalse, ReasonTenantProvisioningBlocked, message)
		if err := patchStatus(ctx, runtime.Client, tenant, original); err != nil {
			return TenantReconcileResult{}, fmt.Errorf("update tenant namespace finalization status: %w", err)
		}
		return TenantReconcileResult{Result: recon.Result{RequeueAfter: resolveRequeueShort(runtime)}}, nil
	}

	logger.Info("Provisioning tenant RBAC", "target_namespace", targetNS)
	if err := runtime.Provisioner.EnsureTenantRBAC(ctx, tenant); err != nil {
		runtime.emitTenantWarningEvent(tenant, ReasonTenantProvisioningFailed, fmt.Sprintf("Tenant provisioning failed for namespace %s: %v", targetNS, err))
		original := tenant.DeepCopy()
		tenant.Status.Provisioned = false
		tenant.Status.LastError = err.Error()
		setTenantProvisionedCondition(runtime, tenant, metav1.ConditionFalse, ReasonTenantProvisioningFailed, err.Error())
		if statusErr := patchStatus(ctx, runtime.Client, tenant, original); statusErr != nil {
			return TenantReconcileResult{}, fmt.Errorf("failed to update OpenBaoTenant status: %w (original error: %w)", statusErr, err)
		}
		return TenantReconcileResult{}, fmt.Errorf("failed to ensure tenant RBAC for namespace %s: %w", targetNS, err)
	}

	original := tenant.DeepCopy()
	tenant.Status.Provisioned = true
	tenant.Status.LastError = ""
	setTenantProvisionedCondition(
		runtime,
		tenant,
		metav1.ConditionTrue,
		ReasonTenantProvisioned,
		fmt.Sprintf("Tenant RBAC provisioned for namespace %s", targetNS),
	)
	if err := patchStatus(ctx, runtime.Client, tenant, original); err != nil {
		return TenantReconcileResult{}, fmt.Errorf("failed to update OpenBaoTenant status: %w", err)
	}

	logger.Info("Successfully provisioned tenant RBAC", "target_namespace", targetNS)
	logging.LogAuditEvent(logger, logging.EventTenantRBACProvisioned, map[string]string{
		"tenant_namespace": tenant.Namespace,
		"tenant_name":      tenant.Name,
		"target_namespace": targetNS,
	})
	runtime.emitTenantNormalEvent(tenant, ReasonTenantProvisioned, fmt.Sprintf("Provisioned tenant RBAC for namespace %s", targetNS))
	// Namespace access is get-only. Poll declared target namespaces so deletion
	// can release tenant RBAC without adding cluster-wide namespace list/watch.
	return TenantReconcileResult{Result: recon.Result{RequeueAfter: resolveRequeueStandard(runtime)}}, nil
}

func reconcileDeletion(
	ctx context.Context,
	logger logr.Logger,
	runtime TenantRuntime,
	tenant *openbaov1alpha1.OpenBaoTenant,
	targetNS string,
	key types.NamespacedName,
) (recon.Result, error) {
	if !controllerutil.ContainsFinalizer(tenant, openbaov1alpha1.OpenBaoTenantFinalizer) {
		return recon.Result{}, nil
	}

	logger.Info("OpenBaoTenant is being deleted", "target_namespace", targetNS)

	hasOtherClaim, err := hasOtherActiveTenantClaim(ctx, runtime, tenant, targetNS)
	if err != nil {
		return recon.Result{}, err
	}
	if hasOtherClaim {
		logger.Info(
			"Another active OpenBaoTenant targets this namespace; preserving shared tenant resources",
			"target_namespace", targetNS,
		)
		return removeTenantFinalizer(ctx, runtime.Client, tenant, key)
	}

	// Keep tenant RBAC while OpenBaoCluster finalizers may still need it.
	reader := runtime.APIReader
	if reader == nil {
		reader = runtime.Client
	}
	clusterList := &openbaov1alpha1.OpenBaoClusterList{}
	if err := reader.List(ctx, clusterList, client.InNamespace(targetNS)); err != nil {
		return recon.Result{}, fmt.Errorf("failed to list keys in namespace %s: %w", targetNS, err)
	}
	restoreList := &openbaov1alpha1.OpenBaoRestoreList{}
	if err := reader.List(ctx, restoreList, client.InNamespace(targetNS)); err != nil {
		return recon.Result{}, fmt.Errorf("failed to list restores in namespace %s: %w", targetNS, err)
	}
	if len(clusterList.Items) > 0 || len(restoreList.Items) > 0 {
		logger.Info("Waiting for OpenBao resources to be deleted before cleaning up RBAC",
			"target_namespace", targetNS,
			"cluster_count", len(clusterList.Items), "restore_count", len(restoreList.Items))
		return recon.Result{RequeueAfter: resolveRequeueShort(runtime)}, nil
	}

	logger.Info("No OpenBaoClusters found; cleaning up tenant resources", "target_namespace", targetNS)
	if err := runtime.Provisioner.CleanupTenantResources(ctx, targetNS); err != nil {
		return recon.Result{}, fmt.Errorf("failed to cleanup tenant resources for namespace %s: %w", targetNS, err)
	}
	logging.LogAuditEvent(logger, logging.EventTenantRBACCleaned, map[string]string{
		"tenant_namespace": tenant.Namespace,
		"tenant_name":      tenant.Name,
		"target_namespace": targetNS,
	})
	runtime.emitTenantNormalEvent(
		tenant,
		ReasonTenantRBACCleaned,
		fmt.Sprintf("Cleaned tenant RBAC, ResourceQuota, and LimitRange for namespace %s", targetNS),
	)

	return removeTenantFinalizer(ctx, runtime.Client, tenant, key)
}

func hasOtherActiveTenantClaim(
	ctx context.Context,
	runtime TenantRuntime,
	tenant *openbaov1alpha1.OpenBaoTenant,
	targetNS string,
) (bool, error) {
	reader := runtime.APIReader
	if reader == nil {
		reader = runtime.Client
	}

	claimNamespaces := []string{targetNS}
	if runtime.OperatorNamespace != "" && runtime.OperatorNamespace != targetNS {
		claimNamespaces = append(claimNamespaces, runtime.OperatorNamespace)
	}

	for _, claimNamespace := range claimNamespaces {
		tenantList := &openbaov1alpha1.OpenBaoTenantList{}
		if err := reader.List(ctx, tenantList, client.InNamespace(claimNamespace)); err != nil {
			return false, fmt.Errorf(
				"failed to list OpenBaoTenants in namespace %s while cleaning up namespace %s: %w",
				claimNamespace,
				targetNS,
				err,
			)
		}

		for i := range tenantList.Items {
			other := &tenantList.Items[i]
			if other.Namespace == tenant.Namespace && other.Name == tenant.Name {
				continue
			}
			if other.DeletionTimestamp.IsZero() && other.Spec.TargetNamespace == targetNS {
				return true, nil
			}
		}
	}

	return false, nil
}

func removeTenantFinalizer(
	ctx context.Context,
	c client.Client,
	tenant *openbaov1alpha1.OpenBaoTenant,
	key types.NamespacedName,
) (recon.Result, error) {
	original := tenant.DeepCopy()
	controllerutil.RemoveFinalizer(tenant, openbaov1alpha1.OpenBaoTenantFinalizer)
	if err := c.Patch(ctx, tenant, client.MergeFrom(original)); err != nil {
		return recon.Result{}, fmt.Errorf("failed to remove finalizer from OpenBaoTenant %s: %w", key, err)
	}

	return recon.Result{}, nil
}

func ensureAdmissionDependenciesReady(
	ctx context.Context,
	logger logr.Logger,
	runtime TenantRuntime,
	tenant *openbaov1alpha1.OpenBaoTenant,
) (bool, recon.Result) {
	// Fail-closed privileged actions when admission policies are not ready.
	if admission.UnsafeAdmissionDisabled() {
		return true, recon.Result{}
	}

	reader := runtime.APIReader
	if reader == nil {
		reader = runtime.Client
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	status, err := admission.RefreshStatus(
		checkCtx,
		runtime.AdmissionTracker,
		reader,
	)
	cancel()
	if err != nil {
		logger.Info("Admission policy dependencies not ready; delaying tenant provisioning", "error", err)
		runtime.emitTenantWarningEvent(tenant, ReasonTenantProvisioningBlocked, fmt.Sprintf("Tenant provisioning blocked until admission dependencies are ready: %v", err))
		return false, recon.Result{RequeueAfter: admissionDependencyRequeueAfter}
	}

	if status.OverallReady {
		return true, recon.Result{}
	}

	message := status.SummaryMessage()
	original := tenant.DeepCopy()
	tenant.Status.Provisioned = false
	tenant.Status.LastError = message
	setTenantProvisionedCondition(
		runtime,
		tenant,
		metav1.ConditionFalse,
		ReasonTenantProvisioningBlocked,
		message,
	)
	// Keep existing behavior: best-effort status patch while requeueing.
	_ = patchStatus(ctx, runtime.Client, tenant, original)

	logger.Info("Admission policy dependencies not ready; delaying tenant provisioning", "summary", message)
	runtime.emitTenantWarningEvent(tenant, ReasonTenantProvisioningBlocked, fmt.Sprintf("Tenant provisioning blocked until admission dependencies are ready: %s", message))
	return false, recon.Result{RequeueAfter: admissionDependencyRequeueAfter}
}

func patchStatus(ctx context.Context, c client.Client, tenant *openbaov1alpha1.OpenBaoTenant, original *openbaov1alpha1.OpenBaoTenant) error {
	return c.Status().Patch(ctx, tenant, client.MergeFrom(original))
}

func setTenantProvisionedCondition(
	runtime TenantRuntime,
	tenant *openbaov1alpha1.OpenBaoTenant,
	status metav1.ConditionStatus,
	reason string,
	message string,
) {
	meta.SetStatusCondition(&tenant.Status.Conditions, metav1.Condition{
		Type:               conditionTypeProvisioned(runtime),
		Status:             status,
		ObservedGeneration: tenant.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func conditionTypeProvisioned(runtime TenantRuntime) string {
	if runtime.ConditionTypeProvisioned != "" {
		return runtime.ConditionTypeProvisioned
	}
	return "Provisioned"
}

func resolveRequeueShort(runtime TenantRuntime) time.Duration {
	if runtime.RequeueShort > 0 {
		return runtime.RequeueShort
	}
	return constants.RequeueShort
}

func resolveRequeueStandard(runtime TenantRuntime) time.Duration {
	if runtime.RequeueStandard > 0 {
		return runtime.RequeueStandard
	}
	return constants.RequeueStandard
}
