package admission

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestNewClusterSamplesUseTargetBootstrap(t *testing.T) {
	paths, err := filepath.Glob("../../../config/samples/*/*.yaml")
	require.NoError(t, err)
	count := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
		for {
			document, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err)
			var meta struct {
				Kind string `json:"kind"`
			}
			require.NoError(t, yaml.Unmarshal(document, &meta))
			if meta.Kind != "OpenBaoCluster" {
				continue
			}
			count++
			t.Run(filepath.Base(path), func(t *testing.T) {
				var cluster api.OpenBaoCluster
				require.NoError(t, yaml.UnmarshalStrict(document, &cluster), "sample must have no duplicate or unknown fields")
				require.Equal(t, api.ControllerJWTModeTarget, cluster.Spec.ControllerJWTMode)
				require.NotNil(t, cluster.Spec.SelfInit)
				require.True(t, cluster.Spec.SelfInit.Enabled)
				require.NotNil(t, cluster.Spec.SelfInit.OIDC)
				require.True(t, cluster.Spec.SelfInit.OIDC.Enabled)
				require.NotEmpty(t, cluster.Spec.SelfInit.Requests)
			})
		}
	}
	require.Positive(t, count, "sample discovery must find cluster manifests")
}
