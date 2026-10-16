package openbaorestore

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	appopenbaorestore "github.com/kubebao/openbao-operator/internal/app/openbaorestore"
	"github.com/kubebao/openbao-operator/internal/platform/admission"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
)

type recordingRestoreReconciler struct {
	calls    int
	result   recon.Result
	observed *openbaov1alpha1.OpenBaoRestore
}

func (r *recordingRestoreReconciler) Reconcile(
	_ context.Context,
	_ logr.Logger,
	restoreResource *openbaov1alpha1.OpenBaoRestore,
) (recon.Result, error) {
	r.calls++
	r.observed = restoreResource
	return r.result, nil
}

func setAdmissionReady(t *testing.T) {
	t.Helper()
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "")
	admission.SetAdmissionDependenciesReady(true)
	t.Cleanup(func() {
		admission.SetAdmissionDependenciesReady(false)
	})
}

func TestOpenBaoRestoreReconciler_Reconcile_NotFound(t *testing.T) {
	setAdmissionReady(t)

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &OpenBaoRestoreReconciler{
		Client:            c,
		RestoreReconciler: &recordingRestoreReconciler{},
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "missing",
			Namespace: "default",
		},
	}

	result, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestOpenBaoRestoreReconciler_AdmissionDependencyLoss(t *testing.T) {
	t.Run("deletion continues", func(t *testing.T) {
		t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "")
		admission.SetAdmissionDependenciesReady(false)
		t.Cleanup(func() { admission.SetAdmissionDependenciesReady(false) })

		scheme := runtime.NewScheme()
		require.NoError(t, batchv1.AddToScheme(scheme))
		require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
		now := metav1.Now()
		restore := &openbaov1alpha1.OpenBaoRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "deleting-restore",
				Namespace:         "default",
				DeletionTimestamp: &now,
				Finalizers:        []string{openbaov1alpha1.OpenBaoRestoreFinalizer},
			},
			Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "missing-cluster"},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(restore).Build()
		tracker := admission.NewTracker(c, admission.DefaultDependencies(), admission.DefaultNamePrefixes(), time.Hour)
		tracker.Set(admission.Status{CheckedAt: time.Now(), OverallReady: false})
		r := &OpenBaoRestoreReconciler{
			Client:            c,
			AdmissionTracker:  tracker,
			RestoreReconciler: appopenbaorestore.NewRestoreReconciler(appopenbaorestore.RestoreDependencies{Client: c, APIReader: c, Scheme: scheme}),
		}

		result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(restore)})
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
		current := &openbaov1alpha1.OpenBaoRestore{}
		err = c.Get(context.Background(), client.ObjectKeyFromObject(restore), current)
		if err == nil {
			assert.NotContains(t, current.Finalizers, openbaov1alpha1.OpenBaoRestoreFinalizer)
		} else {
			assert.True(t, apierrors.IsNotFound(err))
		}
	})

	t.Run("normal reconcile remains paused", func(t *testing.T) {
		t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "")
		admission.SetAdmissionDependenciesReady(false)
		t.Cleanup(func() { admission.SetAdmissionDependenciesReady(false) })

		scheme := runtime.NewScheme()
		require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
		restore := &openbaov1alpha1.OpenBaoRestore{
			ObjectMeta: metav1.ObjectMeta{Name: "active-restore", Namespace: "default"},
			Spec:       openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "test-cluster"},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(restore).Build()
		tracker := admission.NewTracker(c, admission.DefaultDependencies(), admission.DefaultNamePrefixes(), time.Hour)
		tracker.Set(admission.Status{CheckedAt: time.Now(), OverallReady: false})
		r := &OpenBaoRestoreReconciler{
			Client:            c,
			AdmissionTracker:  tracker,
			RestoreReconciler: appopenbaorestore.NewRestoreReconciler(appopenbaorestore.RestoreDependencies{Client: c, APIReader: c, Scheme: scheme}),
		}

		result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(restore)})
		require.NoError(t, err)
		assert.Equal(t, constants.RequeueShort, result.RequeueAfter)
		current := &openbaov1alpha1.OpenBaoRestore{}
		require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(restore), current))
		assert.Empty(t, current.Finalizers)
	})

	t.Run("committed execution continues observation", func(t *testing.T) {
		t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "")
		admission.SetAdmissionDependenciesReady(false)
		t.Cleanup(func() { admission.SetAdmissionDependenciesReady(false) })

		scheme := runtime.NewScheme()
		require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
		restore := &openbaov1alpha1.OpenBaoRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "committed-restore",
				Namespace: "default",
				UID:       types.UID("committed-restore-uid"),
			},
			Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "test-cluster"},
			Status: openbaov1alpha1.OpenBaoRestoreStatus{
				Phase: openbaov1alpha1.RestorePhaseRunning,
				Execution: &openbaov1alpha1.RestoreExecutionStatus{
					OperationID: "committed-restore-uid",
					Stage:       openbaov1alpha1.RestoreExecutionStageCreated,
					JobName:     "restore-committed-restore",
				},
			},
		}
		restoreGetCalls := 0
		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(restore).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(
					ctx context.Context,
					c client.WithWatch,
					key client.ObjectKey,
					obj client.Object,
					opts ...client.GetOption,
				) error {
					if _, ok := obj.(*openbaov1alpha1.OpenBaoRestore); ok {
						restoreGetCalls++
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}).
			Build()
		tracker := admission.NewTracker(c, admission.DefaultDependencies(), admission.DefaultNamePrefixes(), time.Hour)
		tracker.Set(admission.Status{CheckedAt: time.Now(), OverallReady: false})
		recorder := &recordingRestoreReconciler{result: recon.Result{RequeueAfter: 7 * time.Second}}
		r := &OpenBaoRestoreReconciler{
			Client:            c,
			AdmissionTracker:  tracker,
			RestoreReconciler: recorder,
		}

		result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(restore)})
		require.NoError(t, err)
		assert.Equal(t, 7*time.Second, result.RequeueAfter)
		assert.Equal(t, 1, recorder.calls)
		assert.Equal(t, 1, restoreGetCalls)
		require.NotNil(t, recorder.observed)
		assert.Equal(t, restore.Name, recorder.observed.Name)
		assert.Equal(t, restore.Status.Execution.OperationID, recorder.observed.Status.Execution.OperationID)
	})
}

