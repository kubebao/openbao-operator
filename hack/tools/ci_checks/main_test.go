package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func emptyRouting() map[string]string {
	routing := make(map[string]string, len(routingKeys))
	for _, key := range routingKeys {
		routing[key] = routingFalse
	}
	return routing
}

func prEvent(repository string, labels ...string) eventPayload {
	var event eventPayload
	event.PullRequest.Head.Repo.FullName = repository
	for _, label := range labels {
		event.PullRequest.Labels = append(event.PullRequest.Labels, struct {
			Name string `json:"name"`
		}{label})
	}
	return event
}

func TestBuildPlanEventAndTrustPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, event, head  string
		labels, routes     []string
		required, excluded []string
	}{
		{"docs PR", eventPullRequest, "owner/repo", nil, []string{"docs"},
			[]string{changesJob, "docs"}, []string{"e2e", "test-unit", "api-contract"}},
		{"contract PR", eventPullRequest, "owner/repo", nil, []string{"api_contract"},
			[]string{changesJob, "api-contract"}, []string{"e2e", "test-unit"}},
		{"main push", eventPush, "", nil, nil,
			[]string{"e2e", "test-unit", "security", "api-contract", "operator-upgrade-e2e"}, []string{"helm-e2e-smoke"}},
		{"ordinary manual run", eventWorkflowDispatch, "", nil, nil,
			[]string{changesJob, "api-contract"}, []string{"e2e", "test-unit"}},
		{"manual backup", eventWorkflowDispatch, "", nil, []string{"manual_e2e_requested", "manual_backup_e2e"},
			[]string{"e2e", "build-e2e-images", "operator-upgrade-e2e", "api-contract"}, []string{"test-unit"}},
		{"same repo source", eventPullRequest, "owner/repo", nil, []string{"e2e", "go_tests"},
			[]string{"e2e", "test-unit", "test-integration"}, []string{"docs"}},
		{"fork source", eventPullRequest, "fork/repo", []string{"ci:full-e2e"}, []string{"e2e", "go_tests"},
			[]string{"test-unit", "test-integration", "build-artifacts", "build-e2e-image-artifacts",
				"fork-e2e", "e2e-matrix", "fork-operator-upgrade-e2e"},
			[]string{"e2e", "build-e2e-images", "operator-upgrade-e2e"}},
		{"label requested backup", eventPullRequest, "owner/repo", []string{"backup"}, nil,
			[]string{"e2e", "e2e-matrix", "build-artifacts"}, []string{"test-unit"}},
		{"operator upgrade only", eventPullRequest, "owner/repo", nil, []string{"operator_upgrade_e2e"},
			[]string{"build-artifacts", "build-e2e-images", "operator-upgrade-e2e"}, []string{"e2e", "e2e-matrix"}},
		{"chart", eventPullRequest, "owner/repo", nil, []string{"chart"},
			[]string{"helm", "helm-e2e-smoke"}, []string{"e2e"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			routing := emptyRouting()
			for _, key := range test.routes {
				routing[key] = routingTrue
			}
			plan, err := buildPlan(test.event, "refs/heads/main", "owner/repo", "sha",
				prEvent(test.head, test.labels...), routing)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range test.required {
				if !plan.Checks[key] {
					t.Errorf("%s must run", key)
				}
			}
			for _, key := range test.excluded {
				if plan.Checks[key] {
					t.Errorf("%s must be excluded", key)
				}
			}
			if test.head == "fork/repo" && plan.Reason != "" {
				t.Fatal("selected fork E2E is reported as excluded")
			}
		})
	}
}

func TestBuildPlanRejectsIncompleteRouting(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "TRUE", "skipped"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			routing := emptyRouting()
			routing["go_tests"] = value
			if _, err := buildPlan(eventPush, "refs/heads/main", "owner/repo", "sha", eventPayload{}, routing); err == nil {
				t.Fatal("invalid output accepted")
			}
		})
	}
	if _, err := buildPlan(eventPullRequest, "ref", "owner/repo", "sha", eventPayload{}, emptyRouting()); err == nil {
		t.Fatal("missing PR head repository accepted")
	}
	routing := emptyRouting()
	routing["manual_e2e_requested"] = routingTrue
	if _, err := buildPlan(eventWorkflowDispatch, "ref", "owner/repo", "sha", eventPayload{}, routing); err == nil {
		t.Fatal("manual request without selected suites accepted")
	}
	routing["manual_full_e2e"] = routingTrue
	if _, err := buildPlan(eventPullRequest, "ref", "owner/repo", "sha", prEvent("fork/repo"), routing); err == nil {
		t.Fatal("manual inputs on a fork PR accepted")
	}
}

