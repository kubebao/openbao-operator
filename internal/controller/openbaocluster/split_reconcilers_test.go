package openbaocluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	appopenbaocluster "github.com/kubebao/openbao-operator/internal/app/openbaocluster"
	"github.com/kubebao/openbao-operator/internal/platform/admission"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
)

type workloadSubReconcilerStub struct {
	delay time.Duration
	err   error
}

func (s workloadSubReconcilerStub) Reconcile(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	return recon.Result{RequeueAfter: s.delay}, s.err
}

func TestOpenBaoClusterAdminOpsReconcilerReconcile_UsesAPIReaderWhenAvailable(t *testing.T) {
	t.Parallel()

	scheme := newOpenBaoClusterTestScheme(t)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "adminops-api-reader",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Paused: true,
		},
	}

	cachedClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		Build()
	apiReader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(cluster.DeepCopy()).
		Build()

	reconciler := &openBaoClusterAdminOpsReconciler{
		parent: &OpenBaoClusterReconciler{
			Client: cachedClient,
			ControllerRuntime: ControllerRuntime{
				APIReader: apiReader,
			},
		},
	}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

func TestOpenBaoClusterWorkloadReconciler_SteadyStateRequeue(t *testing.T) {
	permanentErr := operatorerrors.WithReason(
		constants.ReasonImageVersionMismatch,
		operatorerrors.WrapPermanentConfig(errors.New("image version does not match spec.version")),
	)
	tests := []struct {
		name         string
		reconcilers  []appopenbaocluster.SubReconciler
		wantDelay    time.Duration
		enabled      bool
		singleTenant bool
		policyDelay  time.Duration
		wantReason   string
	}{
		{
			name:      "successful steady state requeues",
			wantDelay: constants.RequeueStandard,
		},
		{
			name:         "single tenant without policy repair requeues",
			singleTenant: true,
			wantDelay:    constants.RequeueStandard,
		},
		{
			name:        "handled permanent error does not requeue",
			reconcilers: []appopenbaocluster.SubReconciler{workloadSubReconcilerStub{err: permanentErr}},
			wantReason:  constants.ReasonImageVersionMismatch,
		},
		{name: "policy interval preserves multi-tenant poll", enabled: true, policyDelay: 5 * time.Minute, wantDelay: constants.RequeueStandard},
		{name: "policy interval preserves earlier workload retry", enabled: true, policyDelay: 5 * time.Minute,
			reconcilers: []appopenbaocluster.SubReconciler{workloadSubReconcilerStub{delay: time.Second}}, wantDelay: time.Second},
		{name: "single tenant uses policy interval", enabled: true, singleTenant: true, policyDelay: 5 * time.Minute, wantDelay: 5 * time.Minute},
		{name: "feature disabled preserves explicit retry", reconcilers: []appopenbaocluster.SubReconciler{workloadSubReconcilerStub{delay: 5 * time.Minute}}, wantDelay: 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newOpenBaoClusterTestScheme(t)
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "multi-tenant-workload",
					Namespace:       "default",
					ResourceVersion: "1",
				},
			}
			cluster.Spec.ReconcilePolicies = tt.enabled
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
				WithObjects(cluster.DeepCopy()).
				Build()
			parent := &OpenBaoClusterReconciler{
				Client:            fakeClient,
				ControllerRuntime: ControllerRuntime{SingleTenantMode: tt.singleTenant},
				Applications: appopenbaocluster.NewApplications(appopenbaocluster.ApplicationsConfig{
					Client:              fakeClient,
					WorkloadReconcilers: tt.reconcilers,
					PolicyReconciler:    workloadSubReconcilerStub{delay: tt.policyDelay},
					WorkloadPolicy:      appopenbaocluster.DefaultWorkloadResultPolicy(),
				}),
			}

			result, err := (&openBaoClusterWorkloadReconciler{parent: parent}).reconcileCluster(
				context.Background(),
				logr.Discard(),
				cluster,
				func(error) {},
			)
			if err != nil {
				t.Fatalf("reconcileCluster() error = %v", err)
			}
			if result.RequeueAfter != tt.wantDelay {
				t.Fatalf("reconcileCluster() requeueAfter = %s, want %s", result.RequeueAfter, tt.wantDelay)
			}
			if cluster.Status.Workload == nil {
				t.Fatal("reconcileCluster() did not initialize workload status")
			}
			if tt.wantReason == "" && cluster.Status.Workload.LastError != nil {
				t.Fatalf("reconcileCluster() lastError = %#v, want nil", cluster.Status.Workload.LastError)
			}
			if tt.wantReason != "" && (cluster.Status.Workload.LastError == nil || cluster.Status.Workload.LastError.Reason != tt.wantReason) {
				t.Fatalf("reconcileCluster() lastError = %#v, want reason %q", cluster.Status.Workload.LastError, tt.wantReason)
			}
		})
	}
}

