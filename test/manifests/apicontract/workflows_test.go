package apicontract

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflow struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	If              string            `yaml:"if"`
	Needs           yaml.Node         `yaml:"needs"`
	Outputs         map[string]string `yaml:"outputs"`
	ContinueOnError bool              `yaml:"continue-on-error"`
	Steps           []workflowStep    `yaml:"steps"`
}

type workflowStep struct {
	Name            string            `yaml:"name"`
	Run             string            `yaml:"run"`
	Env             map[string]string `yaml:"env"`
	ContinueOnError bool              `yaml:"continue-on-error"`
}

func TestAPIContractPreservesOperatorUpgradeHarness(t *testing.T) {
	// Keep the approved API migration fixture pair independent of both the
	// runtime upgrade baseline and the schema checker baseline.
	env := []string{
		"OPERATOR_UPGRADE_E2E_FROM_VERSION=",
		"OPERATOR_UPGRADE_E2E_TARGET_RELEASE=",
		"OPERATOR_UPGRADE_E2E_TARGET_VERSION=",
	}
	output, err := runCommand(t, repositoryRoot(t), env, "make", "verify-operator-upgrade-e2e")
	if err != nil {
		t.Fatalf("API contract changes must preserve the approved operator-upgrade rehearsal: %v\n%s", err, output)
	}
}

func TestAPIContractWorkflowDependencies(t *testing.T) {
	ci := readWorkflow(t, "ci.yml")
	release := readWorkflow(t, "release.yml")
	for _, w := range []workflow{ci, release} {
		gate, ok := w.Jobs["api-contract"]
		if !ok || gate.ContinueOnError || findStep(t, gate, "Verify API contract").ContinueOnError {
			t.Fatal("API contract must be a blocking job and step")
		}
	}
	for _, job := range []string{"ci-required", "build-edge-candidate"} {
		assertNeeds(t, ci.Jobs[job], "api-contract")
	}
	for _, job := range []string{"build", "rebuild", "promote"} {
		assertNeeds(t, release.Jobs[job], "api-contract")
	}
	if release.Jobs["api-contract"].If != "" {
		t.Fatal("release API contract gate must run unconditionally")
	}
	want := "github.event_name == 'workflow_dispatch' || " +
		"(github.event_name == 'push' && github.ref == 'refs/heads/main') || " +
		"needs.changes.outputs.api_contract == 'true'"
	if ci.Jobs["api-contract"].If != want {
		t.Fatal("CI must gate contract changes, main pushes, and manual runs")
	}
	if ci.Jobs["changes"].Outputs["api_contract"] != "${{ steps.diff.outputs.api_contract }}" {
		t.Fatal("change detection must publish the API contract routing result")
	}
}

func TestAPIContractChangeRouting(t *testing.T) {
	w := readWorkflow(t, "ci.yml")
	script := findStep(t, w.Jobs["changes"], "Detect chart changes").Run
	bin := t.TempDir()
	// Diff input is local and deterministic; the workflow cannot fetch the network.
	git := "#!/usr/bin/env bash\ncase \"$1\" in\n" +
		"fetch) exit 0 ;;\ndiff) printf '%s\\n' \"${CHANGED_PATHS}\" ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o700); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		"api/v1alpha1/openbaocluster_types.go",
		"api/stability/v1alpha1.yaml",
		"api/stability/v1alpha1-paths.tsv",
		"api/stability/baselines/0.5.0.json",
		"config/crd/bases/openbao.org_openbaoclusters.yaml",
		"hack/tools/api_inventory/main.go",
		"hack/tools/crd_compatibility/main.go",
		"test/manifests/apicontract/gate_test.go",
		"Makefile",
		"mk/development.mk",
		"go.mod",
		"vendor/modules.txt",
		".github/workflows/ci.yml",
		".github/workflows/release.yml",
		".github/actions/setup-repo-tools/action.yml",
		"website/content/contribute/setup.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "outputs")
			env := []string{
				"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"EVENT_NAME=pull_request", "GITHUB_SHA=head", "PR_BASE_SHA=base", "PR_HEAD_SHA=head",
				"GITHUB_OUTPUT=" + outputPath, "CHANGED_PATHS=" + path,
			}
			output, err := runCommand(t, repositoryRoot(t), env, "bash", "-ec", script)
			if err != nil {
				t.Fatalf("run change detector: %v\n%s", err, output)
			}
			data, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			want := "api_contract=true\n"
			if strings.HasPrefix(path, "website/") {
				want = "api_contract=false\n"
			}
			if !strings.Contains(string(data), want) {
				t.Fatalf("routing for %s: want %s, got\n%s", path, want, data)
			}
		})
	}
}

func TestAPIContractResultBlocksRequiredCI(t *testing.T) {
	w := readWorkflow(t, "ci.yml")
	step := findStep(t, w.Jobs["ci-required"], "Validate required CI graph")
	if step.Env["API_CONTRACT"] != "${{ needs.api-contract.result }}" {
		t.Fatal("required result aggregation must read the API contract result")
	}
	for _, result := range []string{"success", "failure", "cancelled"} {
		t.Run(result, func(t *testing.T) {
			env := make([]string, 0, len(step.Env))
			for key := range step.Env {
				value := "success"
				if key == "API_CONTRACT" {
					value = result
				}
				env = append(env, key+"="+value)
			}
			output, err := runCommand(t, repositoryRoot(t), env, "bash", "-ec", step.Run)
			if result == "success" {
				if err != nil {
					t.Fatalf("passing graph rejected: %v\n%s", err, output)
				}
				return
			}
			if err == nil || !strings.Contains(output, "api-contract:"+result) {
				t.Fatalf("API contract %s must block CI Required: error=%v\n%s", result, err, output)
			}
		})
	}
}

func readWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".github/workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

func findStep(t *testing.T, job workflowJob, name string) workflowStep {
	t.Helper()
	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("workflow step %q is missing", name)
	return workflowStep{}
}

func assertNeeds(t *testing.T, job workflowJob, prerequisite string) {
	t.Helper()
	var needs []string
	if err := job.Needs.Decode(&needs); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(needs, prerequisite) {
		t.Fatalf("job dependencies %v must include %s", needs, prerequisite)
	}
}