func TestValidateResults(t *testing.T) {
	t.Parallel()
	plan := checkPlan{Version: planVersion, SHA: "sha",
		Checks: map[string]bool{changesJob: true, "unit": true, "docs": false}}
	tests := []struct {
		name, unit, docs                    string
		missing, extra, wrongSHA, wantError bool
	}{
		{"expected success and exclusion", "success", "skipped", false, false, false, false},
		{"extra successful coverage", "success", "success", false, false, false, false},
		{"unexpected skip", "skipped", "skipped", false, false, false, true},
		{"required failure", "failure", "skipped", false, false, false, true},
		{"cancelled required", "cancelled", "skipped", false, false, false, true},
		{"failed extra job", "success", "failure", false, false, false, true},
		{"missing result", "success", "skipped", true, false, false, true},
		{"unplanned dependency", "success", "skipped", false, true, false, true},
		{"wrong source", "success", "skipped", false, false, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			results := map[string]jobResult{changesJob: {"success"}, "unit": {test.unit}, "docs": {test.docs}}
			if test.missing {
				delete(results, "unit")
			}
			if test.extra {
				results["new-job"] = jobResult{"success"}
			}
			sha := "sha"
			if test.wrongSHA {
				sha = "other"
			}
			if err := validateResults(plan, sha, results); (err != nil) != test.wantError {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if err := validateResults(checkPlan{}, "sha", nil); err == nil {
		t.Fatal("empty plan accepted")
	}
}

type workflowJob struct {
	If    string   `yaml:"if"`
	Needs []string `yaml:"needs"`
	Steps []struct {
		ID  string `yaml:"id"`
		Run string `yaml:"run"`
	} `yaml:"steps"`
}

func readWorkflow(t *testing.T) map[string]workflowJob {
	t.Helper()
	// Only parse the jobs used by this contract; other jobs can use scalar needs.
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	jobs := make(map[string]workflowJob)
	for name, node := range workflow.Jobs {
		if name != "ci-required" && name != changesJob {
			continue
		}
		var job workflowJob
		if err := node.Decode(&job); err != nil {
			t.Fatal(err)
		}
		jobs[name] = job
	}
	return jobs
}

func TestWorkflowUsesRecordedExpectations(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If string `yaml:"if"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	plan, err := buildPlan(eventPush, "refs/heads/main", "owner/repo", "sha", eventPayload{}, emptyRouting())
	if err != nil {
		t.Fatal(err)
	}
	dependencies := readWorkflow(t)["ci-required"].Needs
	if len(dependencies) != len(plan.Checks) {
		t.Fatalf("%d dependencies, %d expectations", len(dependencies), len(plan.Checks))
	}
	for _, job := range dependencies {
		if _, ok := plan.Checks[job]; !ok {
			t.Fatalf("%s lacks an expectation", job)
		}
		if job == changesJob {
			continue
		}
		want := "fromJSON(needs.changes.outputs.expected_checks)['" + job + "']"
		if job == "build-e2e-images" {
			want += " && (github.event_name != 'pull_request' || " +
				"github.event.pull_request.head.repo.full_name == github.repository)"
		}
		if job == "build-e2e-image-artifacts" {
			want += " && github.event_name == 'pull_request' && " +
				"github.event.pull_request.head.repo.full_name != github.repository"
		}
		if workflow.Jobs[job].If != want {
			t.Errorf("%s condition = %q, want %q", job, workflow.Jobs[job].If, want)
		}
	}
}

func TestCLIPlanAndCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paths := map[string]string{}
	for name, value := range map[string]any{"event": eventPayload{}, "routing": emptyRouting(), "output": ""} {
		path := filepath.Join(dir, name+".json")
		paths[name] = path
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths["output"], nil, 0600); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "plan.json")
	if err := run([]string{"plan", "--event", paths["event"], "--routing", paths["routing"],
		"--event-name", eventWorkflowDispatch, "--repository", "owner/repo", "--sha", "sha",
		"--plan", planPath, "--github-output", paths["output"]}); err != nil {
		t.Fatal(err)
	}
	var plan checkPlan
	if err := readJSON(planPath, &plan); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(paths["output"])
	if err != nil {
		t.Fatal(err)
	}
	key, value, ok := strings.Cut(strings.TrimSpace(string(output)), "=")
	if !ok || key != "expected_checks" {
		t.Fatal("missing GitHub output")
	}
	var checks map[string]bool
	if err := json.Unmarshal([]byte(value), &checks); err != nil {
		t.Fatal(err)
	}
	if !checks["api-contract"] || checks["e2e"] {
		t.Fatal("incorrect workflow output expectations")
	}
	results := make(map[string]jobResult)
	for name, required := range plan.Checks {
		state := "skipped"
		if required {
			state = "success"
		}
		results[name] = jobResult{state}
	}
	needsPath := filepath.Join(dir, "needs.json")
	if err := writeJSON(needsPath, results); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"check", "--sha", "sha", "--plan", planPath, "--needs", needsPath}); err != nil {
		t.Fatal(err)
	}
	results["api-contract"] = jobResult{"skipped"}
	if err := writeJSON(needsPath, results); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"check", "--sha", "sha", "--plan", planPath, "--needs", needsPath}); err == nil {
		t.Fatal("CLI accepted required skip")
	}
}

func TestWorkflowArtifactAttemptScope(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
				With struct {
					Name         string `yaml:"name"`
					Pattern      string `yaml:"pattern"`
					ArtifactName string `yaml:"artifact-name"`
					Overwrite    bool   `yaml:"overwrite"`
				} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for job, name := range map[string]string{
		changesJob:       "Upload routing record and CI tools",
		"ci-required":    "Download routing record and CI tools",
		"ci-measurement": "Download routing record and CI tools",
	} {
		found := false
		for _, step := range workflow.Jobs[job].Steps {
			if step.Name != name {
				continue
			}
			found = true
			if step.With.Name != "ci-routing" {
				t.Errorf("%s cannot reuse the routing record on a failed-job rerun", job)
			}
			if job == changesJob && !step.With.Overwrite {
				t.Error("full rerun cannot replace the routing record")
			}
		}
		if !found {
			t.Fatalf("missing %s step", job)
		}
	}
	for _, step := range workflow.Jobs["ci-measurement"].Steps {
		if step.Name == "Download E2E reports" && step.With.Pattern != "e2e-artifacts-*-${{ github.run_attempt }}" {
			t.Error("measurement can mix E2E reports from different attempts")
		}
	}
	for _, step := range workflow.Jobs["e2e"].Steps {
		if step.Name == "Run planned E2E lane" && !strings.Contains(step.With.ArtifactName, "${{ github.run_attempt }}") {
			t.Error("E2E reports lack an attempt identity")
		}
	}
}

func TestForkPlansRequireExactlyOneUnprivilegedTransport(t *testing.T) {
	for _, head := range []string{"owner/repo", "fork/repo"} {
		for _, label := range []string{"", "ci:full-e2e", "backup", "upgrades", "security"} {
			t.Run(head+"/"+label, func(t *testing.T) {
				routing := emptyRouting()
				routing["e2e"] = routingTrue
				plan, err := buildPlan(eventPullRequest, "refs/pull/1/merge", "owner/repo", "sha",
					prEvent(head, label), routing)
				if err != nil {
					t.Fatal(err)
				}
				fork := head != "owner/repo"
				for _, pair := range [][2]string{
					{"build-e2e-images", "build-e2e-image-artifacts"},
					{"e2e", "fork-e2e"},
					{"operator-upgrade-e2e", "fork-operator-upgrade-e2e"},
				} {
					if plan.Checks[pair[0]] != !fork || plan.Checks[pair[1]] != fork {
						t.Fatalf("transport pair %v must be exclusive: %v", pair, plan.Checks)
					}
				}
				results := make(map[string]jobResult, len(plan.Checks))
				for job, required := range plan.Checks {
					result := "skipped"
					if required {
						result = "success"
					}
					results[job] = jobResult{result}
				}
				if err := validateResults(plan, "sha", results); err != nil {
					t.Fatal(err)
				}
				if fork {
					for _, job := range []string{"build-e2e-image-artifacts", "fork-e2e", "fork-operator-upgrade-e2e"} {
						results[job] = jobResult{"skipped"}
						if validateResults(plan, "sha", results) == nil {
							t.Fatalf("missing fork qualification %s accepted", job)
						}
						results[job] = jobResult{"success"}
					}
				}
			})
		}
	}
}
