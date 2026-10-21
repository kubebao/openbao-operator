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

func TestHelmPodMetadataSyncCallers(t *testing.T) {
	for _, test := range []struct {
		name, callers string
		want          map[string]map[string][]string
	}{
		{name: "disabled", callers: `[]`, want: map[string]map[string][]string{}},
		{name: "namespace-isolation", callers: `[
			{"username":"syncer","namespaces":["tenant-a"],"annotationKeys":["example.com/a"]},
			{"username":"syncer","namespaces":["tenant-b"],"annotationKeys":["example.com/b"]},
			{"username":"syncer","namespaces":["tenant-a"],"annotationKeys":["example.com/c"]}]`,
			want: map[string]map[string][]string{"syncer": {
				"tenant-a": {"example.com/a", "example.com/c"}, "tenant-b": {"example.com/b"}}}},
		{name: "quoted-username", callers: `[{"username":"user\"quoted\\name",
			"namespaces":["tenant"],"annotationKeys":["example.com/key"]}]`,
			want: map[string]map[string][]string{"user\"quoted\\name": {"tenant": {"example.com/key"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rendered := renderChart(t, "--set-json", "admissionPolicies.podMetadataSync.allowedCallers="+test.callers)
			assertStrictYAML(t, rendered)
			for _, document := range bytes.Split(rendered, []byte("\n---")) {
				var policy admissionv1.ValidatingAdmissionPolicy
				require.NoError(t, yaml.Unmarshal(document, &policy))
				if !strings.HasSuffix(policy.Name, "openbao-lock-managed-resource-mutations") {
					continue
				}
				for _, variable := range policy.Spec.Variables {
					if variable.Name == "pod_metadata_sync_callers" {
						var actual map[string]map[string][]string
						require.NoError(t, json.Unmarshal([]byte(variable.Expression), &actual))
						require.Equal(t, test.want, actual)
						return
					}
				}
			}
			t.Fatal("Pod metadata sync caller variable not found")
		})
	}
}

func TestHelmPodMetadataSyncRejectsInvalidCallers(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm binary not available")
	}
	for _, callers := range []string{
		`[{"username":"syncer","namespaces":["tenant"]}]`,
		`[{"username":"syncer","namespaces":[],"annotationKeys":["example.com/key"]}]`,
		`[{"username":"syncer","namespaces":["tenant"],"annotationKeys":[]}]`,
		`[{"username":"*","namespaces":["tenant"],"annotationKeys":["example.com/key"]}]`,
		`[{"username":"syncer","namespaces":["*"],"annotationKeys":["example.com/key"]}]`,
		`[{"username":"syncer","namespaces":["tenant"],"annotationKeys":["example.com/*"]}]`,
		`[{"username":"syncer","namespaces":["tenant"],"annotationKeys":["openbao.org/maintenance"]}]`,
		`[{"username":"syncer","namespaces":["tenant"],"annotationKeys":["vcluster.loft.sh/uid"]}]`,
		`[{"username":"syncer","namespaces":["tenant"],"annotationKeys":["kubernetes.io/config.mirror"]}]`,
		`[{"username":"syncer","namespaces":["tenant"],"annotationKeys":["seccomp.security.alpha.kubernetes.io/pod"]}]`,
		`[{"username":"syncer","namespaces":["tenant"],"annotationKeys":["k8s.io/test"]}]`,
		`[{"username":"syncer","namespaces":["tenant"],"annotationKeys":["example.com/key"],"groups":["system:masters"]}]`,
	} {
		t.Run(callers, func(t *testing.T) {
			command := exec.Command("helm", "template", "test", "../../charts/openbao-operator",
				"--set-json", "admissionPolicies.podMetadataSync.allowedCallers="+callers)
			output, err := command.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(output), "allowedCallers")
		})
	}
}

func TestPodMetadataSyncGeneratorRequiresDefaultVariable(t *testing.T) {
	_, err := addPodMetadataSyncCallers("spec: {}")
	require.ErrorContains(t, err, "expected exactly one")
	source, err := os.ReadFile(filepath.Join("..", "..", "config", "policy",
		"openbao-lock-managed-resource-mutations.yaml"))
	require.NoError(t, err)
	generated, err := addPodMetadataSyncCallers(string(source))
	require.NoError(t, err)
	require.Contains(t, generated, `include "openbao-operator.podMetadataSyncCallers" . | quote`)
}
