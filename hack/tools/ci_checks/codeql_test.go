package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	codeQLRunner  = "ubuntu-24.04"
	codeQLSuccess = "success"
)

type codeQLStep struct {
	Name           string            `yaml:"name"`
	ID             string            `yaml:"id"`
	TimeoutMinutes int               `yaml:"timeout-minutes"`
	Uses           string            `yaml:"uses"`
	If             string            `yaml:"if"`
	Run            string            `yaml:"run"`
	With           map[string]string `yaml:"with"`
	Env            map[string]string `yaml:"env"`
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
		if step.Name == "Require processed results" {
			positions["processing"], steps["processing"] = i, step
			continue
		}
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
	if upload.ID != "analyze" || upload.With["upload"] != "always" || upload.With["wait-for-processing"] != routingTrue ||
		upload.With["token"] != "" {
		t.Fatal("CodeQL must upload and process results with the default token")
	}
	order := []string{
		"actions/checkout", "actions/setup-go", "github/codeql-action/init", "build", "github/codeql-action/analyze",
		"processing",
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

func TestCodeQLProcessingPermissions(t *testing.T) {
	processing := codeQLProcessingStep(t)
	if processing.Uses != "" || processing.Run == "" || processing.If != "" || processing.TimeoutMinutes != 6 ||
		!reflect.DeepEqual(processing.Env, map[string]string{
			"GH_TOKEN": "${{ github.token }}", "SARIF_ID": "${{ steps.analyze.outputs.sarif-id }}",
		}) {
		t.Fatal("CodeQL must independently require processed SARIF results with the default token and a bounded wait")
	}
}

func TestCodeQLRequiresProcessedSARIF(t *testing.T) {
	script := codeQLProcessingStep(t).Run
	for _, tt := range []struct {
		name    string
		id      string
		states  string
		success bool
		calls   int
	}{
		{name: "complete", id: "test-upload", states: "complete", success: true, calls: 1},
		{name: "pending then complete", id: "test-upload", states: "pending,complete", success: true, calls: 2},
		{name: "missing upload ID", states: "complete"},
		{name: "failed processing", id: "test-upload", states: "failed", calls: 1},
		{name: "unknown status", id: "test-upload", states: "unknown", calls: 1},
		{name: "missing status", id: "test-upload", states: "null", calls: 1},
		{name: "API error", id: "test-upload", states: "API_ERROR", calls: 1},
		{name: "API error after pending", id: "test-upload", states: "pending,API_ERROR", calls: 2},
		{name: "processing timeout", id: "test-upload", states: "pending", calls: 30},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := codeQLMockCommands(t)
			callsFile := filepath.Join(dir, "calls")
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(),
				"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GH_TOKEN=test-token", "GITHUB_REPOSITORY=kubebao/openbao-operator", "SARIF_ID="+tt.id,
				"CODEQL_TEST_STATES="+tt.states, "CODEQL_TEST_CALLS="+callsFile,
			)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tt.success {
				t.Fatalf("unexpected processing result: error=%v, output=%s", err, output)
			}
			if calls := codeQLReadCalls(t, callsFile); calls != tt.calls {
				t.Fatalf("processing calls: got %d, want %d", calls, tt.calls)
			}
		})
	}
}

func codeQLReadCalls(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	calls, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func codeQLMockCommands(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	api := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" != "api repos/kubebao/openbao-operator/code-scanning/sarifs/test-upload --jq .processing_status" ]]; then
  echo "unexpected API arguments" >&2
  exit 64
fi
calls=0
if [[ -f "${CODEQL_TEST_CALLS}" ]]; then
  read -r calls < "${CODEQL_TEST_CALLS}"
fi
calls=$((calls + 1))
printf '%s\n' "${calls}" > "${CODEQL_TEST_CALLS}"
IFS=',' read -r -a states <<< "${CODEQL_TEST_STATES}"
index=$((calls - 1))
if (( index >= ${#states[@]} )); then
  index=$((${#states[@]} - 1))
fi
if [[ "${states[index]}" == "API_ERROR" ]]; then
  exit 42
fi
printf '%s\n' "${states[index]}"
`
	for name, script := range map[string]string{"gh": api, "sleep": "#!/usr/bin/env bash\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func codeQLProcessingStep(t *testing.T) codeQLStep {
	t.Helper()
	_, _, jobs := readCodeQLWorkflow(t)
	for _, step := range jobs["analyze"].Steps {
		if step.Name == "Require processed results" {
			return step
		}
	}
	t.Fatal("missing strict SARIF processing check")
	return codeQLStep{}
}
