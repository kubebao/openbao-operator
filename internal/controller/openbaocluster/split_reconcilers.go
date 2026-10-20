package openbaocluster

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	appopenbaocluster "github.com/kubebao/openbao-operator/internal/app/openbaocluster"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/observability"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
)

type openBaoClusterWorkloadReconciler struct {
	parent *OpenBaoClusterReconciler
}

type openBaoClusterAdminOpsReconciler struct {
	parent *OpenBaoClusterReconciler
}

type openBaoClusterStatusReconciler struct {
	parent *OpenBaoClusterReconciler
}

func reconcileErrorReason(err error) string {
	return appopenbaocluster.ReconcileErrorReason(err)
}

func (r *OpenBaoClusterReconciler) loggerFor(ctx context.Context, _ ctrl.Request, _ string) logr.Logger {
	// controller-runtime already injects controller/reconcile identifiers, including
	// namespace/name for the reconciled object.
	return log.FromContext(ctx)
}

func (r *openBaoClusterWorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	start := time.Now()
	reconcileMetrics := observability.NewReconcileMetrics(req.Namespace, req.Name, controllerNameWorkload)
	recordedError := false
	recordError := func(e error) {
		if e == nil {
			return
		}
		reconcileMetrics.IncrementError(reconcileErrorReason(e))
		recordedError = true
	}
	defer func() {
		reconcileMetrics.ObserveDuration(time.Since(start).Seconds())
		if err != nil && !recordedError {
			recordError(err)
		}
	}()

	logger := r.parent.loggerFor(ctx, req, controllerNameWorkload)
	logger.V(1).Info("Reconciling OpenBaoCluster workload")

	cluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := r.parent.Get(ctx, req.NamespacedName, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			reconcileMetrics.Clear()
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get OpenBaoCluster %s/%s: %w", req.Namespace, req.Name, err)
	}

	if shouldSkipWorkloadReconcile(cluster) {
		if !cluster.DeletionTimestamp.IsZero() &&
			!controllerutil.ContainsFinalizer(cluster, openbaov1alpha1.OpenBaoClusterFinalizer) {
			reconcileMetrics.Clear()
		}
		return ctrl.Result{}, nil
	}
	if result, err, blocked := r.parent.pauseForTenantOnboarding(ctx, logger, controllerNameWorkload, cluster.Namespace); blocked {
		return result, err
	}

	if result, blocked := r.parent.pauseForAdmissionDependencyLoss(ctx, logger, controllerNameWorkload); blocked {
		return result, nil
	}
	if !controllerutil.ContainsFinalizer(cluster, openbaov1alpha1.OpenBaoClusterFinalizer) {
		return ctrl.Result{RequeueAfter: constants.RequeueShort}, nil
	}

	result, err = r.reconcileCluster(ctx, logger, cluster, recordError)
	return result, err
}

func shouldSkipWorkloadReconcile(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster == nil ||
		!cluster.DeletionTimestamp.IsZero() ||
		cluster.Spec.Paused ||
		cluster.Spec.Profile == ""
}

func (r *openBaoClusterWorkloadReconciler) reconcileCluster(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	recordError func(error),
) (ctrl.Result, error) {
	original := cluster.DeepCopy()
	if r.parent.Applications == nil {
		return ctrl.Result{}, fmt.Errorf("OpenBaoCluster applications are not configured")
	}

	appResult, appErr := r.parent.Applications.ReconcileWorkload(
		ctx,
		logger,
		original,
		cluster,
		recordError,
	)
	if appErr == nil &&
		cluster.Status.Workload != nil &&
		cluster.Status.Workload.LastError == nil {
		// Pods are not watched in either mode. Refresh an idle workload so later
		// terminal status or finalizer changes can complete a pending deletion.
		refresh := steadyStateStatusRefreshRequeueAfter(time.Now())
		if appResult.RequeueAfter <= 0 {
			appResult.RequeueAfter = refresh
		} else if !r.parent.SingleTenantMode && portauth.PolicyReconciliationEnabled(cluster) {
			// Multi-tenant infrastructure relies on polling rather than child watches.
			// The policy verification cache must not lengthen that repair interval.
			appResult.RequeueAfter = min(appResult.RequeueAfter, refresh)
		}
	}
	return ctrl.Result{RequeueAfter: appResult.RequeueAfter}, appErr
}

func (r *openBaoClusterAdminOpsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	start := time.Now()
	reconcileMetrics := observability.NewReconcileMetrics(req.Namespace, req.Name, controllerNameAdminOps)
	recordedError := false
	recordError := func(e error) {
		if e == nil {
			return
		}
		reconcileMetrics.IncrementError(reconcileErrorReason(e))
		recordedError = true
	}
	defer func() {
		reconcileMetrics.ObserveDuration(time.Since(start).Seconds())
		if err != nil && !recordedError {
			recordError(err)
		}
	}()

	logger := r.parent.loggerFor(ctx, req, controllerNameAdminOps)
	logger.V(1).Info("Reconciling OpenBaoCluster admin operations")

	cluster := &openbaov1alpha1.OpenBaoCluster{}
	reader := r.parent.APIReader
	if reader == nil {
		reader = r.parent.Client
	}
	if err := reader.Get(ctx, req.NamespacedName, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			reconcileMetrics.Clear()
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get OpenBaoCluster %s/%s: %w", req.Namespace, req.Name, err)
	}

	if !cluster.DeletionTimestamp.IsZero() || cluster.Spec.Paused || cluster.Spec.Profile == "" {
		if !cluster.DeletionTimestamp.IsZero() &&
			!controllerutil.ContainsFinalizer(cluster, openbaov1alpha1.OpenBaoClusterFinalizer) {
			reconcileMetrics.Clear()
		}
		return ctrl.Result{}, nil
	}
	if result, err, blocked := r.parent.pauseForTenantOnboarding(ctx, logger, controllerNameAdminOps, cluster.Namespace); blocked {
		return result, err
	}

	if result, blocked := r.parent.pauseForAdmissionDependencyLoss(ctx, logger, controllerNameAdminOps); blocked {
		return result, nil
	}
	if !controllerutil.ContainsFinalizer(cluster, openbaov1alpha1.OpenBaoClusterFinalizer) {
		return ctrl.Result{RequeueAfter: constants.RequeueShort}, nil
	}

	original := cluster.DeepCopy()
	if r.parent.Applications == nil {
		return ctrl.Result{}, fmt.Errorf("OpenBaoCluster applications are not configured")
	}
	appResult, appErr := r.parent.Applications.ReconcileAdminOps(ctx, logger, original, cluster, recordError)
	return ctrl.Result{RequeueAfter: appResult.RequeueAfter}, appErr
}

func (r *openBaoClusterStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	start := time.Now()
	reconcileMetrics := observability.NewReconcileMetrics(req.Namespace, req.Name, controllerNameStatus)
	recordedError := false
	recordError := func(e error) {
		if e == nil {
			return
		}
		reconcileMetrics.IncrementError(reconcileErrorReason(e))
		recordedError = true
	}
	defer func() {
		reconcileMetrics.ObserveDuration(time.Since(start).Seconds())
		if err != nil && !recordedError {
			recordError(err)
		}
	}()

	logger := r.parent.loggerFor(ctx, req, controllerNameStatus)
	logger.V(1).Info("Reconciling OpenBaoCluster status")

	cluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := r.parent.Get(ctx, req.NamespacedName, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			reconcileMetrics.Clear()
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get OpenBaoCluster %s/%s: %w", req.Namespace, req.Name, err)
	}

	if !cluster.DeletionTimestamp.IsZero() {
		logger.Info("OpenBaoCluster is marked for deletion")
		if controllerutil.ContainsFinalizer(cluster, openbaov1alpha1.OpenBaoClusterFinalizer) {
			if r.parent.Applications == nil {
				return ctrl.Result{}, fmt.Errorf("OpenBaoCluster applications are not configured")
			}
			if err := r.parent.Applications.HandleDeletion(ctx, logger, cluster); err != nil {
				return ctrl.Result{}, err
			}
			original := cluster.DeepCopy()
			controllerutil.RemoveFinalizer(cluster, openbaov1alpha1.OpenBaoClusterFinalizer)
			if err := r.parent.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, err)
			}
		}
		reconcileMetrics.Clear()
		return ctrl.Result{}, nil
	}

	if result, err, blocked := r.parent.pauseForTenantOnboarding(ctx, logger, controllerNameStatus, cluster.Namespace); blocked {
		return result, err
	}

	if !controllerutil.ContainsFinalizer(cluster, openbaov1alpha1.OpenBaoClusterFinalizer) {
		original := cluster.DeepCopy()
		controllerutil.AddFinalizer(cluster, openbaov1alpha1.OpenBaoClusterFinalizer)
		if err := r.parent.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer to OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, err)
		}
		return ctrl.Result{}, nil
	}

	if cluster.Spec.Paused {
		if err := r.parent.updateStatusForPaused(ctx, logger, cluster); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if err := r.parent.emitSecurityWarningEvents(ctx, logger, cluster); err != nil {
		logger.Error(err, "Failed to emit security warning events")
	}

	if cluster.Spec.Profile == "" {
		if err := r.parent.updateStatusForProfileNotSet(ctx, logger, cluster); err != nil {
			return ctrl.Result{}, err
		}
		requeueAfter := safetyNetRequeueAfter(time.Now())
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	statusUpdateResult, err := r.parent.updateStatus(ctx, logger, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if statusUpdateResult.RequeueAfter > 0 {
		return statusUpdateResult, nil
	}

	if r.parent.Applications != nil && r.parent.Applications.InitializationConfigured() && !cluster.Status.Initialized {
		return ctrl.Result{RequeueAfter: constants.RequeueShort}, nil
	}

	// In steady state, keep status fresh on a normal cadence. In multi-tenant mode
	// we do not watch child resources directly, so relying only on the safety-net
	// requeue can leave health conditions stale for many minutes after runtime drift.
	requeueAfter := steadyStateStatusRefreshRequeueAfter(time.Now())
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func steadyStateStatusRefreshRequeueAfter(now time.Time) time.Duration {
	if constants.RequeueStandard > 0 {
		return constants.RequeueStandard
	}
	return safetyNetRequeueAfter(now)
}

func safetyNetRequeueAfter(now time.Time) time.Duration {
	jitterNanos := now.UnixNano() % int64(constants.RequeueSafetyNetJitter)
	return constants.RequeueSafetyNetBase + time.Duration(jitterNanos)
}
