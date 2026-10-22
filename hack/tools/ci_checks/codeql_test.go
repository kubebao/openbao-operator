package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	codeQLRunner  = "ubuntu-24.04"
	codeQLSuccess = "success"
)

type codeQLStep struct {
	Uses string            `yaml:"uses"`
	If   string            `yaml:"if"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
	Env  map[string]string `yaml:"env"`
}

type codeQLJob struct {
	Name        string            `yaml:"name"`
	If          string            `yaml:"if"`
	Needs       []string          `yaml:"needs"`
	RunsOn      string            `yaml:"runs-on"`
	Environment string            `yaml:"environment"`
	Permissions map[string]string `yaml:"permissions"`
	Strategy    struct {
		FailFast *bool `yaml:"fail-fast"`
		Matrix   struct {
			Include []struct {
				Language  string `yaml:"language"`
				BuildMode string `yaml:"build-mode"`
			} `yaml:"include"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []codeQLStep `yaml:"steps"`
}

func readCodeQLWorkflow(t *testing.T) (map[string]yaml.Node, map[string]string, map[string]codeQLJob) {
	t.Helper()
	path := filepath.Join("..", "..", "..", ".github", "workflows", "codeql.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On          map[string]yaml.Node `yaml:"on"`
		Permissions map[string]string    `yaml:"permissions"`
		Jobs        map[string]codeQLJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secrets.") {
		t.Fatal("fork CodeQL execution must not receive repository secrets")
	}
	return workflow.On, workflow.Permissions, workflow.Jobs
}

func TestCodeQLCoversForkPullRequests(t *testing.T) {
	events, permissions, jobs := readCodeQLWorkflow(t)
	if !reflect.DeepEqual(permissions, map[string]string{"contents": "read"}) {
		t.Fatal("CodeQL workflow defaults must remain read-only")
	}
	for event := range events {
		switch event {
		case "pull_request", "push", "schedule", "workflow_dispatch":
		default:
			t.Fatalf("CodeQL must not execute fork code through %s", event)
		}
	}
	for _, event := range []string{"pull_request", "push"} {
		trigger, exists := events[event]
		if !exists {
			t.Fatalf("missing CodeQL trigger %s", event)
		}
		var config struct {
			Branches    []string `yaml:"branches"`
			Types       []string `yaml:"types"`
			Paths       []string `yaml:"paths"`
			PathsIgnore []string `yaml:"paths-ignore"`
		}
		if err := trigger.Decode(&config); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(config.Branches, []string{"main", "release-[0-9]*.[0-9]*"}) ||
			len(config.Paths) != 0 || len(config.PathsIgnore) != 0 {
			t.Fatalf("%s must scan main and release branches without path exclusions", event)
		}
		if event == "pull_request" && !reflect.DeepEqual(config.Types,
			[]string{"opened", "synchronize", "reopened", "ready_for_review"}) {
			t.Fatal("CodeQL must scan new and updated fork PRs, including when they become ready")
		}
	}
	analyze, exists := jobs["analyze"]
	if !exists || analyze.If != "" || analyze.RunsOn != codeQLRunner || analyze.Environment != "" {
		t.Fatal("CodeQL must analyze all PRs on hosted runners without a fork guard or environment")
	}
	if !reflect.DeepEqual(analyze.Permissions, map[string]string{"contents": "read", "security-events": "write"}) {
		t.Fatal("CodeQL may request only source read and SARIF upload permissions")
	}
	if analyze.Strategy.FailFast == nil || *analyze.Strategy.FailFast {
		t.Fatal("one failing language must not cancel the other analyses")
	}
	languages := make(map[string]string)
	for _, row := range analyze.Strategy.Matrix.Include {
		languages[row.Language] = row.BuildMode
	}
	if len(analyze.Strategy.Matrix.Include) != 4 || !reflect.DeepEqual(languages, map[string]string{
		"actions": "none", "go": "manual", "javascript-typescript": "none", "python": "none",
	}) {
		t.Fatal("CodeQL must retain all four language groups from default setup")
	}
}

