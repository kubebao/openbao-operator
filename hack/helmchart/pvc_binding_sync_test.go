package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"
)

func TestHelmPVCBindingSyncCallers(t *testing.T) {
	for _, callers := range []string{
		`[]`,
		`[{"username":"kubernetes-super-admin","namespaces":["tenant-a","tenant-b"]}]`,
		`[{"username":"user\"quoted\\name","namespaces":["tenant"]}]`,
	} {
		t.Run(callers, func(t *testing.T) {
			rendered := renderChart(t, "--set-json", "admissionPolicies.pvcBindingSync.allowedCallers="+callers)
			assertStrictYAML(t, rendered)
			for _, document := range bytes.Split(rendered, []byte("\n---")) {
				var policy admissionv1.ValidatingAdmissionPolicy
				require.NoError(t, yaml.Unmarshal(document, &policy))
				if !strings.HasSuffix(policy.Name, "openbao-lock-managed-resource-mutations") {
					continue
				}
				for _, variable := range policy.Spec.Variables {
					if variable.Name != "pvc_binding_sync_callers" {
						continue
					}
					var configured []struct {
						Username   string   `json:"username"`
						Namespaces []string `json:"namespaces"`
					}
					var actual map[string][]string
					expected := map[string][]string{}
					require.NoError(t, json.Unmarshal([]byte(callers), &configured))
					require.NoError(t, json.Unmarshal([]byte(variable.Expression), &actual))
					for _, caller := range configured {
						expected[caller.Username] = append(expected[caller.Username], caller.Namespaces...)
					}
					require.Equal(t, expected, actual)
					return
				}
			}
			t.Fatal("PVC binding sync caller variable not found")
		})
	}
}

func TestHelmPVCBindingSyncRejectsInvalidCallers(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm binary not available")
	}
	for _, callers := range []string{
		`[{"username":"syncer"}]`,
		`[{"username":"syncer","namespaces":[]}]`,
		`[{"username":"*","namespaces":["tenant"]}]`,
		`[{"username":"syncer","namespaces":["*"]}]`,
		`[{"username":"syncer","namespaces":[""]}]`,
		`[{"username":"syncer","namespaces":["tenant"],"groups":["system:masters"]}]`,
	} {
		t.Run(callers, func(t *testing.T) {
			command := exec.Command("helm", "template", "test", "../../charts/openbao-operator",
				"--set-json", "admissionPolicies.pvcBindingSync.allowedCallers="+callers)
			output, err := command.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(output), "allowedCallers")
		})
	}
}

func TestPVCBindingSyncGeneratorRequiresDefaultVariable(t *testing.T) {
	_, err := addPVCBindingSyncCallers("spec: {}")
	require.ErrorContains(t, err, "expected exactly one")
	sourcePath := filepath.Join("..", "..", "config", "policy", "openbao-lock-managed-resource-mutations.yaml")
	source, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	generated, err := addPVCBindingSyncCallers(string(source))
	require.NoError(t, err)
	require.Contains(t, generated, `include "openbao-operator.pvcBindingSyncCallers" . | quote`)
}
