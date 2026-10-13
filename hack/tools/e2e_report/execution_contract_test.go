package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type executionStep struct {
	Name            string            `yaml:"name"`
	If              string            `yaml:"if"`
	Run             string            `yaml:"run"`
	Uses            string            `yaml:"uses"`
	Env             map[string]string `yaml:"env"`
	With            map[string]string `yaml:"with"`
	ContinueOnError bool              `yaml:"continue-on-error"`
}

func TestExecutionBundleIsSharedAndBlocking(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, name := range []string{"ci", "nightly", "release"} {
		t.Run(name, func(t *testing.T) {
			var workflow struct {
				Jobs map[string]struct {
					Steps []executionStep `yaml:"steps"`
				} `yaml:"jobs"`
			}
			decodeExecutionYAML(t, filepath.Join(root, ".github/workflows", name+".yml"), &workflow)
			compile, upload, selectedPlan := -1, -1, -1
			for index, step := range workflow.Jobs["e2e-matrix"].Steps {
				if step.Name == "Generate E2E matrix" || step.Name == "Generate release-gate E2E matrix" {
					selectedPlan = index
				}
				if step.Name == "Compile E2E executables once" {
					compile = index
					if step.Run != "make build-e2e-binaries" || step.ContinueOnError || step.If != "" ||
						step.Env["E2E_SOURCE_SHA"] != "${{ github.sha }}" {
						t.Fatal("compilation must block the source-matched matrix")
					}
				}
				if step.Name == "Upload E2E binaries" {
					upload = index
					if step.With["name"] != "e2e-binaries" || step.With["overwrite"] != "true" ||
						step.With["if-no-files-found"] != "error" {
						t.Fatal("failed-job reruns must reuse one run-scoped binary bundle")
					}
				}
			}
			if selectedPlan < 0 || compile <= selectedPlan || upload <= compile {
				t.Fatal("select lanes before compiling and uploading the bundle")
			}
			for _, step := range workflow.Jobs["e2e"].Steps {
				if step.Uses == "./.github/actions/run-e2e-lane" {
					if !strings.Contains(step.With["artifact-name"], "${{ github.run_attempt }}") {
						t.Fatal("lane evidence can mix run attempts")
					}
				}
				if strings.Contains(step.Run, "go test") || strings.Contains(step.Run, "go run") {
					t.Fatal("matrix rows must execute the shared compiled suite and reporter")
				}
			}
		})
	}
}

func TestSharedExecutionRetainsEvidenceBeforeCleanup(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var action struct {
		Runs struct {
			Steps []executionStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	decodeExecutionYAML(t, filepath.Join(root, ".github/actions/run-e2e-lane/action.yml"), &action)
	order := map[string]int{}
	for index, step := range action.Runs.Steps {
		order[step.Name] = index
		if step.ContinueOnError {
			t.Fatalf("shared E2E step %s cannot suppress failures", step.Name)
		}
		switch step.Name {
		case "Configure E2E lane":
			if step.Run != "python3 hack/ci/configure-e2e-lane.py" || step.Env["LANE_JSON"] != "${{ inputs.lane }}" {
				t.Fatal("shared action must configure the planned row without rerouting it")
			}
		case "Download E2E binaries":
			if step.With["name"] != "e2e-binaries" || strings.Contains(step.With["path"], "artifacts/") {
				t.Fatal("binary downloads must stay outside uploaded lane evidence")
			}
		case "Verify E2E binaries":
			if !strings.Contains(step.Run, "e2e-binaries.py verify") || !strings.Contains(step.Run, "E2E_SOURCE_SHA") {
				t.Fatal("verify bundle identity before cluster preparation")
			}
		case "Summarize E2E report", "Upload E2E artifacts":
			if step.If != "always()" {
				t.Fatal("lane evidence must survive preparation or execution failures")
			}
		case "Delete E2E cluster":
			if !strings.Contains(step.If, "always()") || !strings.Contains(step.If, "steps.prepare.outcome == 'failure'") {
				t.Fatal("cleanup must include failed preparation after collecting diagnostics")
			}
		}
	}
	for _, names := range [][2]string{
		{"Verify E2E binaries", "Prepare E2E lane"},
		{"Prepare E2E lane", "Run E2E Tests"},
		{"Run E2E Tests", "Summarize E2E report"},
		{"Summarize E2E report", "Collect E2E artifacts (failure)"},
		{"Collect E2E artifacts (failure)", "Delete E2E cluster"},
		{"Delete E2E cluster", "Upload E2E artifacts"},
	} {
		left, leftExists := order[names[0]]
		right, rightExists := order[names[1]]
		if !leftExists || !rightExists || left >= right {
			t.Fatalf("shared step order must preserve %s before %s", names[0], names[1])
		}
	}
}

func decodeExecutionYAML(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
