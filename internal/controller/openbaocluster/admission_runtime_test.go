package openbaocluster

import (
	"context"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/admission"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

type admissionRuntimeTestReconciler interface {
	Reconcile(context.Context, ctrl.Request) (ctrl.Result, error)
}

func TestReconcilers_PauseWhenAdmissionDependenciesNotReady(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		newRuntime func(parent *OpenBaoClusterReconciler) admissionRuntimeTestReconciler
	}{
		{
			name: "workload",
			newRuntime: func(parent *OpenBaoClusterReconciler) admissionRuntimeTestReconciler {
				return &openBaoClusterWorkloadReconciler{parent: parent}
			},
		},
		{
			name: "adminops",
			newRuntime: func(parent *OpenBaoClusterReconciler) admissionRuntimeTestReconciler {
				return &openBaoClusterAdminOpsReconciler{parent: parent}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cluster, tracker, parent := newAdmissionRuntimeTestContext(t)
			reconciler := tc.newRuntime(parent)

			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name},
			})
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if result.RequeueAfter != constants.RequeueShort {
				t.Fatalf("Reconcile() requeueAfter = %s, want %s", result.RequeueAfter, constants.RequeueShort)
			}

			status := tracker.Current()
			if status == nil || status.OverallReady {
				t.Fatalf("tracker status = %#v, want admission not ready", status)
			}
		})
	}
}

func TestReconcilers_RefreshAdmissionDependenciesEvenWhenCachedReady(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		newRuntime func(parent *OpenBaoClusterReconciler) admissionRuntimeTestReconciler
	}{
		{
			name: "workload",
			newRuntime: func(parent *OpenBaoClusterReconciler) admissionRuntimeTestReconciler {
				return &openBaoClusterWorkloadReconciler{parent: parent}
			},
		},
		{
			name: "adminops",
			newRuntime: func(parent *OpenBaoClusterReconciler) admissionRuntimeTestReconciler {
				return &openBaoClusterAdminOpsReconciler{parent: parent}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cluster, tracker, parent := newAdmissionRuntimeTestContext(t)
			tracker.Set(admission.Status{
				CheckedAt:    time.Now(),
				OverallReady: true,
			})
			reconciler := tc.newRuntime(parent)

			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name},
			})
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if result.RequeueAfter != constants.RequeueShort {
				t.Fatalf("Reconcile() requeueAfter = %s, want %s", result.RequeueAfter, constants.RequeueShort)
			}

			status := tracker.Current()
			if status == nil || status.OverallReady {
				t.Fatalf("tracker status = %#v, want refreshed admission not ready", status)
			}
		})
	}
}

func newAdmissionRuntimeTestContext(t *testing.T) (*openbaov1alpha1.OpenBaoCluster, *admission.Tracker, *OpenBaoClusterReconciler) {
	t.Helper()

	scheme := newOpenBaoClusterTestScheme(t)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
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

	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.TenantRoleBindingName,
			Namespace: cluster.Namespace,
		},
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: constants.TenantRoleName},
		Subjects: []rbacv1.Subject{{
			Kind: "ServiceAccount", Namespace: "openbao-operator-system", Name: "openbao-operator-controller",
		}},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, roleBinding).Build()
	tracker := admission.NewTracker(fakeClient, admission.DefaultDependencies(), admission.DefaultNamePrefixes(), time.Hour)
	parent := &OpenBaoClusterReconciler{
		Client: fakeClient,
		ControllerRuntime: ControllerRuntime{
			APIReader:        fakeClient,
			AdmissionTracker: tracker,
		},
	}

	return cluster, tracker, parent
}
