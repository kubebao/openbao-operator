//go:build integration

package integration

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// TestControllerJWTMode_StorageDefaultingBoundary demonstrates why the schema
// must not default Target when legacy objects omit the mode.
// The isolated API server prevents the hypothetical default from affecting
// other integration tests.
func TestControllerJWTMode_StorageDefaultingBoundary(t *testing.T) {
	c, _, config := startIsolatedEnv(t, isolatedEnvOptions{
		crdDirs: []string{filepath.Join("..", "..", "config", "crd", "bases")},
	})
	namespace := createNamespace(t, ctx, c)
	clusters := map[string]*api.OpenBaoCluster{}
	for _, name := range []string{"untouched", "initialized", "pinned", "explicit-shared", "explicit-target"} {
		cluster := newMinimalClusterObj(namespace, name)
		cluster.Spec.ControllerJWTMode = ""
		cluster.Spec.SelfInit = nil
		if name == "explicit-shared" {
			cluster.Spec.ControllerJWTMode = api.ControllerJWTModeShared
		}
		if name == "explicit-target" {
			cluster.Spec.ControllerJWTMode = api.ControllerJWTModeTarget
			cluster.Spec.SelfInit = &api.SelfInitConfig{
				Enabled: true, OIDC: &api.SelfInitOIDCConfig{Enabled: true},
			}
		}
		require.NoError(t, c.Create(ctx, cluster))
		clusters[name] = cluster
	}

	// Initialization evidence covers only a subset of old objects. Both this
	// object and the untouched object must retain the old authentication mode.
	initialized := clusters["initialized"]
	initialized.Status.Initialized = true
	require.NoError(t, c.Status().Update(ctx, initialized))
	pinned := clusters["pinned"]
	pinned.Spec.ControllerJWTMode = api.ControllerJWTModeShared
	require.NoError(t, c.Update(ctx, pinned))

	extensions, err := apiextensionsclient.NewForConfig(config)
	require.NoError(t, err)
	crd, err := extensions.ApiextensionsV1().CustomResourceDefinitions().Get(
		ctx, "openbaoclusters.openbao.org", metav1.GetOptions{})
	require.NoError(t, err)
	for i := range crd.Spec.Versions {
		schema := crd.Spec.Versions[i].Schema.OpenAPIV3Schema
		spec := schema.Properties["spec"]
		mode := spec.Properties["controllerJWTMode"]
		require.Nil(t, mode.Default, "the shipping schema must preserve legacy omission")
		mode.Default = &apiextensionsv1.JSON{Raw: []byte(`"Target"`)}
		spec.Properties["controllerJWTMode"] = mode
		schema.Properties["spec"] = spec
	}
	_, err = extensions.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, crd, metav1.UpdateOptions{})
	require.NoError(t, err)

	// No controller, mutating webhook, or object update participates in these
	// reads. The new storage-version default alone changes the visible mode.
	for _, name := range []string{"untouched", "initialized"} {
		cluster := clusters[name]
		require.Eventually(t, func() bool {
			err := c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)
			return err == nil && cluster.Spec.ControllerJWTMode == api.ControllerJWTModeTarget
		}, 10*time.Second, 100*time.Millisecond)
		require.Nil(t, cluster.Spec.SelfInit, "a CRD default does not arrange Target bootstrap prerequisites")
	}
	for _, name := range []string{"pinned", "explicit-shared", "explicit-target"} {
		cluster := clusters[name]
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
		expected := api.ControllerJWTModeShared
		if name == "explicit-target" {
			expected = api.ControllerJWTModeTarget
		}
		require.Equal(t, expected, cluster.Spec.ControllerJWTMode)
	}

	// Keeping Shared in the declared object survives reapply. Removing it after
	// the default change becomes Target before validating admission can inspect
	// the request, so a one-time storage pin cannot replace a GitOps update.
	pinned.Labels = map[string]string{"gitops-reapply": "declared-shared"}
	require.NoError(t, c.Update(ctx, pinned))
	require.Equal(t, api.ControllerJWTModeShared, pinned.Spec.ControllerJWTMode)
	pinned.Spec.ControllerJWTMode = ""
	require.NoError(t, c.Update(ctx, pinned))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pinned), pinned))
	require.Equal(t, api.ControllerJWTModeTarget, pinned.Spec.ControllerJWTMode)
}
