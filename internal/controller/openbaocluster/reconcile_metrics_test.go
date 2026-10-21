package openbaocluster

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/observability"
)

func TestClusterReconcileMetrics_OwnerAbsent(t *testing.T) {
	for _, controller := range []string{controllerNameWorkload, controllerNameAdminOps, controllerNameStatus} {
		t.Run(controller, func(t *testing.T) {
			key := client.ObjectKey{Namespace: "metrics", Name: t.Name()}
			c := fake.NewClientBuilder().WithScheme(newOpenBaoClusterTestScheme(t)).Build()
			parent := &OpenBaoClusterReconciler{Client: c, ControllerRuntime: ControllerRuntime{APIReader: c}}
			r := metricTestClusterReconciler(parent, controller)
			seedReconcileMetrics(t, key, controller)
			for range 2 {
				_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
				require.NoError(t, err)
				require.Empty(t, reconcileMetricCounts(t, key, controller), "deferred observations must stay suppressed")
			}
		})
	}
}

func TestClusterReconcileMetrics_DeletingOwner(t *testing.T) {
	for _, controller := range []string{controllerNameWorkload, controllerNameAdminOps} {
		for _, pending := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/pending=%t", controller, pending), func(t *testing.T) {
				key := client.ObjectKey{Namespace: "metrics", Name: t.Name()}
				cluster := deletingMetricsCluster(key, pending)
				c := fake.NewClientBuilder().WithScheme(newOpenBaoClusterTestScheme(t)).WithObjects(cluster).Build()
				parent := &OpenBaoClusterReconciler{Client: c, ControllerRuntime: ControllerRuntime{APIReader: c}}
				seedReconcileMetrics(t, key, controller)
				_, err := metricTestClusterReconciler(parent, controller).Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
				require.NoError(t, err)
				counts := reconcileMetricCounts(t, key, controller)
				if pending {
					require.Equal(t, 2.0, counts["duration"])
					require.Equal(t, 1.0, counts["FirstReason"])
					require.Equal(t, 1.0, counts["SecondReason"])
				} else {
					require.Empty(t, counts)
				}
			})
		}
	}
}

func TestClusterReconcileMetrics_StatusFinalization(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		alreadyFinalized, failPatch, childAbsent bool
	}{
		{name: "completed"},
		{name: "already finalized", alreadyFinalized: true},
		{name: "finalizer patch failed", failPatch: true},
		{name: "child list not found", childAbsent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := client.ObjectKey{Namespace: "metrics", Name: t.Name()}
			cluster := deletingMetricsCluster(key, !tc.alreadyFinalized)
			cluster.Spec.DeletionPolicy = openbaov1alpha1.DeletionPolicyDeletePVCs
			scheme := newOpenBaoClusterTestScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).
				WithInterceptorFuncs(interceptor.Funcs{
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						if tc.failPatch {
							return fmt.Errorf("finalizer patch failed")
						}
						return c.Patch(ctx, obj, patch, opts...)
					},
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if tc.childAbsent {
							return apierrors.NewNotFound(schema.GroupResource{Resource: "persistentvolumeclaims"}, "child")
						}
						return c.List(ctx, list, opts...)
					},
				}).Build()
			parent := &OpenBaoClusterReconciler{Client: c, Applications: newStatusTestApplications(c, scheme)}
			seedReconcileMetrics(t, key, controllerNameStatus)
			_, err := (&openBaoClusterStatusReconciler{parent: parent}).Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
			counts := reconcileMetricCounts(t, key, controllerNameStatus)
			if tc.failPatch || tc.childAbsent {
				require.Error(t, err)
				require.Equal(t, 2.0, counts["duration"])
				require.Equal(t, 1.0, counts["FirstReason"])
				require.Equal(t, 1.0, counts["SecondReason"])
			} else {
				require.NoError(t, err)
				require.Empty(t, counts)
			}
		})
	}
}

func deletingMetricsCluster(key client.ObjectKey, pending bool) *openbaov1alpha1.OpenBaoCluster {
	now := metav1.Now()
	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{
		Namespace: key.Namespace, Name: key.Name, UID: "metrics-cluster-uid", DeletionTimestamp: &now,
		Finalizers: []string{"test.example/retained"},
	}}
	if pending {
		cluster.Finalizers = append(cluster.Finalizers, openbaov1alpha1.OpenBaoClusterFinalizer)
	}
	return cluster
}

func metricTestClusterReconciler(parent *OpenBaoClusterReconciler, controller string) reconcile.Reconciler {
	switch controller {
	case controllerNameWorkload:
		return &openBaoClusterWorkloadReconciler{parent: parent}
	case controllerNameAdminOps:
		return &openBaoClusterAdminOpsReconciler{parent: parent}
	default:
		return &openBaoClusterStatusReconciler{parent: parent}
	}
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
