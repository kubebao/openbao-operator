//go:build integration

package integration

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestOperatorUpgradeStaticRestoreFixtureAdmission(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoRestoreAdmissionPolicies(t, namespace)

	data, err := os.ReadFile(filepath.Join("..", "fixtures", "operator-upgrade", "static-restore-token-guardrail.yaml"))
	require.NoError(t, err)
	decoder := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
	sawRestore := false
	for {
		object := &unstructured.Unstructured{}
		err := decoder.Decode(&object.Object)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		object.SetNamespace(namespace)
		require.NoError(t, k8sClient.Create(ctx, object), "admit fixture %s/%s", object.GetKind(), object.GetName())
		sawRestore = sawRestore || object.GetKind() == "OpenBaoRestore"
	}
	require.True(t, sawRestore, "fixture must contain an OpenBaoRestore")
}
