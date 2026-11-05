package openbaorestore

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestAPIMigrationHoldSuppressesEntireRestoreLifecycle(t *testing.T) {
	setAdmissionReady(t)
	for _, phase := range []api.RestorePhase{"", api.RestorePhasePending, api.RestorePhaseRunning, api.RestorePhaseCompleted} {
		for _, deleting := range []bool{false, true} {
			name := string(phase)
			if deleting {
				name += "/deleting"
			}
			t.Run(name, func(t *testing.T) {
				scheme := runtime.NewScheme()
				require.NoError(t, api.AddToScheme(scheme))
				restore := &api.OpenBaoRestore{
					ObjectMeta: metav1.ObjectMeta{Name: "queued", Namespace: "tenant", Finalizers: []string{api.OpenBaoRestoreFinalizer}},
					Spec:       api.OpenBaoRestoreSpec{Cluster: "target"}, Status: api.OpenBaoRestoreStatus{Phase: phase},
				}
				if deleting {
					now := metav1.Now()
					restore.DeletionTimestamp = &now
				}
				c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(restore).Build()
				application := &recordingRestoreReconciler{}
				r := &OpenBaoRestoreReconciler{Client: c, RestoreReconciler: application, APIMigrationHold: true}
				request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(restore)}
				before := &api.OpenBaoRestore{}
				require.NoError(t, c.Get(t.Context(), request.NamespacedName, before))
				for range 2 {
					result, err := r.Reconcile(t.Context(), request)
					require.NoError(t, err)
					require.Equal(t, ctrl.Result{}, result)
				}
				require.Zero(t, application.calls)
				after := &api.OpenBaoRestore{}
				require.NoError(t, c.Get(t.Context(), request.NamespacedName, after))
				require.Equal(t, before, after, "hold must not reserve, finalize, acknowledge, or clean up restores")
				r.APIMigrationHold = false
				_, err := r.Reconcile(t.Context(), request)
				require.NoError(t, err)
				require.Equal(t, 1, application.calls)
			})
		}
	}
}
