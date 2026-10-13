package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2/types"
	"gopkg.in/yaml.v3"
)

// Exercise the actual blocking summary steps. A missing report must not turn
// a successfully started matrix row into a successful qualification result.
func TestWorkflowScenarioValidationBlocksIncompleteEvidence(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "e2e_report")
	cmd := exec.Command("go", "build", "-mod=vendor", "-o", binary, "./hack/tools/e2e_report")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build reporter: %v\n%s", err, output)
	}
	for _, name := range []string{"ci", "nightly", "release"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", name+".yml"))
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				Jobs map[string]struct {
					Steps []struct {
						Name string            `yaml:"name"`
						If   string            `yaml:"if"`
						Run  string            `yaml:"run"`
						Env  map[string]string `yaml:"env"`
						Uses string            `yaml:"uses"`
						With map[string]string `yaml:"with"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(data, &workflow); err != nil {
				t.Fatal(err)
			}
			catalogChecked := false
			for _, step := range workflow.Jobs["e2e-matrix"].Steps {
				if step.Run == "make verify-e2e-manifest" && step.If == "" {
					catalogChecked = true
				}
			}
			if !catalogChecked {
				t.Fatal("matrix planning must reject stale scenario catalogs before selecting cases")
			}
			shared := false
			for _, step := range workflow.Jobs["e2e"].Steps {
				if step.Uses == "./.github/actions/run-e2e-lane" && step.With["lane"] == "${{ toJSON(matrix) }}" {
					shared = true
				}
			}
			if !shared {
				t.Fatal("every lane must use the shared execution and scenario validation action")
			}
			actionData, err := os.ReadFile(filepath.Join(root, ".github/actions/run-e2e-lane/action.yml"))
			if err != nil {
				t.Fatal(err)
			}
			var action struct {
				Runs struct {
					Steps []struct{ Name, If, Run string }
				} `yaml:"runs"`
			}
			if err := yaml.Unmarshal(actionData, &action); err != nil {
				t.Fatal(err)
			}
			var script string
			for _, step := range action.Runs.Steps {
				if step.Name == "Summarize E2E report" && step.If == "always()" {
					script = step.Run
				}
			}
			if script != "bash hack/ci/summarize-e2e.sh" {
				t.Fatal("blocking scenario validation step is missing")
			}
			for _, state := range []string{"passed", "skipped", "missing scenario", "no report"} {
				t.Run(state, func(t *testing.T) {
					dir := t.TempDir()
					// The shared step executes the compiled reporter directly.
					binaryData, err := os.ReadFile(binary)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "e2e_report"), binaryData, 0o700); err != nil {
						t.Fatal(err)
					}
					report := selectionReport()
					report.SuiteConfig.LabelFilter = "test-lane"
					report.SpecReports[1].LeafNodeLabels = []string{"test-lane", "case:planned"}
					if state == "skipped" {
						report.SpecReports[1].State = types.SpecStateSkipped
					}
					if state == "missing scenario" {
						report.SpecReports = report.SpecReports[:1]
					}
					reportPath := filepath.Join(dir, "ginkgo.json")
					if state != "no report" {
						encoded, err := json.Marshal([]any{report})
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(reportPath, encoded, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					cmd := exec.Command("bash", "-ec", script)
					cmd.Dir = root
					cmd.Env = append(os.Environ(), "E2E_BINARY_DIR="+dir, "E2E_LANE_NAME=test-lane", "E2E_OPENBAO_IMAGE=test-image",
						"EXPECTED_CASE_IDS=[\"planned\"]", "E2E_LABEL_FILTER=test-lane", "KIND_NODE_IMAGE=kindest/node:test",
						"E2E_REPORT_DIR="+dir, "E2E_JSON_REPORT="+reportPath, "E2E_SUMMARY_REPORT="+filepath.Join(dir, "summary.md"),
						"E2E_SUMMARY_JSON="+filepath.Join(dir, "summary.json"), "GITHUB_STEP_SUMMARY="+filepath.Join(dir, "job.md"))
					output, err := cmd.CombinedOutput()
					if (err == nil) != (state == "passed") {
						t.Fatalf("validation for %s: %v\n%s", state, err, output)
					}
				})
			}
		})
	}
}