func TestCodeQLBuildAndUploadPreserveForkIsolation(t *testing.T) {
	_, _, jobs := readCodeQLWorkflow(t)
	analyze := jobs["analyze"]
	pinnedAction := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	positions := make(map[string]int)
	steps := make(map[string]codeQLStep)
	for i, step := range analyze.Steps {
		if step.Uses == "" {
			if step.Run != "go build -mod=vendor ./..." || step.If != "matrix.language == 'go'" ||
				step.Env["CGO_ENABLED"] != "0" {
				t.Fatal("the manual Go build must use vendored packages without changing CodeQL tracing flags")
			}
			positions["build"] = i
			continue
		}
		if !pinnedAction.MatchString(step.Uses) {
			t.Fatalf("CodeQL action is not pinned to a full commit: %s", step.Uses)
		}
		name, _, _ := strings.Cut(step.Uses, "@")
		positions[name], steps[name] = i, step
		if name != "actions/setup-go" && step.If != "" {
			t.Fatalf("%s must not skip fork PRs", name)
		}
	}
	for _, action := range []string{
		"actions/checkout", "actions/setup-go", "github/codeql-action/init", "github/codeql-action/analyze",
	} {
		if _, exists := steps[action]; !exists {
			t.Fatalf("missing CodeQL action %s", action)
		}
	}
	checkout, setup, init, upload := steps["actions/checkout"], steps["actions/setup-go"],
		steps["github/codeql-action/init"], steps["github/codeql-action/analyze"]
	if checkout.With["persist-credentials"] != routingFalse || checkout.With["ref"] != "" {
		t.Fatal("CodeQL must scan the PR merge ref without persisted checkout credentials")
	}
	if setup.If != "matrix.language == 'go'" || setup.With["cache"] != routingFalse ||
		setup.With["go-version-file"] != "go.mod" {
		t.Fatal("Go analysis must use the module toolchain without persisting fork dependency caches")
	}
	if init.With["languages"] != "${{ matrix.language }}" || init.With["build-mode"] != "${{ matrix.build-mode }}" ||
		init.With["dependency-caching"] != routingFalse || init.With["token"] != "" {
		t.Fatal("CodeQL initialization must use the language matrix, disabled dependency caching, and the default token")
	}
	if upload.With["upload"] != "always" || upload.With["wait-for-processing"] != routingTrue ||
		upload.With["token"] != "" {
		t.Fatal("CodeQL must upload and process results with the default token")
	}
	order := []string{
		"actions/checkout", "actions/setup-go", "github/codeql-action/init", "build", "github/codeql-action/analyze",
	}
	for i := 1; i < len(order); i++ {
		if positions[order[i-1]] >= positions[order[i]] {
			t.Fatal("CodeQL must initialize tracing before the manual build and analyze after it")
		}
	}
}

func TestCodeQLGateRequiresSuccessfulAnalysis(t *testing.T) {
	_, _, jobs := readCodeQLWorkflow(t)
	gate, exists := jobs["codeql-required"]
	if !exists || gate.Name != "CodeQL Required" || gate.If != "always()" ||
		!reflect.DeepEqual(gate.Needs, []string{"analyze"}) || gate.RunsOn != codeQLRunner ||
		gate.Permissions == nil || len(gate.Permissions) != 0 || len(gate.Steps) != 1 {
		t.Fatal("CodeQL needs an always-running completion gate with no token permissions")
	}
	step := gate.Steps[0]
	if step.If != "" || step.Env["ANALYSIS_RESULT"] != "${{ needs.analyze.result }}" {
		t.Fatal("CodeQL completion gate must check the analysis result on every run")
	}
	for _, result := range []string{codeQLSuccess, "failure", "cancelled", "skipped", ""} {
		t.Run(result, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", step.Run)
			cmd.Env = append(os.Environ(), "ANALYSIS_RESULT="+result)
			output, err := cmd.CombinedOutput()
			if (err == nil) != (result == codeQLSuccess) {
				t.Fatalf("analysis result %q: error=%v, output=%s", result, err, output)
			}
		})
	}
}
