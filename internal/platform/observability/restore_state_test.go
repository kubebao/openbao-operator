package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestObserveClusterRestoreState(t *testing.T) {
	namespace := t.Name()
	request := api.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Name: "request", Namespace: namespace}, Spec: api.OpenBaoRestoreSpec{Cluster: "target"}}
	for _, tc := range []struct {
		name   string
		status api.OpenBaoRestoreStatus
		want   float64
	}{
		{"running", api.OpenBaoRestoreStatus{Phase: api.RestorePhaseRunning}, 1},
		{"unknown", api.OpenBaoRestoreStatus{Phase: api.RestorePhaseUnknown}, 4},
		{"restarting", api.OpenBaoRestoreStatus{Phase: api.RestorePhaseUnknown, Restart: &api.RestoreRestartStatus{}}, 5},
		{"resumed", api.OpenBaoRestoreStatus{Phase: api.RestorePhaseUnknown, AdministratorDisposition: api.RestoreAdministratorResume}, 6},
		{"abandoned", api.OpenBaoRestoreStatus{Phase: api.RestorePhaseUnknown, AdministratorDisposition: api.RestoreAdministratorAbandon}, 7},
		{"completed", api.OpenBaoRestoreStatus{Phase: api.RestorePhaseCompleted, AdministratorDisposition: api.RestoreAdministratorResume}, 2},
		{"failed", api.OpenBaoRestoreStatus{Phase: api.RestorePhaseFailed}, 3},
		{"blocked cleanup", api.OpenBaoRestoreStatus{Phase: api.RestorePhaseFailed, Target: &api.RestoreTargetStatus{Cleanup: api.RestoreTargetCleanupFailed}}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request.Status = tc.status
			// No transition event is needed, including after process restart.
			restoreStateGauge.DeleteLabelValues(namespace, "target")
			ObserveClusterRestoreState(namespace, "target", []api.OpenBaoRestore{request})
			require.Equal(t, tc.want, testutil.ToFloat64(restoreStateGauge.WithLabelValues(namespace, "target")))
		})
	}
	ObserveClusterRestoreState(namespace, "target", nil)
	require.False(t, restoreStateGauge.DeleteLabelValues(namespace, "target"), "deletion must remove stale series")
}

func TestObserveClusterRestoreStatePrefersUnreleasedRecovery(t *testing.T) {
	namespace := t.Name()
	older := api.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Name: "older", Namespace: namespace, CreationTimestamp: metav1.NewTime(time.Unix(1, 0))},
		Spec: api.OpenBaoRestoreSpec{Cluster: "target"}, Status: api.OpenBaoRestoreStatus{Phase: api.RestorePhaseUnknown}}
	newer := older.DeepCopy()
	newer.Name, newer.CreationTimestamp = "newer", metav1.NewTime(time.Unix(2, 0))
	newer.Status.Phase = api.RestorePhaseFailed
	for _, requests := range [][]api.OpenBaoRestore{{older, *newer}, {*newer, older}} {
		ObserveClusterRestoreState(namespace, "target", requests)
		require.Equal(t, float64(4), testutil.ToFloat64(restoreStateGauge.WithLabelValues(namespace, "target")))
	}
	older.Status.AdministratorDisposition = api.RestoreAdministratorResume
	ObserveClusterRestoreState(namespace, "target", []api.OpenBaoRestore{*newer, older})
	require.Equal(t, float64(3), testutil.ToFloat64(restoreStateGauge.WithLabelValues(namespace, "target")))
	ObserveClusterRestoreState(namespace, "target", nil)
}

