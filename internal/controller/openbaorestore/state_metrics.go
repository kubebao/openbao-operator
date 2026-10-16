package openbaorestore

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/observability"
)

const restoreClusterIndex = "spec.cluster"

// restoreStateMetricsReconciler refreshes one target cluster's observed restore
// state after a restore event. Polling a restore does not enqueue metric work.
type restoreStateMetricsReconciler struct {
	reader client.Reader
}

func (r *restoreStateMetricsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var requests openbaov1alpha1.OpenBaoRestoreList
	if err := r.reader.List(ctx, &requests,
		client.InNamespace(req.Namespace), client.MatchingFields{restoreClusterIndex: req.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list restores for target cluster metrics: %w", err)
	}
	observability.ObserveClusterRestoreState(req.Namespace, req.Name, requests.Items)
	return ctrl.Result{}, nil
}

func setupRestoreStateMetrics(ctx context.Context, mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx,
		&openbaov1alpha1.OpenBaoRestore{}, restoreClusterIndex, restoreClusterIndexValues,
	); err != nil {
		return fmt.Errorf("index restore target clusters for metrics: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		Watches(&openbaov1alpha1.OpenBaoRestore{}, handler.EnqueueRequestsFromMapFunc(restoreMetricsTarget),
			builder.WithPredicates(predicate.Funcs{UpdateFunc: restoreMetricsChanged})).
		Named("openbaorestore-metrics").
		Complete(&restoreStateMetricsReconciler{reader: mgr.GetCache()})
}

func restoreClusterIndexValues(object client.Object) []string {
	restore, ok := object.(*openbaov1alpha1.OpenBaoRestore)
	if !ok || restore == nil || restore.Spec.Cluster == "" {
		return nil
	}
	return []string{restore.Spec.Cluster}
}

func restoreMetricsTarget(_ context.Context, object client.Object) []ctrl.Request {
	clusters := restoreClusterIndexValues(object)
	if len(clusters) == 0 {
		return nil
	}
	// Update events enqueue both the old and new target, while delete events
	// retain the deleted object's target even when its request no longer exists.
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: object.GetNamespace(), Name: clusters[0]}}}
}

func restoreMetricsChanged(update event.UpdateEvent) bool {
	previous, previousOK := update.ObjectOld.(*openbaov1alpha1.OpenBaoRestore)
	current, currentOK := update.ObjectNew.(*openbaov1alpha1.OpenBaoRestore)
	if !previousOK || !currentOK || previous == nil || current == nil {
		return true
	}
	return observability.RestoreStateChanged(previous, current)
}
