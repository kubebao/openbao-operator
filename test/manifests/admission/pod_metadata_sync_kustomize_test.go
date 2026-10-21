package admission

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"
)

func TestKustomizePodMetadataSyncOverlay(t *testing.T) {
	binary := "../../../bin/kustomize"
	if _, err := exec.LookPath(binary); err != nil {
		binary, err = exec.LookPath("kustomize")
		require.NoError(t, err, "run make kustomize to install the repo-managed tool")
	}
	output, err := exec.Command(binary, "build", "../../../config/overlays/pod-metadata-sync").CombinedOutput()
	require.NoError(t, err, "%s", output)
	source := readClusterPolicySpec(t, "../../../config/policy/openbao-lock-managed-resource-mutations.yaml")
	for _, document := range bytes.Split(output, []byte("\n---")) {
		var policy admissionv1.ValidatingAdmissionPolicy
		require.NoError(t, yaml.Unmarshal(document, &policy))
		if policy.Name != "openbao-operator-openbao-lock-managed-resource-mutations" {
			continue
		}
		require.Len(t, policy.Spec.Variables, len(source.Variables))
		for i, variable := range policy.Spec.Variables {
			if variable.Name == "pod_metadata_sync_callers" {
				var callers map[string]map[string][]string
				require.NoError(t, json.Unmarshal([]byte(variable.Expression), &callers))
				require.Equal(t, map[string]map[string][]string{
					"kubernetes-super-admin": {"openbao-demo": {"example.com/observed-host-annotation"}},
				}, callers)
			} else if !strings.Contains(variable.Expression, "openbao-operator-system") {
				require.Equal(t, strings.Fields(source.Variables[i].Expression), strings.Fields(variable.Expression), variable.Name)
			}
		}
		return
	}
	t.Fatal("managed-resource mutation policy is missing")
}