func TestObserveClusterRestoreStateIsolation(t *testing.T) {
	namespace := t.Name()
	request := api.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "request"},
		Spec:       api.OpenBaoRestoreSpec{Cluster: "target"}, Status: api.OpenBaoRestoreStatus{Phase: api.RestorePhaseRunning},
	}
	other := *request.DeepCopy()
	other.Spec.Cluster = "other"
	otherNamespace := *request.DeepCopy()
	otherNamespace.Namespace += "-other"
	for _, item := range []api.OpenBaoRestore{request, other, otherNamespace} {
		ObserveClusterRestoreState(item.Namespace, item.Spec.Cluster, []api.OpenBaoRestore{item})
		t.Cleanup(func() { restoreStateGauge.DeleteLabelValues(item.Namespace, item.Spec.Cluster) })
	}
	existing := restoreStateGauge.WithLabelValues(namespace, "target")
	request.Status.Phase = api.RestorePhaseCompleted
	ObserveClusterRestoreState(namespace, "target", []api.OpenBaoRestore{other, otherNamespace, request})
	require.Equal(t, 2.0, testutil.ToFloat64(existing), "update the existing series without deleting its handle")
	ObserveClusterRestoreState(namespace, "target", nil)
	require.False(t, restoreStateGauge.DeleteLabelValues(namespace, "target"))
	require.Equal(t, 1.0, testutil.ToFloat64(restoreStateGauge.WithLabelValues(namespace, "other")))
	require.Equal(t, 1.0, testutil.ToFloat64(restoreStateGauge.WithLabelValues(otherNamespace.Namespace, "target")))
}

func TestObserveClusterRestoreStateNewestSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		newerTime int64
	}{
		{name: "creation timestamp", newerTime: 2},
		{name: "name breaks equal timestamp tie", newerTime: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			namespace := t.Name()
			a := api.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "a", CreationTimestamp: metav1.NewTime(time.Unix(1, 0))},
				Spec: api.OpenBaoRestoreSpec{Cluster: "target"}, Status: api.OpenBaoRestoreStatus{Phase: api.RestorePhaseFailed}}
			b := *a.DeepCopy()
			b.Name, b.CreationTimestamp = "b", metav1.NewTime(time.Unix(tc.newerTime, 0))
			b.Status.Phase = api.RestorePhaseCompleted
			t.Cleanup(func() { restoreStateGauge.DeleteLabelValues(namespace, "target") })
			for _, requests := range [][]api.OpenBaoRestore{{a, b}, {b, a}} {
				ObserveClusterRestoreState(namespace, "target", requests)
				require.Equal(t, 2.0, testutil.ToFloat64(restoreStateGauge.WithLabelValues(namespace, "target")))
			}
		})
	}
}

func TestRestoreStateChanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*api.OpenBaoRestore)
		want   bool
	}{
		{name: "unchanged", change: func(*api.OpenBaoRestore) {}},
		{name: "metadata bookkeeping", change: func(r *api.OpenBaoRestore) {
			r.ResourceVersion = "2"
			r.Annotations = map[string]string{"note": "updated"}
		}},
		{name: "status bookkeeping", change: func(r *api.OpenBaoRestore) {
			r.Status.StartTime = &metav1.Time{Time: time.Unix(10, 0)}
		}},
		{name: "target changes", change: func(r *api.OpenBaoRestore) { r.Spec.Cluster = "other" }, want: true},
		{name: "creation order changes", change: func(r *api.OpenBaoRestore) {
			r.CreationTimestamp = metav1.NewTime(time.Unix(2, 0))
		}, want: true},
		{name: "published state changes", change: func(r *api.OpenBaoRestore) { r.Status.Restart = nil }, want: true},
		{name: "priority changes with the same state value", change: func(r *api.OpenBaoRestore) {
			r.Status.Phase = api.RestorePhaseRunning
		}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := &api.OpenBaoRestore{
				ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "request", CreationTimestamp: metav1.NewTime(time.Unix(1, 0))},
				Spec:       api.OpenBaoRestoreSpec{Cluster: "target"},
				Status:     api.OpenBaoRestoreStatus{Phase: api.RestorePhaseUnknown, Restart: &api.RestoreRestartStatus{}},
			}
			current := previous.DeepCopy()
			tc.change(current)
			require.Equal(t, tc.want, RestoreStateChanged(previous, current))
		})
	}
}
