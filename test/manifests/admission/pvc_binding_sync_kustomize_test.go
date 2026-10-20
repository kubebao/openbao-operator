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

func TestKustomizePVCBindingSyncOverlay(t *testing.T) {
	binary := "../../../bin/kustomize"
	if _, err := exec.LookPath(binary); err != nil {
		binary, err = exec.LookPath("kustomize")
		require.NoError(t, err, "run make kustomize to install the repo-managed tool")
	}
	output, err := exec.Command(binary, "build", "../../../config/overlays/pvc-binding-sync").CombinedOutput()
	require.NoError(t, err, "%s", output)
	source := readClusterPolicySpec(t, "../../../config/policy/openbao-lock-managed-resource-mutations.yaml")
	for _, document := range bytes.Split(output, []byte("\n---")) {
		var policy admissionv1.ValidatingAdmissionPolicy
		require.NoError(t, yaml.Unmarshal(document, &policy))
		if policy.Name != "openbao-operator-openbao-lock-managed-resource-mutations" {
			continue
		}
		require.Len(t, policy.Spec.Variables, len(source.Variables))
		for _, variable := range policy.Spec.Variables {
			if variable.Name == "pvc_binding_sync_callers" {
				var callers map[string][]string
				require.NoError(t, json.Unmarshal([]byte(variable.Expression), &callers))
				require.Equal(t, map[string][]string{"kubernetes-super-admin": {"openbao-demo"}}, callers)
			}
			if variable.Name == "pvc_binding_sync_preserves_spec" {
				for _, original := range source.Variables {
					if original.Name == variable.Name {
						require.Equal(t, strings.Fields(original.Expression), strings.Fields(variable.Expression))
					}
				}
			}
		}
		return
	}
	t.Fatal("managed-resource mutation policy is missing")
}
