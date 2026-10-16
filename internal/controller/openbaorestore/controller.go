/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package openbaorestore

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
	appopenbaorestore "github.com/kubebao/openbao-operator/internal/app/openbaorestore"
	"github.com/kubebao/openbao-operator/internal/platform/admission"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	observability "github.com/kubebao/openbao-operator/internal/platform/observability"
)

// OpenBaoRestoreReconciler reconciles a OpenBaoRestore object.
type OpenBaoRestoreReconciler struct {
	client.Client
	AdmissionTracker  *admission.Tracker
	RestoreReconciler appopenbaorestore.RestoreReconciler
}

const controllerNameOpenBaoRestore = "openbaorestore"

// SECURITY: RBAC is provided via namespace-scoped tenant Roles, not cluster-wide.
// The controller uses direct API calls for Jobs (GET, not list/watch) to check status,
// similar to the backup controller pattern. No cluster-wide permissions are needed.

// Reconcile is part of the main Kubernetes reconciliation loop.
func (r *OpenBaoRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	start := time.Now()
	reconcileMetrics := observability.NewReconcileMetrics(req.Namespace, req.Name, controllerNameOpenBaoRestore)
	recordedError := false
	recordError := func(e error) {
		if e == nil {
			return
		}
		reason := "Error"
		if r, ok := operatorerrors.Reason(e); ok {
			reason = r
		}
		reconcileMetrics.IncrementError(reason)
		recordedError = true
	}
	defer func() {
		reconcileMetrics.ObserveDuration(time.Since(start).Seconds())
		if err != nil && !recordedError {
			recordError(err)
		}
	}()

	logger := log.FromContext(ctx).WithName("openbaorestore")

	if r.RestoreReconciler == nil {
		return ctrl.Result{}, fmt.Errorf("restore reconciler is not configured")
	}

	restoreResource := &openbaov1alpha1.OpenBaoRestore{}
	if getErr := r.Get(ctx, req.NamespacedName, restoreResource); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			reconcileMetrics.Clear()
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get OpenBaoRestore before admission dependency check: %w", getErr)
	}
	if result, blocked := r.pauseForAdmissionDependencyLoss(ctx, logger); blocked {
		if !appopenbaorestore.CanContinueWithoutAdmission(restoreResource) {
			return result, nil
		}
		logger.Info("Admission policy dependencies are unavailable; continuing restore observation or cleanup")
	}

	appResult, appErr := appopenbaorestore.ReconcileOpenBaoRestore(
		ctx,
		restoreResource,
		logger,
		r.RestoreReconciler,
	)
	result = ctrl.Result{RequeueAfter: appResult.RequeueAfter}
	err = appErr
	if err != nil {
		recordError(err)
		return result, err
	}
	if !restoreResource.DeletionTimestamp.IsZero() &&
		!controllerutil.ContainsFinalizer(restoreResource, openbaov1alpha1.OpenBaoRestoreFinalizer) {
		reconcileMetrics.Clear()
	}

	return result, nil
}

func (r *OpenBaoRestoreReconciler) pauseForAdmissionDependencyLoss(ctx context.Context, logger logr.Logger) (ctrl.Result, bool) {
	if admission.UnsafeAdmissionDisabled() {
		return ctrl.Result{}, false
	}

	status, err := admission.RefreshStatus(ctx, r.AdmissionTracker, r.Client)
	if err != nil {
		logger.Info("Admission policy dependency refresh failed; pausing restore reconciliation", "error", err)
		return ctrl.Result{RequeueAfter: constants.RequeueShort}, true
	}
	if status != nil && !status.OverallReady {
		logger.Info("Admission policy dependencies not ready; pausing restore reconciliation", "summary", status.SummaryMessage())
		return ctrl.Result{RequeueAfter: constants.RequeueShort}, true
	}

	return ctrl.Result{}, false
}

// SetupWithManager sets up the controller with the Manager.
// NOTE: This controller does NOT use Owns() or Watches() for Jobs because the operator
// uses namespace-scoped RBAC via tenant Roles. Owns/Watches would require cluster-wide
// list/watch permissions on Jobs, which we don't have. Instead, the restore manager
// uses direct API calls (GET) and RequeueAfter polling to monitor job status.
func (r *OpenBaoRestoreReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	if r.RestoreReconciler == nil {
		return fmt.Errorf("restore reconciler is not configured")
	}

	if err := setupRestoreStateMetrics(ctx, mgr); err != nil {
		return fmt.Errorf("set up restore state metrics: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&openbaov1alpha1.OpenBaoRestore{}).
		Named(controllerNameOpenBaoRestore).
		Complete(r)
}