func TestWorkloadAndAdminOpsWaitForClusterFinalizer(t *testing.T) {
	t.Parallel()

	scheme := newOpenBaoClusterTestScheme(t)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wait-for-finalizer",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(cluster).
		Build()
	admissionTracker := admission.NewTracker(nil, nil, nil, 0)
	admissionTracker.MarkReadyForUnsafeMode()
	parent := &OpenBaoClusterReconciler{
		Client: fakeClient,
		ControllerRuntime: ControllerRuntime{
			APIReader:        fakeClient,
			AdmissionTracker: admissionTracker,
			SingleTenantMode: true,
		},
	}

	tests := []struct {
		name       string
		reconciler admissionRuntimeTestReconciler
	}{
		{name: "workload", reconciler: &openBaoClusterWorkloadReconciler{parent: parent}},
		{name: "adminops", reconciler: &openBaoClusterAdminOpsReconciler{parent: parent}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace},
			})
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if result.RequeueAfter != constants.RequeueShort {
				t.Fatalf("Reconcile() requeueAfter = %s, want %s", result.RequeueAfter, constants.RequeueShort)
			}
		})
	}
}

func TestReconcilers_PauseWhenTenantNamespaceProvisioningIsIncomplete(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		newReconciler func(parent *OpenBaoClusterReconciler) admissionRuntimeTestReconciler
	}{
		{
			name: "workload",
			newReconciler: func(parent *OpenBaoClusterReconciler) admissionRuntimeTestReconciler {
				return &openBaoClusterWorkloadReconciler{parent: parent}
			},
		},
		{
			name: "adminops",
			newReconciler: func(parent *OpenBaoClusterReconciler) admissionRuntimeTestReconciler {
				return &openBaoClusterAdminOpsReconciler{parent: parent}
			},
		},
		{
			name: "status",
			newReconciler: func(parent *OpenBaoClusterReconciler) admissionRuntimeTestReconciler {
				return &openBaoClusterStatusReconciler{parent: parent}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cluster, parent := newTenantOnboardingTestContext(t, false)
			reconciler := tc.newReconciler(parent)

			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace},
			})
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if result.RequeueAfter != constants.RequeueShort {
				t.Fatalf("Reconcile() requeueAfter = %s, want %s", result.RequeueAfter, constants.RequeueShort)
			}
		})
	}
}

func TestOpenBaoClusterStatusReconciler_BypassesTenantOnboardingGateInSingleTenantMode(t *testing.T) {
	t.Parallel()

	cluster, parent := newTenantOnboardingTestContext(t, true)
	current := &openbaov1alpha1.OpenBaoCluster{}
	if err := parent.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, current); err != nil {
		t.Fatalf("Get() cluster: %v", err)
	}
	current.Spec.Paused = true
	if err := parent.Update(context.Background(), current); err != nil {
		t.Fatalf("Update() paused cluster: %v", err)
	}

	reconciler := &openBaoClusterStatusReconciler{parent: parent}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("Reconcile() requeueAfter = %s, want 0", result.RequeueAfter)
	}
}

func TestOpenBaoClusterStatusReconciler_BypassesTenantOnboardingGateWhenRoleBindingExists(t *testing.T) {
	t.Parallel()

	cluster, parent := newTenantOnboardingTestContext(t, false, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.TenantRoleBindingName,
			Namespace: "default",
		},
	})
	current := &openbaov1alpha1.OpenBaoCluster{}
	if err := parent.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, current); err != nil {
		t.Fatalf("Get() cluster: %v", err)
	}
	current.Spec.Paused = true
	if err := parent.Update(context.Background(), current); err != nil {
		t.Fatalf("Update() paused cluster: %v", err)
	}

	reconciler := &openBaoClusterStatusReconciler{parent: parent}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("Reconcile() requeueAfter = %s, want 0", result.RequeueAfter)
	}
}

