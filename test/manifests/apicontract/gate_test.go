package apicontract

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// Execute the workflow's actual gate in an isolated checkout fixture. Refreshing
// the inventory must not authorize removal or tighter validation of released fields.
func TestWorkflowAPIContractCommands(t *testing.T) {
	tests := []struct {
		name     string
		mutation func(*apiextensionsv1.JSONSchemaProps)
		refresh  bool
		want     string
	}{
		{name: "released schema passes"},
		{
			name: "reviewed optional addition passes",
			mutation: func(spec *apiextensionsv1.JSONSchemaProps) {
				ingress := spec.Properties["ingress"]
				ingress.Properties["futureOption"] = apiextensionsv1.JSONSchemaProps{Type: "string"}
			},
			refresh: true,
		},
		{
			name: "removed field fails even with refreshed inventory",
			mutation: func(spec *apiextensionsv1.JSONSchemaProps) {
				delete(spec.Properties["ingress"].Properties, "className")
			},
			refresh: true,
			want:    "field-removed",
		},
		{
			name: "tightened validation fails even with refreshed inventory",
			mutation: func(spec *apiextensionsv1.JSONSchemaProps) {
				ingress := spec.Properties["ingress"]
				field := ingress.Properties["host"]
				minimum := int64(2)
				field.MinLength = &minimum
				ingress.Properties["host"] = field
			},
			refresh: true,
			want:    "validation-tightened",
		},
		{
			name: "unclassified field fails",
			mutation: func(spec *apiextensionsv1.JSONSchemaProps) {
				spec.Properties["unclassified"] = apiextensionsv1.JSONSchemaProps{Type: "string"}
			},
			want: "spec.unclassified: top-level field requires an explicit rule",
		},
		{
			name: "unreviewed nested addition fails",
			mutation: func(spec *apiextensionsv1.JSONSchemaProps) {
				spec.Properties["ingress"].Properties["futureOption"] = apiextensionsv1.JSONSchemaProps{Type: "string"}
			},
			want: "is out of date",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := copyContractFixture(t)
			if tt.mutation != nil {
				mutateClusterSchema(t, dir, tt.mutation)
			}
			if tt.refresh {
				output, err := runCommand(t, dir, nil, "make", "update-api-stability-inventory")
				if err != nil {
					t.Fatalf("refresh fixture inventory: %v\n%s", err, output)
				}
			}
			for _, workflow := range []string{"ci.yml", "release-candidate.yml"} {
				gate := readWorkflow(t, workflow).Jobs["api-contract"]
				step := findStep(t, gate, "Verify API contract")
				output, err := runCommand(t, dir, []string{"CRD_COMPAT_MODE=report"}, "bash", "-ec", step.Run)
				if tt.want == "" {
					if err != nil {
						t.Fatalf("%s gate rejected compatible fixture: %v\n%s", workflow, err, output)
					}
					continue
				}
				if err == nil || !strings.Contains(output, tt.want) {
					t.Fatalf("%s gate: error=%v, want rejection containing %q\n%s", workflow, err, tt.want, output)
				}
			}
			if tt.refresh && tt.want != "" {
				output, err := runCommand(t, dir, nil, "go", "run", "./hack/tools/crd_compatibility")
				if err == nil || !strings.Contains(output, tt.want) {
					t.Fatalf("checker default must enforce compatibility: error=%v\n%s", err, output)
				}
			}
		})
	}
}

func copyContractFixture(t *testing.T) string {
	t.Helper()
	root := repositoryRoot(t)
	dir := t.TempDir()
	for _, path := range []string{"mk", "api/stability", "config/crd/bases"} {
		if err := os.CopyFS(filepath.Join(dir, path), os.DirFS(filepath.Join(root, path))); err != nil {
			t.Fatalf("copy fixture %s: %v", path, err)
		}
	}
	for _, path := range []string{"Makefile", "go.mod", "go.sum", "vendor", "hack"} {
		if err := os.Symlink(filepath.Join(root, path), filepath.Join(dir, path)); err != nil {
			t.Fatalf("link fixture %s: %v", path, err)
		}
	}
	return dir
}

func mutateClusterSchema(t *testing.T, dir string, mutate func(*apiextensionsv1.JSONSchemaProps)) {
	t.Helper()
	path := filepath.Join(dir, "config/crd/bases/openbao.org_openbaoclusters.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.UnmarshalStrict(data, &crd); err != nil {
		t.Fatal(err)
	}
	root := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	spec := root.Properties["spec"]
	mutate(&spec)
	root.Properties["spec"] = spec
	data, err = yaml.Marshal(crd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func runCommand(t *testing.T, dir string, env []string, command string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