func TestOpenBaoRestoreReconciler_DeletingRecoveryAdmission(t *testing.T) {
	for _, action := range []string{"Resume", "Abandon", ""} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "")
			admission.SetAdmissionDependenciesReady(false)
			t.Cleanup(func() { admission.SetAdmissionDependenciesReady(false) })

			scheme := runtime.NewScheme()
			require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
			now := metav1.Now()
			request := &openbaov1alpha1.OpenBaoRestore{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default", Name: "deleting", UID: "operation",
					DeletionTimestamp: &now,
					Finalizers:        []string{openbaov1alpha1.OpenBaoRestoreFinalizer},
					Annotations:       map[string]string{constants.AnnotationRestoreAcknowledge: "operation/" + action},
				},
				Status: openbaov1alpha1.OpenBaoRestoreStatus{
					Phase: openbaov1alpha1.RestorePhaseUnknown,
					Execution: &openbaov1alpha1.RestoreExecutionStatus{
						OperationID: "operation", Stage: openbaov1alpha1.RestoreExecutionStageUnknown,
					},
				},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(request).Build()
			tracker := admission.NewTracker(c, admission.DefaultDependencies(), admission.DefaultNamePrefixes(), time.Hour)
			tracker.Set(admission.Status{CheckedAt: time.Now(), OverallReady: false})
			recorder := &recordingRestoreReconciler{}
			r := &OpenBaoRestoreReconciler{Client: c, AdmissionTracker: tracker, RestoreReconciler: recorder}

			result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(request)})
			require.NoError(t, err)
			if action == "Resume" {
				assert.Zero(t, recorder.calls)
				assert.Equal(t, constants.RequeueShort, result.RequeueAfter)
			} else {
				assert.Equal(t, 1, recorder.calls)
			}
		})
	}
}

func TestOpenBaoRestoreReconciler_SetupWithManager_RequiresRestoreReconciler(t *testing.T) {
	err := (&OpenBaoRestoreReconciler{}).SetupWithManager(t.Context(), nil)

	require.EqualError(t, err, "restore reconciler is not configured")
}
