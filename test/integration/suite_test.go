//go:build integration
// +build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

var (
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sScheme *runtime.Scheme
	k8sClient client.Client
	ctx       context.Context
	cancel    context.CancelFunc
)

func TestMain(m *testing.M) {
	logf.SetLogger(zap.New(zap.WriteTo(os.Stdout), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	k8sScheme = runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(k8sScheme)
	_ = corev1.AddToScheme(k8sScheme)
	_ = openbaov1alpha1.AddToScheme(k8sScheme)
	_ = gatewayv1.Install(k8sScheme)

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "fixtures", "cilium"),
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("..", "manifests", "gateway-api", "v1.5.1", "crds"),
			filepath.Join("..", "manifests", "prometheus-operator", "v0.91.0", "crds"),
		},
		ErrorIfCRDPathMissing: true,
	}

	if assetsDir := getFirstFoundEnvTestBinaryDir(); assetsDir != "" {
		testEnv.BinaryAssetsDirectory = assetsDir
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		panic(err)
	}

	// Conservative defaults; envtest runs in-process and should not require high QPS.
	cfg.QPS = 20
	cfg.Burst = 40

	k8sClient, err = client.New(cfg, client.Options{Scheme: k8sScheme})
	if err != nil {
		panic(err)
	}

	// Some reconciliation logic (e.g., NetworkPolicy CIDR inference) expects the
	// well-known default/kubernetes Service to exist.
	seedKubernetesService()

	code := m.Run()

	cancel()
	if err := testEnv.Stop(); err != nil {
		panic(err)
	}

	os.Exit(code)
}

func seedKubernetesService() {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubernetes",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "https", Port: 443},
			},
		},
	}

	if err := k8sClient.Create(ctx, svc); err != nil && !apierrors.IsAlreadyExists(err) {
		panic(err)
	}
}

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// This mirrors the controller suites so tests can be run directly (e.g., from an IDE).
func getFirstFoundEnvTestBinaryDir() string {
	if assetsDir := os.Getenv("KUBEBUILDER_ASSETS"); assetsDir != "" {
		absoluteAssetsDir, err := filepath.Abs(assetsDir)
		if err != nil {
			return ""
		}
		return absoluteAssetsDir
	}

	basePath := filepath.Join("..", "..", "bin", "k8s")
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
