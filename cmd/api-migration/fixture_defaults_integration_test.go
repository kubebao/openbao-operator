//go:build integration

package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	adapterconfig "github.com/kubebao/openbao-operator/internal/adapter/config"
)

func TestFixtureControllerProofPreservesRenderedConfigurationRealAPI(t *testing.T) {
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	live, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	require.NoError(t, live.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fixture-defaults"}}))
	for _, seeded := range []bool{false, true} {
		t.Run(map[bool]string{false: "unseeded diagnostic", true: "seeded fixture"}[seeded], func(t *testing.T) {
			cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: map[bool]string{false: "unseeded", true: "seeded"}[seeded], Namespace: "fixture-defaults"}, Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Version: "2.6.5", Image: "openbao/openbao:2.6.5", Replicas: 3, Profile: openbaov1alpha1.ProfileDevelopment,
				TLS: openbaov1alpha1.TLSConfig{Enabled: true, RotationPeriod: "720h"}, Storage: openbaov1alpha1.StorageConfig{Size: "1Gi"},
				InitContainer: &openbaov1alpha1.InitContainerConfig{Image: "openbao/openbao-init:test"},
			}}
			if seeded {
				data, err := json.Marshal(migrationFixtureConfiguration())
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(data, &cluster.Spec.Configuration))
			}
			require.NoError(t, live.Create(t.Context(), cluster))
			infra := adapterconfig.InfrastructureDetails{HeadlessServiceName: cluster.Name, Namespace: cluster.Namespace, APIPort: 8200, ClusterPort: 8201}
			before, err := adapterconfig.RenderHCL(cluster, infra)
			require.NoError(t, err)
			// This is the source-side controller-login proof used after rollback.
			require.NoError(t, live.Patch(t.Context(), cluster, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"configuration":{"raft":{"autopilot":{"maxTrailingLogs":1200,"cleanupDeadServers":false}}}}}`))))
			require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			require.Equal(t, "info", cluster.Spec.Configuration.LogLevel)
			require.NotNil(t, cluster.Spec.Configuration.UI)
			require.True(t, *cluster.Spec.Configuration.UI)
			after, err := adapterconfig.RenderHCL(cluster, infra)
			require.NoError(t, err)
			if seeded {
				require.Equal(t, before, after, "runtime Autopilot proof must not cause a voter template change")
			} else {
				require.NotEqual(t, before, after, "retain the diagnostic that distinguishes the old fixture")
			}
		})
	}
}
