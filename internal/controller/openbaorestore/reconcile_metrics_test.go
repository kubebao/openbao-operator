package openbaorestore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/observability"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
)

func TestRestoreReconcileMetrics_Lifecycle(t *testing.T) {
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "true")
	for _, tc := range []struct {
		name                                     string
		absent, deleting, removeFinalizer, clear bool
		result                                   recon.Result
		err                                      error
	}{
		{name: "owner absent", absent: true, clear: true},
		{name: "completed finalization", deleting: true, removeFinalizer: true, clear: true},
		{name: "committed execution draining", deleting: true, result: recon.Result{RequeueAfter: time.Second}},
		{name: "finalizer patch failed", deleting: true, removeFinalizer: true, err: fmt.Errorf("patch failed")},
		{name: "child Job not found", deleting: true, err: apierrors.NewNotFound(schema.GroupResource{Resource: "jobs"}, "child")},
		{name: "target cluster not found", err: apierrors.NewNotFound(schema.GroupResource{Resource: "openbaoclusters"}, "target")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := client.ObjectKey{Namespace: "metrics", Name: t.Name()}
			scheme := runtime.NewScheme()
			require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
			restoreLists := 0
			builder := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, cached client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*openbaov1alpha1.OpenBaoRestoreList); ok {
						restoreLists++
					}
					return cached.List(ctx, list, opts...)
				},
			})
			if !tc.absent {
				restore := &openbaov1alpha1.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{
					Namespace: key.Namespace, Name: key.Name,
					Finalizers: []string{openbaov1alpha1.OpenBaoRestoreFinalizer},
				}, Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "target"}}
				if tc.deleting {
					now := metav1.Now()
					restore.DeletionTimestamp = &now
				}
				builder.WithObjects(restore)
			}
			r := &OpenBaoRestoreReconciler{
				Client:            builder.Build(),
				RestoreReconciler: metricRestoreReconciler{removeFinalizer: tc.removeFinalizer, result: tc.result, err: tc.err},
			}
			seedReconcileMetrics(t, key, controllerNameOpenBaoRestore)
			result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
			require.ErrorIs(t, err, tc.err)
			require.Equal(t, tc.result.RequeueAfter, result.RequeueAfter)
			require.Zero(t, restoreLists, "restore polling must not list requests for metric updates")
			counts := reconcileMetricCounts(t, key, controllerNameOpenBaoRestore)
			if tc.clear {
				require.Empty(t, counts)
			} else {
				require.Equal(t, 2.0, counts["duration"])
				require.Equal(t, 1.0, counts["FirstReason"])
				require.Equal(t, 1.0, counts["SecondReason"])
			}
		})
	}
}

type metricRestoreReconciler struct {
	removeFinalizer bool
	result          recon.Result
	err             error
}

func (r metricRestoreReconciler) Reconcile(_ context.Context, _ logr.Logger, restore *openbaov1alpha1.OpenBaoRestore) (recon.Result, error) {
	if r.removeFinalizer {
		controllerutil.RemoveFinalizer(restore, openbaov1alpha1.OpenBaoRestoreFinalizer)
	}
	return r.result, r.err
}

func reconcileMetricCounts(t *testing.T, key client.ObjectKey, controller string) map[string]float64 {
	t.Helper()
	families, err := controllermetrics.Registry.Gather()
	require.NoError(t, err)
	counts := make(map[string]float64)
	for _, family := range families {
		if family.GetName() != "openbao_reconcile_duration_seconds" && family.GetName() != "openbao_reconcile_errors_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string)
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["namespace"] != key.Namespace || labels["name"] != key.Name || labels["controller"] != controller {
				continue
			}
			if family.GetName() == "openbao_reconcile_duration_seconds" {
				counts["duration"] = float64(metric.GetHistogram().GetSampleCount())
			} else {
				counts[labels["reason"]] = metric.GetCounter().GetValue()
			}
		}
	}
	return counts
}

func seedReconcileMetrics(t *testing.T, key client.ObjectKey, controller string) {
	t.Helper()
	m := observability.NewReconcileMetrics(key.Namespace, key.Name, controller)
	t.Cleanup(m.Clear)
	m.ObserveDuration(1)
	m.IncrementError("FirstReason")
	m.IncrementError("SecondReason")
}
