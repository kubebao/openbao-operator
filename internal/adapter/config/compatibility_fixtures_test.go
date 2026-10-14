package config

import (
	"strings"
	"testing"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestRenderCompatibilityRaftNonVoter(t *testing.T) {
	for _, blueGreen := range []bool{false, true} {
		t.Run(map[bool]string{false: "read replica", true: "blue green"}[blueGreen], func(t *testing.T) {
			cluster := newMinimalCluster("raft-options", "default")
			cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{
				Raft: &openbaov1alpha1.RaftConfig{PerformanceMultiplier: int32Ptr(3)},
			}
			infra := InfrastructureDetails{Namespace: cluster.Namespace, APIPort: 8200, ClusterPort: 8201}
			if blueGreen {
				infra.TargetRevisionForJoin = "blue"
			} else {
				infra.RetryJoinAsNonVoter = true
			}
			got, err := RenderHCL(cluster, infra)
			require.NoError(t, err)
			require.Contains(t, string(got), "retry_join_as_non_voter = true")
			require.NotContains(t, string(got), "election_timeout")
		})
	}
}

func TestSelfInitRequestControlsRequireSupportedVersion(t *testing.T) {
	for _, version := range []string{"2.4.4", "2.5.5", "2.6.0-rc.1", "2.6.0", "v2.7.1+build.1"} {
		for _, control := range []string{"headers", "when"} {
			t.Run(version+"/"+control, func(t *testing.T) {
				cluster := newMinimalCluster("profile-versions", "default")
				cluster.Spec.Version = version
				request := openbaov1alpha1.SelfInitRequest{Name: "test", Operation: openbaov1alpha1.SelfInitOperationRead, Path: "sys/health"}
				if control == "headers" {
					request.Headers = map[string][]string{"X-Trace": {"value"}}
				} else {
					request.When = &apiextensionsv1.JSON{Raw: []byte("false")}
				}
				cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{Enabled: true, Requests: []openbaov1alpha1.SelfInitRequest{request}}
				_, err := RenderSelfInitHCL(cluster, nil)
				if version == "2.6.0" || strings.HasPrefix(version, "v2.7") {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, "require OpenBao >= 2.6.0")
				}
			})
		}
	}
}
