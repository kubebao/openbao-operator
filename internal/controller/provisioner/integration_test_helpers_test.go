//go:build integration
// +build integration

package provisioner_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	appprovisioner "github.com/kubebao/openbao-operator/internal/app/provisioner"
	provisionercontroller "github.com/kubebao/openbao-operator/internal/controller/provisioner"
	"github.com/kubebao/openbao-operator/internal/platform/admission"
)

const operatorNamespace = "openbao-operator-system"

func setAdmissionReady(t *testing.T) {
	t.Helper()

	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "")
	admission.SetAdmissionDependenciesReady(true)
	t.Cleanup(func() {
		admission.SetAdmissionDependenciesReady(false)
	})
}

func startNamespaceProvisionerController(t *testing.T) client.Client {
	t.Helper()

	return startProvisionerManager(t, func(mgr ctrl.Manager) error {
		provisionerManager, err := appprovisioner.NewProvisioner(appprovisioner.ProvisionerDependencies{
			Client:    mgr.GetClient(),
			APIReader: mgr.GetAPIReader(),
			Logger:    logr.Discard(),
		})
		if err != nil {
			return err
		}

		reconciler := &provisionercontroller.NamespaceProvisionerReconciler{
			Client:            mgr.GetClient(),
			APIReader:         mgr.GetAPIReader(),
			Recorder:          mgr.GetEventRecorder("namespace-provisioner-test"),
			Provisioner:       provisionerManager,
			OperatorNamespace: operatorNamespace,
		}
		return reconciler.SetupWithManager(mgr)
	})
}

func startProvisionerControllers(t *testing.T) client.Client {
	t.Helper()

	return startProvisionerManager(t, func(mgr ctrl.Manager) error {
		provisionerManager, err := appprovisioner.NewProvisioner(appprovisioner.ProvisionerDependencies{
			Client:    mgr.GetClient(),
			APIReader: mgr.GetAPIReader(),
			Logger:    logr.Discard(),
		})
		if err != nil {
			return err
		}

		namespaceReconciler := &provisionercontroller.NamespaceProvisionerReconciler{
			Client:            mgr.GetClient(),
			APIReader:         mgr.GetAPIReader(),
			Recorder:          mgr.GetEventRecorder("namespace-provisioner-test"),
			Provisioner:       provisionerManager,
			OperatorNamespace: operatorNamespace,
		}
		if err := namespaceReconciler.SetupWithManager(mgr); err != nil {
			return err
		}

		tenantSecretsReconciler := &provisionercontroller.TenantSecretsRBACReconciler{
			Client:      mgr.GetClient(),
			APIReader:   mgr.GetAPIReader(),
			Recorder:    mgr.GetEventRecorder("tenant-secrets-rbac-test"),
			Provisioner: provisionerManager,
		}
		return tenantSecretsReconciler.SetupWithManager(mgr)
	})
}

func startProvisionerManager(t *testing.T, register func(ctrl.Manager) error) client.Client {
	t.Helper()

	scheme := newIntegrationScheme(t)
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
	}
	if assetsDir := getFirstFoundEnvTestBinaryDir(); assetsDir != "" {
		testEnv.BinaryAssetsDirectory = assetsDir
	}

	cfg, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, testEnv.Stop())
	})

	liveClient, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	mgrOptions := ctrl.Options{
		Scheme: scheme,
		Client: client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{
			&corev1.Namespace{}, &corev1.ResourceQuota{}, &corev1.LimitRange{}, &rbacv1.Role{}, &rbacv1.RoleBinding{},
		}}},
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	}
	skipNameValidation := true
	mgrOptions.Controller.SkipNameValidation = &skipNameValidation

	mgr, err := ctrl.NewManager(cfg, mgrOptions)
	require.NoError(t, err)
	require.NoError(t, register(mgr))

	managerCtx, cancel := context.WithCancel(context.Background())
	managerErr := make(chan error, 1)
	go func() {
		managerErr <- mgr.Start(managerCtx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-managerErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("manager stopped with error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("manager did not stop within timeout")
		}
	})

	return liveClient
}

func newIntegrationScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	return scheme
}

func getFirstFoundEnvTestBinaryDir() string {
	if assetsDir := os.Getenv("KUBEBUILDER_ASSETS"); assetsDir != "" {
		absoluteAssetsDir, err := filepath.Abs(assetsDir)
		if err != nil {
			return ""
		}
		return absoluteAssetsDir
	}

	basePath := filepath.Join("..", "..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			assetsDir, err := filepath.Abs(filepath.Join(basePath, entry.Name()))
			if err != nil {
				return ""
			}
			return assetsDir
		}
	}
	return ""
}

func createNamespace(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()

	require.NoError(t, c.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}))
}

func waitForTenantProvisioned(t *testing.T, ctx context.Context, c client.Client, key types.NamespacedName) *openbaov1alpha1.OpenBaoTenant {
	t.Helper()

	require.Eventually(t, func() bool {
		current := &openbaov1alpha1.OpenBaoTenant{}
		if err := c.Get(ctx, key, current); err != nil {
			return false
		}
		return current.Status.Provisioned && slices.Contains(current.Finalizers, openbaov1alpha1.OpenBaoTenantFinalizer)
	}, 20*time.Second, 200*time.Millisecond, "expected manager-driven reconcile to provision tenant %s", key)

	current := &openbaov1alpha1.OpenBaoTenant{}
	require.NoError(t, c.Get(ctx, key, current))
	return current
}

func waitForRole(t *testing.T, ctx context.Context, c client.Client, key types.NamespacedName) *rbacv1.Role {
	t.Helper()

	require.Eventually(t, func() bool {
		current := &rbacv1.Role{}
		return c.Get(ctx, key, current) == nil
	}, 20*time.Second, 200*time.Millisecond, "expected role %s/%s", key.Namespace, key.Name)

	current := &rbacv1.Role{}
	require.NoError(t, c.Get(ctx, key, current))
	return current
}

func waitForRoleBinding(t *testing.T, ctx context.Context, c client.Client, key types.NamespacedName) *rbacv1.RoleBinding {
	t.Helper()

	require.Eventually(t, func() bool {
		current := &rbacv1.RoleBinding{}
		return c.Get(ctx, key, current) == nil
	}, 20*time.Second, 200*time.Millisecond, "expected rolebinding %s/%s", key.Namespace, key.Name)

	current := &rbacv1.RoleBinding{}
	require.NoError(t, c.Get(ctx, key, current))
	return current
}

func waitForNotFound(t *testing.T, ctx context.Context, c client.Client, key types.NamespacedName, obj client.Object) {
	t.Helper()

	require.Eventually(t, func() bool {
		current := obj.DeepCopyObject().(client.Object)
		err := c.Get(ctx, key, current)
		return apierrors.IsNotFound(err)
	}, 20*time.Second, 200*time.Millisecond, "expected object %s/%s to be deleted", key.Namespace, key.Name)
}
