//go:build integration

package perf

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestNativeClusterAdmission(t *testing.T) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testEnv.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "perf-admission"}}
	require.NoError(t, c.Create(ctx, namespace))
	for _, name := range []string{
		"openbao-validate-openbaocluster.yaml", "openbao-validate-openbaocluster-binding.yaml",
	} {
		data, readErr := os.ReadFile(filepath.Join("..", "..", "config", "policy", name))
		require.NoError(t, readErr)
		object := &unstructured.Unstructured{}
		decoder := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
		require.NoError(t, decoder.Decode(&object.Object))
		require.NoError(t, c.Create(ctx, object))
	}
	native := &nativeScenarioContext{
		namespace: namespace.Name,
		runID:     "admission",
		opts: Config{
			ConfigInitImage:      "openbao-init:perf-admission",
			OpenBaoVersion:       "2.6.4",
			OpenBaoImage:         "openbao/openbao:2.6.4",
			UpgradeFromVersion:   "2.6.1",
			UpgradeFromImage:     "openbao/openbao:2.6.1",
			UpgradeExecutorImage: "openbao-upgrade:perf-admission",
			APIServerCIDR:        "10.96.0.0/12",
		},
	}
	legacy := native.buildCluster("legacy-perf-probe", "2.6.4", "openbao/openbao:2.6.4", 1)
	legacy.Spec.ControllerJWTMode = ""
	require.Eventually(t, func() bool {
		probeErr := c.Create(ctx, legacy, client.DryRunAll)
		return probeErr != nil && strings.Contains(probeErr.Error(), "spec.controllerJWTMode is required to be Target")
	}, 10*time.Second, 100*time.Millisecond, "current CREATE admission must reject the old performance fixture")
	for _, cluster := range []*api.OpenBaoCluster{
		native.buildCluster("perf-lifecycle", native.opts.OpenBaoVersion, native.opts.OpenBaoImage, 1),
		native.buildRollingUpgradeCluster(),
	} {
		t.Run(cluster.Name, func(t *testing.T) {
			require.NoError(t, c.Create(ctx, cluster), "admit the actual native performance builder")
			require.Equal(t, api.ControllerJWTModeTarget, cluster.Spec.ControllerJWTMode)
		})
	}
}
