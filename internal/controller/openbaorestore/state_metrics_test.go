package openbaorestore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/observability"
)

func TestRestoreMetricsTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		object client.Object
		want   []ctrl.Request
	}{
		{
			name: "maps request to its target cluster",
			object: &api.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "request"},
				Spec: api.OpenBaoRestoreSpec{Cluster: "target"}},
			want: []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: "tenant", Name: "target"}}},
		},
		{name: "empty cluster", object: &api.OpenBaoRestore{}},
		{name: "different kind", object: &corev1.ConfigMap{}},
		{name: "nil restore", object: (*api.OpenBaoRestore)(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, restoreMetricsTarget(t.Context(), tc.object))
		})
	}
}

type restoreMetricsReader struct {
	client.Reader
	list func(context.Context, client.ObjectList, ...client.ListOption) error
}

func (r restoreMetricsReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return r.list(ctx, list, opts...)
}

func TestRestoreStateMetricsReconcile(t *testing.T) {
	listErr := errors.New("cache unavailable")
	for _, tc := range []struct {
		name      string
		absent    bool
		listErr   error
		wantState float64
	}{
		{name: "unresolved recovery precedes a newer success", wantState: 4},
		{name: "last request deleted", absent: true},
		{name: "read failure retains the previous state", listErr: listErr, wantState: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, api.AddToScheme(scheme))
			namespace := t.Name()
			older := &api.OpenBaoRestore{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "older", CreationTimestamp: metav1.NewTime(time.Unix(1, 0))},
				Spec:       api.OpenBaoRestoreSpec{Cluster: "target"}, Status: api.OpenBaoRestoreStatus{Phase: api.RestorePhaseUnknown},
			}
			newer := older.DeepCopy()
			newer.Name = "newer"
			newer.CreationTimestamp = metav1.NewTime(time.Unix(2, 0))
			newer.Status.Phase = api.RestorePhaseCompleted
			unrelated := older.DeepCopy()
			unrelated.Name, unrelated.Spec.Cluster = "unrelated", "other"
			unrelated.Status.Phase = api.RestorePhaseRunning
			otherNamespace := older.DeepCopy()
			otherNamespace.Namespace += "-other"
			builder := fake.NewClientBuilder().WithScheme(scheme).
				WithIndex(&api.OpenBaoRestore{}, restoreClusterIndex, restoreClusterIndexValues).
				WithObjects(unrelated, otherNamespace)
			if !tc.absent {
				builder.WithObjects(older, newer)
			}
			cached := builder.Build()
			observability.ObserveClusterRestoreState(namespace, "target", []api.OpenBaoRestore{*older})
			observability.ObserveClusterRestoreState(namespace, "other", []api.OpenBaoRestore{*unrelated})
			t.Cleanup(func() {
				observability.ObserveClusterRestoreState(namespace, "target", nil)
				observability.ObserveClusterRestoreState(namespace, "other", nil)
			})
			lists := 0
			r := restoreStateMetricsReconciler{reader: restoreMetricsReader{Reader: cached, list: func(
				ctx context.Context, list client.ObjectList, opts ...client.ListOption,
			) error {
				lists++
				options := &client.ListOptions{}
				options.ApplyOptions(opts)
				require.Equal(t, namespace, options.Namespace)
				require.NotNil(t, options.FieldSelector)
				require.Equal(t, "spec.cluster=target", options.FieldSelector.String())
				if tc.listErr != nil {
					return tc.listErr
				}
				err := cached.List(ctx, list, opts...)
				if !tc.absent {
					require.Len(t, list.(*api.OpenBaoRestoreList).Items, 2, "unrelated restores must not be copied")
				}
				return err
			}}}
			_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: "target"}})
			require.ErrorIs(t, err, tc.listErr)
			require.Equal(t, 1, lists)
			state, exists := restoreGaugeState(t, namespace, "target")
			require.Equal(t, !tc.absent, exists)
			require.Equal(t, tc.wantState, state)
			other, exists := restoreGaugeState(t, namespace, "other")
			require.True(t, exists)
			require.Equal(t, 1.0, other)
		})
	}
}

func restoreGaugeState(t *testing.T, namespace, cluster string) (float64, bool) {
	t.Helper()
	families, err := controllermetrics.Registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "openbao_restore_state" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string)
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["namespace"] == namespace && labels["name"] == cluster {
				return metric.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func TestRestoreMetricsChanged(t *testing.T) {
	previous := &api.OpenBaoRestore{Spec: api.OpenBaoRestoreSpec{Cluster: "target"}}
	current := previous.DeepCopy()
	current.ResourceVersion = "2"
	require.False(t, restoreMetricsChanged(event.UpdateEvent{ObjectOld: previous, ObjectNew: current}))
	current.Status.Phase = api.RestorePhaseCompleted
	require.True(t, restoreMetricsChanged(event.UpdateEvent{ObjectOld: previous, ObjectNew: current}))
	require.True(t, restoreMetricsChanged(event.UpdateEvent{}))
}
