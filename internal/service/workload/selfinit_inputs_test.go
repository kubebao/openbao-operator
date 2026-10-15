package workload

import (
	"encoding/json"
	"strings"
	"testing"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/initmanager"
	"github.com/stretchr/testify/require"
)

func TestSelfInitSecretInputWorkloads(t *testing.T) {
	tests := []struct {
		name                                                      string
		initialized, selfInitialized, disableSelfInit, wantInputs bool
	}{
		{name: "fresh initialization", wantInputs: true},
		{name: "initialized replacement", initialized: true},
		{name: "completed self-init replacement", selfInitialized: true},
		{name: "upgrade green pool", disableSelfInit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("bao", "tenant")
			cluster.Status.Initialized = tt.initialized
			cluster.Status.SelfInitialized = tt.selfInitialized
			cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{Enabled: true, Requests: []openbaov1alpha1.SelfInitRequest{{
				Name: "login", Path: "auth/userpass/users/admin",
				DataFrom: map[string]openbaov1alpha1.SelfInitDataSource{
					"password": {SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "bootstrap", Key: "password"}},
				},
			}}}
			sts, err := buildStatefulSetForSpec(cluster, "config", tt.initialized,
				StatefulSetSpec{Name: "bao", DisableSelfInit: tt.disableSelfInit}, constants.PlatformKubernetes)
			require.NoError(t, err)
			pod := sts.Spec.Template.Spec
			found := false
			for _, volume := range pod.Volumes {
				if volume.Name != selfInitInputsVolumeName {
					continue
				}
				found = true
				require.NotNil(t, volume.Projected)
				require.EqualValues(t, 0o440, *volume.Projected.DefaultMode)
				require.Len(t, volume.Projected.Sources, 1)
				source := volume.Projected.Sources[0].Secret
				require.True(t, *source.Optional, "stale templates must not block initialized restarts")
				require.Equal(t, "bootstrap", source.Name)
				require.Equal(t, "password", source.Items[0].Key)
				inputs, err := initmanager.SecretInputs(cluster.Spec.SelfInit.Requests)
				require.NoError(t, err)
				require.Equal(t, inputs[0].FileName, source.Items[0].Path)
				require.Contains(t, pod.InitContainers[0].Args, "--self-init-secret-file="+inputs[0].Path())
			}
			require.Equal(t, tt.wantInputs, found)
			for _, container := range append(pod.InitContainers, pod.Containers...) {
				mounted := false
				for _, mount := range container.VolumeMounts {
					if mount.Name == selfInitInputsVolumeName {
						mounted = true
						require.True(t, mount.ReadOnly)
						require.Empty(t, mount.SubPath)
					}
				}
				require.Equal(t, tt.wantInputs, mounted)
				if !tt.wantInputs {
					require.NotContains(t, strings.Join(container.Args, " "), "--self-init-secret-file")
				}
			}
			encoded, err := json.Marshal(sts)
			require.NoError(t, err)
			if !tt.wantInputs {
				require.NotContains(t, string(encoded), `"bootstrap"`)
			}
		})
	}
}
