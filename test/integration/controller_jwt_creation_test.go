//go:build integration

package integration

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	hardenedfixtures "github.com/kubebao/openbao-operator/test/fixtures/hardenedcontract"
)

func TestControllerJWTMode_CreateContract(t *testing.T) {
	c, _, _ := startIsolatedEnv(t, isolatedEnvOptions{
		crdDirs: []string{filepath.Join("..", "..", "config", "crd", "bases")},
	})
	namespace := createNamespace(t, ctx, c)
	legacy := []*api.OpenBaoCluster{
		newMinimalClusterObj(namespace, "legacy-development"),
		hardenedfixtures.NewValidCluster(namespace, "legacy-hardened"),
		hardenedfixtures.NewValidCluster(namespace, "legacy-hardened-shared"),
	}
	for i, cluster := range legacy {
		cluster.Spec.ControllerJWTMode = ""
		if i == 2 {
			cluster.Spec.ControllerJWTMode = api.ControllerJWTModeShared
		}
		// Older Hardened clusters did not require OIDC. Their ordinary updates
		// must remain usable after installing the new creation requirements.
		if cluster.Spec.SelfInit != nil {
			cluster.Spec.SelfInit.OIDC = nil
		}
		require.NoError(t, c.Create(ctx, cluster))
	}
	installControllerJWTAdmission(t, c, namespace)

	t.Run("legacy-read-update-and-gitops-reapply", func(t *testing.T) {
		for _, cluster := range legacy {
			uid, mode := cluster.UID, cluster.Spec.ControllerJWTMode
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
			require.Equal(t, mode, cluster.Spec.ControllerJWTMode)
			cluster.Labels = map[string]string{"gitops-reapply": "unchanged-mode"}
			require.NoError(t, c.Update(ctx, cluster))
			cluster.SetGroupVersionKind(api.GroupVersion.WithKind("OpenBaoCluster"))
			applyClientObject(t, c, cluster)
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
			require.Equal(t, uid, cluster.UID)
			require.Equal(t, mode, cluster.Spec.ControllerJWTMode)
			audience, err := portauth.ControllerJWTAudience(cluster, "shared-installation")
			require.NoError(t, err)
			require.Equal(t, "shared-installation", audience)
		}
		var list api.OpenBaoClusterList
		require.NoError(t, c.List(ctx, &list, client.InNamespace(namespace)))
		require.Len(t, list.Items, len(legacy))
	})
	for _, tt := range []struct {
		name, profile string
		mode          api.ControllerJWTMode
		oidc, allowed bool
	}{
		{name: "development-omitted", profile: "Development"},
		{name: "development-shared", profile: "Development", mode: api.ControllerJWTModeShared, allowed: true},
		{name: "development-target", profile: "Development", mode: api.ControllerJWTModeTarget, oidc: true, allowed: true},
		{name: "hardened-omitted", profile: "Hardened", oidc: true},
		{name: "hardened-shared", profile: "Hardened", mode: api.ControllerJWTModeShared, oidc: true},
		{name: "hardened-target", profile: "Hardened", mode: api.ControllerJWTModeTarget, oidc: true, allowed: true},
		{name: "hardened-no-oidc", profile: "Hardened", mode: api.ControllerJWTModeTarget},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(namespace, tt.name)
			if tt.profile == "Hardened" {
				cluster = hardenedfixtures.NewValidCluster(namespace, tt.name)
			}
			cluster.Spec.ControllerJWTMode = tt.mode
			if tt.oidc {
				cluster.Spec.SelfInit = hardenedfixtures.NewValidCluster(namespace, "template").Spec.SelfInit
			} else if cluster.Spec.SelfInit != nil {
				cluster.Spec.SelfInit.OIDC = nil
			}
			err := c.Create(ctx, cluster)
			if !tt.allowed {
				requireAdmissionDenied(t, err)
				require.Contains(t, err.Error(), "spec.controllerJWTMode")
				return
			}
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
			require.Equal(t, tt.mode, cluster.Spec.ControllerJWTMode)
		})
	}
	t.Run("delete-and-recreate-is-a-new-cluster", func(t *testing.T) {
		cluster := legacy[0]
		require.NoError(t, c.Delete(ctx, cluster))
		replacement := newMinimalClusterObj(namespace, cluster.Name)
		replacement.Spec.ControllerJWTMode = ""
		requireAdmissionDenied(t, c.Create(ctx, replacement))
	})
}

func installControllerJWTAdmission(t *testing.T, c client.Client, namespace string) {
	t.Helper()
	objects := parseYAMLToUnstructured(t,
		kustomizeBuild(t, filepath.Join("..", "..", "config", "default")),
		func(obj *unstructured.Unstructured) bool {
			return obj.GetAPIVersion() == "admissionregistration.k8s.io/v1" &&
				(strings.HasSuffix(obj.GetName(), "openbao-validate-openbaocluster") ||
					strings.HasSuffix(obj.GetName(), "openbao-validate-openbaocluster-binding"))
		})
	require.Len(t, objects, 2)
	for _, obj := range objects {
		applyClientObject(t, c, obj)
	}
	var probeError error
	require.Eventually(t, func() bool {
		probe := newMinimalClusterObj(namespace, "jwt-creation-policy-probe")
		probe.Spec.ControllerJWTMode = ""
		probeError = c.Create(ctx, probe, &client.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		return probeError != nil && strings.Contains(probeError.Error(), "spec.controllerJWTMode is required")
	}, 10*time.Second, 100*time.Millisecond, "CREATE-only JWT policy must be enforced")
	requireAdmissionDenied(t, probeError)
}
