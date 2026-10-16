package upgrade

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/testutil/robustness"
)

func TestEnsureUpgradeServiceAccountValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cluster   *openbaov1alpha1.OpenBaoCluster
		wantError string
	}{
		{
			name:      "cluster required",
			cluster:   nil,
			wantError: "cluster is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("AddToScheme() error: %v", err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("AddToScheme(core) error: %v", err)
			}

			k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			err := EnsureUpgradeServiceAccount(context.Background(), k8sClient, tt.cluster, "")
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantError)
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error=%q, want contains %q", err.Error(), tt.wantError)
			}
		})
	}
}

func TestEnsureUpgradeServiceAccountApplyFailure(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core) error: %v", err)
	}

	cluster := newUpgradeTestCluster()
	expected := errors.New("apply failed")
	injector := robustness.NewInjector(map[robustness.Operation]robustness.Rule{
		robustness.OpCreate: robustness.Always(expected),
	})

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(injector.InterceptorFuncs()).
		Build()

	err := EnsureUpgradeServiceAccount(context.Background(), k8sClient, cluster, "custom-owner")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "failed to ensure upgrade ServiceAccount default/demo-upgrade-serviceaccount") {
		t.Fatalf("error=%q, expected wrapped serviceaccount context", err.Error())
	}
	if !strings.Contains(err.Error(), expected.Error()) {
		t.Fatalf("error=%q, expected wrapped apply error %q", err.Error(), expected.Error())
	}
}

func TestEnsureUpgradeServiceAccount_TransientApplyFailureThenSuccess(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core) error: %v", err)
	}

	cluster := newUpgradeTestCluster()
	expected := errors.New("transient apply failed")
	injector := robustness.NewInjector(map[robustness.Operation]robustness.Rule{
		robustness.OpCreate: robustness.Once(expected),
	})

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(injector.InterceptorFuncs()).
		Build()

	firstErr := EnsureUpgradeServiceAccount(context.Background(), k8sClient, cluster, "owner")
	if firstErr == nil {
		t.Fatalf("first EnsureUpgradeServiceAccount() expected transient error")
	}
	if !strings.Contains(firstErr.Error(), expected.Error()) {
		t.Fatalf("first error=%q, expected wrapped apply error %q", firstErr.Error(), expected.Error())
	}

	if err := EnsureUpgradeServiceAccount(context.Background(), k8sClient, cluster, "owner"); err != nil {
		t.Fatalf("second EnsureUpgradeServiceAccount() unexpected error: %v", err)
	}
	if err := EnsureUpgradeServiceAccount(context.Background(), k8sClient, cluster, "owner"); err != nil {
		t.Fatalf("third EnsureUpgradeServiceAccount() unexpected idempotency error: %v", err)
	}

	sa := &corev1.ServiceAccount{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      cluster.Name + constants.SuffixUpgradeServiceAccount,
		Namespace: cluster.Namespace,
	}, sa); err != nil {
		t.Fatalf("Get(ServiceAccount) error: %v", err)
	}
}

func TestEnsureUpgradeServiceAccountSuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		fieldOwner     string
		wantFieldOwner string
	}{
		{
			name:           "defaults field owner",
			fieldOwner:     "",
			wantFieldOwner: constants.FieldOwnerOpenBaoOperator,
		},
		{
			name:           "uses provided field owner",
			fieldOwner:     "security-team",
			wantFieldOwner: "security-team",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("AddToScheme() error: %v", err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("AddToScheme(core) error: %v", err)
			}

			cluster := newUpgradeTestCluster()
			var capturedOptions client.CreateOptions

			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						capturedOptions = *(&client.CreateOptions{}).ApplyOptions(opts)
						return c.Create(ctx, obj, opts...)
					},
				}).
				Build()

			err := EnsureUpgradeServiceAccount(context.Background(), k8sClient, cluster, tt.fieldOwner)
			if err != nil {
				t.Fatalf("EnsureUpgradeServiceAccount() unexpected error: %v", err)
			}

			sa := &corev1.ServiceAccount{}
			if err := k8sClient.Get(context.Background(), types.NamespacedName{
				Name:      cluster.Name + constants.SuffixUpgradeServiceAccount,
				Namespace: cluster.Namespace,
			}, sa); err != nil {
				t.Fatalf("Get(ServiceAccount) error: %v", err)
			}

			if sa.Labels[constants.LabelAppName] != constants.LabelValueAppNameOpenBao {
				t.Fatalf("label %q=%q, want %q", constants.LabelAppName, sa.Labels[constants.LabelAppName], constants.LabelValueAppNameOpenBao)
			}
			if sa.Labels[constants.LabelAppInstance] != cluster.Name {
				t.Fatalf("label %q=%q, want %q", constants.LabelAppInstance, sa.Labels[constants.LabelAppInstance], cluster.Name)
			}
			if sa.Labels[constants.LabelOpenBaoCluster] != cluster.Name {
				t.Fatalf("label %q=%q, want %q", constants.LabelOpenBaoCluster, sa.Labels[constants.LabelOpenBaoCluster], cluster.Name)
			}
			if sa.Labels[constants.LabelOpenBaoComponent] != "upgrade" {
				t.Fatalf("label %q=%q, want %q", constants.LabelOpenBaoComponent, sa.Labels[constants.LabelOpenBaoComponent], "upgrade")
			}
			if sa.Labels[constants.LabelOpenBaoServiceAccountRole] != constants.ServiceAccountRoleUpgrade {
				t.Fatalf("label %q=%q, want %q", constants.LabelOpenBaoServiceAccountRole, sa.Labels[constants.LabelOpenBaoServiceAccountRole], constants.ServiceAccountRoleUpgrade)
			}
			if sa.Annotations[constants.AnnotationOpenBaoOwnerUID] != string(cluster.UID) {
				t.Fatalf("annotation %q=%q, want %q", constants.AnnotationOpenBaoOwnerUID, sa.Annotations[constants.AnnotationOpenBaoOwnerUID], cluster.UID)
			}

			if capturedOptions.FieldManager != tt.wantFieldOwner {
				t.Fatalf("FieldManager=%q, want %q", capturedOptions.FieldManager, tt.wantFieldOwner)
			}
		})
	}
}

func TestEnsureUpgradeServiceAccountRejectsUnownedExistingServiceAccount(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core) error: %v", err)
	}

	cluster := newUpgradeTestCluster()
	existing := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name + constants.SuffixUpgradeServiceAccount,
			Namespace: cluster.Namespace,
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existing).
		Build()

	err := EnsureUpgradeServiceAccount(context.Background(), k8sClient, cluster, "")
	if err == nil || !strings.Contains(err.Error(), "requires OpenBaoCluster owner proof") {
		t.Fatalf("EnsureUpgradeServiceAccount() error = %v, want owner proof error", err)
	}
}

func newUpgradeTestCluster() *openbaov1alpha1.OpenBaoCluster {
	name := "demo"
	namespace := "default"
	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(name + "-uid"),
		},
	}
}
