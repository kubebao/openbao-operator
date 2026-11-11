package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	syaml "sigs.k8s.io/yaml"
)

const migrationHoldDeploymentKind = "Deployment"

func TestHelmAPIMigrationHoldControllerOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "default disabled"},
		{name: "enabled", args: []string{"--set", "controller.apiMigrationHold=true"}, want: true},
		{name: "explicitly disabled", args: []string{"--set", "controller.apiMigrationHold=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := renderChart(t, tc.args...)
			deployments := 0
			for _, document := range bytes.Split(rendered, []byte("\n---")) {
				var obj struct {
					Kind     string `json:"kind"`
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
					Spec struct {
						Template struct {
							Spec struct {
								Containers []struct {
									Args []string `json:"args"`
								} `json:"containers"`
							} `json:"spec"`
						} `json:"template"`
					} `json:"spec"`
				}
				if err := syaml.Unmarshal(document, &obj); err != nil {
					t.Fatal(err)
				}
				if obj.Kind != migrationHoldDeploymentKind {
					continue
				}
				deployments++
				found := false
				for _, container := range obj.Spec.Template.Spec.Containers {
					found = found || slices.Contains(container.Args, "--api-migration-hold=true")
				}
				want := tc.want && strings.HasSuffix(obj.Metadata.Name, "-controller")
				if found != want {
					t.Fatalf("%s hold flag=%v, want %v", obj.Metadata.Name, found, want)
				}
			}
			if deployments != 2 {
				t.Fatalf("expected controller and provisioner Deployments, got %d", deployments)
			}
		})
	}
}

func TestHelmAPIMigrationHoldRequiresBoolean(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm binary not available")
	}
	cmd := exec.CommandContext(t.Context(), "helm", "template", "test",
		filepath.Join("..", "..", "charts", "openbao-operator"),
		"--set-string", "controller.apiMigrationHold=perhaps")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "boolean") {
		t.Fatalf("invalid hold value must fail schema validation: error=%v\n%s", err, output)
	}
}
