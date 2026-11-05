package admission

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"
)

func TestHelmPolicySpecsSurviveKustomizeRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm binary not available")
	}
	kustomize := os.Getenv("KUSTOMIZE")
	if kustomize == "" {
		kustomize = filepath.Join("..", "..", "..", "bin", "kustomize")
	}
	kustomize, err := filepath.Abs(kustomize)
	require.NoError(t, err)
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "default"},
		{name: "custom-installation", args: []string{
			"--set", "fullnameOverride=security-control",
			"--set", "tenancy.mode=single",
			"--set", "tenancy.targetNamespace=tenant-a",
			"--set", "tenancy.namespacePodSecurityLabels.mode=external",
			"--set-json", `admissionPolicies.pvcBindingSync.allowedCallers=[{"username":"syncer","namespaces":["tenant-a"]}]`,
			"--set-json", `admissionPolicies.podMetadataSync.allowedCallers=` +
				`[{"username":"syncer","namespaces":["tenant-a"],"annotationKeys":["example.com/host-id"]}]`,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"template", "sample", "../../../charts/openbao-operator",
				"--namespace", "custom-operator-system"}, tt.args...)
			rendered, err := exec.Command("helm", args...).CombinedOutput()
			require.NoError(t, err, "%s", rendered)
			original := policySpecs(t, rendered)
			require.Len(t, original, 13)

			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, "kustomization.yaml"), []byte(`resources:
  - rendered.yaml
labels:
  - pairs:
      helm.toolkit.fluxcd.io/name: sample
      helm.toolkit.fluxcd.io/namespace: custom-operator-system
`), 0o600))
			// Repeated post-rendering must preserve the complete policy specification,
			// including CEL whitespace checked by the operator's release contract.
			for round := 1; round <= 2; round++ {
				require.NoError(t, os.WriteFile(filepath.Join(directory, "rendered.yaml"), rendered, 0o600))
				rendered, err = exec.Command(kustomize, "build", directory).CombinedOutput()
				require.NoError(t, err, "%s", rendered)
				require.Equal(t, original, policySpecs(t, rendered), "round trip %d", round)
			}
		})
	}
}

func policySpecs(t *testing.T, rendered []byte) map[string]admissionv1.ValidatingAdmissionPolicySpec {
	t.Helper()
	policies := map[string]admissionv1.ValidatingAdmissionPolicySpec{}
	for _, document := range bytes.Split(rendered, []byte("\n---")) {
		var policy admissionv1.ValidatingAdmissionPolicy
		require.NoError(t, yaml.Unmarshal(document, &policy))
		if policy.Kind == "ValidatingAdmissionPolicy" {
			policies[policy.Name] = policy.Spec
		}
	}
	return policies
}