func TestOpenBaoClusterStatusReconciler_FinalizerAddUsesMergePatch(t *testing.T) {
	t.Parallel()

	scheme := newOpenBaoClusterTestScheme(t)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "finalizer-patch",
			Namespace: "default",
		},
	}

	var patches int
	var updates int
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(cluster.DeepCopy()).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				updates++
				return errors.New("unexpected update for finalizer")
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if obj.GetName() == cluster.Name && obj.GetNamespace() == cluster.Namespace {
					patches++
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	parent := &OpenBaoClusterReconciler{
		Client: fakeClient,
		ControllerRuntime: ControllerRuntime{
			APIReader:        fakeClient,
			SingleTenantMode: true,
		},
		Applications: newStatusTestApplications(fakeClient, scheme),
	}
	reconciler := &openBaoClusterStatusReconciler{parent: parent}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("Reconcile() result = %v, want zero", result)
	}
	if updates != 0 {
		t.Fatalf("Update() calls = %d, want 0", updates)
	}
	if patches != 1 {
		t.Fatalf("Patch() calls = %d, want 1", patches)
	}

	updated := &openbaov1alpha1.OpenBaoCluster{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, updated); err != nil {
		t.Fatalf("Get() updated cluster: %v", err)
	}
	if !controllerutil.ContainsFinalizer(updated, openbaov1alpha1.OpenBaoClusterFinalizer) {
		t.Fatalf("expected finalizer to be present")
	}
}

func TestOpenBaoClusterStatusReconciler_FinalizerRemoveUsesMergePatch(t *testing.T) {
	t.Parallel()

	scheme := newOpenBaoClusterTestScheme(t)
	now := metav1.Now()
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "finalizer-remove-patch",
			Namespace:         "default",
			UID:               "finalizer-remove-uid",
			Finalizers:        []string{openbaov1alpha1.OpenBaoClusterFinalizer},
			DeletionTimestamp: &now,
		},
	}

	var patches int
	var updates int
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(cluster.DeepCopy()).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				updates++
				return errors.New("unexpected update for finalizer")
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if obj.GetName() == cluster.Name && obj.GetNamespace() == cluster.Namespace {
					patches++
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	parent := &OpenBaoClusterReconciler{
		Client: fakeClient,
		ControllerRuntime: ControllerRuntime{
			APIReader:        fakeClient,
			SingleTenantMode: true,
		},
		Applications: newStatusTestApplications(fakeClient, scheme),
	}
	reconciler := &openBaoClusterStatusReconciler{parent: parent}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("Reconcile() result = %v, want zero", result)
	}
	if updates != 0 {
		t.Fatalf("Update() calls = %d, want 0", updates)
	}
	if patches != 1 {
		t.Fatalf("Patch() calls = %d, want 1", patches)
	}
}

func TestOpenBaoClusterStatusReconciler_DeletionBypassesTenantOnboarding(t *testing.T) {
	t.Parallel()

	scheme := newOpenBaoClusterTestScheme(t)
	now := metav1.Now()
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "delete-without-onboarding",
			Namespace:         "tenant-a",
			UID:               "deleting-cluster-uid",
			Finalizers:        []string{openbaov1alpha1.OpenBaoClusterFinalizer},
			DeletionTimestamp: &now,
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(cluster.DeepCopy()).
		Build()

	parent := &OpenBaoClusterReconciler{
		Client: fakeClient,
		ControllerRuntime: ControllerRuntime{
			APIReader:        fakeClient,
			SingleTenantMode: false,
		},
		Applications: newStatusTestApplications(fakeClient, scheme),
	}
	reconciler := &openBaoClusterStatusReconciler{parent: parent}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("Reconcile() result = %v, want zero", result)
	}

	updated := &openbaov1alpha1.OpenBaoCluster{}
	err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), updated)
	if err == nil && controllerutil.ContainsFinalizer(updated, openbaov1alpha1.OpenBaoClusterFinalizer) {
		t.Fatal("cluster finalizer remains after deletion cleanup")
	}
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("Get() after deletion cleanup error = %v", err)
	}
}

func newTenantOnboardingTestContext(t *testing.T, singleTenant bool, objects ...client.Object) (*openbaov1alpha1.OpenBaoCluster, *OpenBaoClusterReconciler) {
	t.Helper()

	scheme := newOpenBaoClusterTestScheme(t)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-onboarding",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.5.0",
			Image:    "openbao/openbao:2.5.0",
			Replicas: 1,
			Profile:  openbaov1alpha1.ProfileHardened,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled: true,
			},
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
			},
		},
	}

	clientObjects := make([]client.Object, 0, 1+len(objects))
	clientObjects = append(clientObjects, cluster.DeepCopy())
	clientObjects = append(clientObjects, objects...)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(clientObjects...).
		Build()

	parent := &OpenBaoClusterReconciler{
		Client: fakeClient,
		ControllerRuntime: ControllerRuntime{
			APIReader:        fakeClient,
			SingleTenantMode: singleTenant,
		},
		Applications: newStatusTestApplications(fakeClient, scheme),
	}

	return cluster, parent
}
