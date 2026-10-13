package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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
			var script string
			for _, step := range workflow.Jobs["e2e"].Steps {
				if step.Name != "Summarize E2E report" {
					continue
				}
				if step.If != "always()" || step.Env["EXPECTED_CASE_IDS"] != "${{ toJSON(matrix.expected_case_ids) }}" {
					t.Fatal("every planned lane must validate its recorded scenario IDs, including after failure")
				}
				script = regexp.MustCompile(`\$\{\{.*?\}\}`).ReplaceAllString(step.Run, "test-lane")
			}
			if script == "" || !strings.Contains(script, "--expected-cases") {
				t.Fatal("blocking scenario validation step is missing")
			}
			for _, state := range []string{"passed", "skipped", "missing scenario", "no report"} {
				t.Run(state, func(t *testing.T) {
					dir := t.TempDir()
					bin := filepath.Join(dir, "go")
					// Intercept only the workflow's go run invocation; execute
					// the real compiled reporter with its original arguments.
					stub := "#!/usr/bin/env bash\nshift 2\ncd '" + root + "'\nexec '" + binary + "' \"$@\"\n"
					if err := os.WriteFile(bin, []byte(stub), 0o700); err != nil {
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
					cmd.Dir = dir
					cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
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
