package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func commitFixture(t *testing.T, dir string) string {
	t.Helper()
	gitCommand(t, dir, "add", ".")
	gitCommand(t, dir, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
		"-c", "user.name=CI test", "-c", "user.email=ci@example.invalid", "commit", "--quiet", "-m", "fixture")
	return gitCommand(t, dir, "rev-parse", "HEAD")
}

func runRouting(t *testing.T, base, head, failure, event string) (map[string]string, error) {
	t.Helper()
	dir := t.TempDir()
	gitCommand(t, dir, "init", "--quiet")
	writeFile(t, filepath.Join(dir, "README.md"), "base\n")
	if base == "rename" {
		writeFile(t, filepath.Join(dir, "internal/service/upgrade/old.go"), "package upgrade\n")
	}
	baseSHA := commitFixture(t, dir)
	if head != "" {
		if base == "rename" {
			if err := os.Remove(filepath.Join(dir, "internal/service/upgrade/old.go")); err != nil {
				t.Fatal(err)
			}
		}
		writeFile(t, filepath.Join(dir, head), "head\n")
		commitFixture(t, dir)
	}
	headSHA := gitCommand(t, dir, "rev-parse", "HEAD")
	for _, name := range []string{
		"e2e-lifecycle-paths.txt", "e2e-seal-paths.txt", "e2e-execution-paths.txt", "e2e-selection-paths.txt",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "hack", "ci", name))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "hack/ci", name), string(data))
	}
	if base == "missing" {
		baseSHA = strings.Repeat("f", 40)
	}
	if base == "initial push" {
		baseSHA = strings.Repeat("0", 40)
	}
	outputPath := filepath.Join(t.TempDir(), "outputs")
	writeFile(t, outputPath, "")
	env := append(os.Environ(), "EVENT_NAME="+event, "GITHUB_SHA="+headSHA, "PR_BASE_SHA="+baseSHA,
		"PR_HEAD_SHA="+headSHA, "PUSH_BEFORE_SHA="+baseSHA, "GITHUB_OUTPUT="+outputPath)
	for _, key := range routingKeys {
		if strings.HasPrefix(key, "manual_") {
			env = append(env, strings.ToUpper(key)+"=false")
		}
	}
	if failure != "" {
		binDir := t.TempDir()
		gitPath, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		wrapper := "#!/bin/bash\nif [[ $1 == " + failure + " ]]; then exit 128; fi\nexec '" + gitPath + "' \"$@\"\n"
		path := filepath.Join(binDir, "git")
		writeFile(t, path, wrapper)
		if err := os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
		env = append(env, "PATH="+binDir+":"+os.Getenv("PATH"))
	}
	var script string
	for _, step := range readWorkflow(t)["changes"].Steps {
		if step.ID == "diff" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("missing change-detection script")
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	cmd.Env = env
	log, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("routing failed as expected: %s", log)
	}
	data, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	outputs := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			outputs[key] = value
		}
	}
	return outputs, err
}

func TestChangeDetectionFailsClosed(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"diff", "cat-file"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			if _, err := runRouting(t, "", "internal/service/backup/new.go", failure, eventPullRequest); err == nil {
				t.Fatal("failed Git command accepted")
			}
		})
	}
	if _, err := runRouting(t, "missing", "", "", eventPullRequest); err == nil {
		t.Fatal("unavailable base accepted")
	}
}

func TestChangeDetectionRoutesRealGitDiffs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, base, path, event string
		routes                  []string
	}{
		{"empty diff", "", "", eventPullRequest, nil},
		{"docs", "", "website/content/example.md", eventPullRequest, []string{"docs"}},
		{"scripts", "", ".github/scripts/example.go", eventPullRequest, []string{"workflow"}},
		{"renamed upgrade", "rename", "website/old.go", eventPullRequest, []string{"e2e_upgrade"}},
		{"initial push", "initial push", "", eventPush, []string{"docs"}},
		{"existing base ignores failed fetch", "", "README.md", eventPullRequest, []string{"docs"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			failure := ""
			if test.name == "existing base ignores failed fetch" {
				failure = "fetch"
			}
			outputs, err := runRouting(t, test.base, test.path, failure, test.event)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range routingKeys {
				if outputs[key] != routingTrue && outputs[key] != routingFalse {
					t.Errorf("missing boolean %s", key)
				}
				if test.name == "empty diff" && outputs[key] != routingFalse {
					t.Errorf("empty diff selected %s", key)
				}
			}
			for _, key := range test.routes {
				if outputs[key] != routingTrue {
					t.Errorf("%s not selected", key)
				}
			}
		})
	}
}

func TestSharedExecutionChangesSelectEveryLaneFamily(t *testing.T) {
	for _, path := range []string{
		".github/actions/run-e2e-lane/action.yml",
		".github/actions/prepare-e2e-lane/action.yml",
		".github/actions/load-e2e-image-artifacts/action.yml",
		".github/actions/setup-repo-tools/action.yml",
		"hack/ci/e2e-images.py",
		"hack/ci/e2e_artifacts.py",
		"hack/ci/build-e2e-binaries.sh",
		"hack/ci/configure-e2e-lane.py",
		"hack/ci/e2e-binaries.py",
		"hack/ci/run-e2e.sh",
		"hack/ci/summarize-e2e.sh",
		"hack/ci/e2e-execution-paths.txt",
		"hack/tools/e2e_report/main.go",
	} {
		t.Run(path, func(t *testing.T) {
			outputs, err := runRouting(t, "normal", path, "", eventPullRequest)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"workflow", "e2e", "e2e_backup", "e2e_upgrade", "e2e_hardened", "e2e_seal"} {
				if outputs[key] != routingTrue {
					t.Fatalf("shared execution change %s does not select %s", path, key)
				}
			}
		})
	}
}

func TestSecurityPolicyChangesRequireSecurityChecks(t *testing.T) {
	t.Parallel()
	const (
		jobSuccess = "success"
		jobSkipped = "skipped"
	)
	for _, path := range []string{
		".semgrep/rules/example.yaml",
		".semgrep/tests/example.go",
		".semgrepignore",
		".govulnignore",
		".trivyignore",
		".trivy.yaml",
		".github/workflows/example.yml",
		".github/actions/example/action.yml",
		".github/scripts/example.go",
		".github/dependabot.yml",
		".github/dependency-review-config.yml",
		"test/e2e/images/pykmip-server/requirements.txt",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			routing, err := runRouting(t, "normal", path, "", eventPullRequest)
			if err != nil {
				t.Fatal(err)
			}
			for _, head := range []string{"owner/repo", "fork/repo"} {
				plan, err := buildPlan(eventPullRequest, "refs/pull/1/merge", "owner/repo", "sha",
					prEvent(head), routing)
				if err != nil {
					t.Fatal(err)
				}
				if !plan.Checks["security"] {
					t.Fatalf("policy change %s from %s does not require security checks", path, head)
				}
				results := make(map[string]jobResult, len(plan.Checks))
				for job, required := range plan.Checks {
					result := jobSkipped
					if required {
						result = jobSuccess
					}
					results[job] = jobResult{Result: result}
				}
				if err := validateResults(plan, "sha", results); err != nil {
					t.Fatal(err)
				}
				results["security"] = jobResult{Result: jobSkipped}
				if err := validateResults(plan, "sha", results); err == nil || !strings.Contains(err.Error(), "security:") {
					t.Fatalf("skipped security checks accepted for %s from %s: %v", path, head, err)
				}
			}
		})
	}
}

func lanePlanner(t *testing.T) func(checkPlan) []string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	planner := filepath.Join(t.TempDir(), "e2e_plan")
	cmd := exec.Command("go", "build", "-mod=vendor", "-o", planner, "./hack/tools/e2e_plan")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build E2E planner: %v\n%s", err, output)
	}
	return func(plan checkPlan) []string {
		t.Helper()
		planPath := filepath.Join(t.TempDir(), "plan.json")
		if err := writeJSON(planPath, plan); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(planner, "--ci-plan", planPath, "--sha", "tested-sha")
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("plan E2E lanes: %v\n%s", err, output)
		}
		var matrix struct {
			Include []struct {
				ID string `json:"id"`
			} `json:"include"`
		}
		if err := json.Unmarshal(output, &matrix); err != nil {
			t.Fatal(err)
		}
		lanes := make([]string, 0, len(matrix.Include))
		for _, row := range matrix.Include {
			lanes = append(lanes, row.ID)
		}
		return lanes
	}
}

func TestSharedSelectionChangesSelectEveryLaneFamily(t *testing.T) {
	plannedLanes := lanePlanner(t)
	for _, path := range []string{
		"hack/ci/create-network-kind.sh",
		"hack/ci/delete-network-kind.sh",
		"hack/tools/e2e_plan/main.go",
		"hack/tools/e2e_plan/selection.go",
		"hack/tools/e2e_plan/selection_test.go",
		"hack/tools/e2e_manifest/main.go",
		"hack/tools/e2e_report/main.go",
		"hack/tools/e2e_report/selection.go",
		"hack/tools/e2e_report/selection_test.go",
		"test/e2e/suites.yaml",
		"hack/ci/e2e-selection-paths.txt",
	} {
		t.Run(path, func(t *testing.T) {
			routing, err := runRouting(t, "normal", path, "", eventPullRequest)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"workflow", "e2e", "e2e_backup", "e2e_upgrade", "e2e_hardened", "e2e_seal"} {
				if routing[key] != routingTrue {
					t.Errorf("shared selection change %s does not select %s", path, key)
				}
			}
			plan, err := buildPlan(eventPullRequest, "refs/pull/1/merge", "owner/repo", "tested-sha",
				prEvent("owner/repo"), routing)
			if err != nil {
				t.Fatal(err)
			}
			if plan.E2E.Full || plan.E2E.IncludePentest {
				t.Fatal("shared selection paths must retain the default PR selector and pentest policy")
			}
			lanes := plannedLanes(plan)
			want := "core,controller-jwt,security,backup-restore,restore-tests,upgrade-rolling,upgrade-bluegreen-core," +
				"upgrade-bluegreen-verification,upgrade-bluegreen-recovery,upgrade-gateway,hardened,hsm,networking"
			if strings.Join(lanes, ",") != want {
				t.Fatalf("shared selection change planned %v, want all 13 automatic lanes", lanes)
			}
			forkPlan, err := buildPlan(eventPullRequest, "refs/pull/1/merge", "owner/repo", "tested-sha",
				prEvent("fork/repo"), routing)
			if err != nil {
				t.Fatal(err)
			}
			for _, job := range []string{"build-e2e-images", "operator-upgrade-e2e", "e2e"} {
				if forkPlan.Checks[job] {
					t.Fatalf("shared selection change must not select %s on a fork PR", job)
				}
			}
		})
	}
}

func TestFixtureChangesSelectOwningLaneFamily(t *testing.T) {
	const upgradeLanes = "core,controller-jwt,security,upgrade-rolling,upgrade-bluegreen-core," +
		"upgrade-bluegreen-verification,upgrade-bluegreen-recovery,upgrade-gateway,networking"
	plannedLanes := lanePlanner(t)
	for _, test := range []struct {
		path, lanes string
	}{
		{"test/e2e/restore_minimal_helpers.go", "core,controller-jwt,security,backup-restore,restore-tests,networking"},
		{"test/e2e/backup_observation.go", "core,controller-jwt,security,backup-restore,restore-tests,networking"},
		{"test/e2e/Upgrade_Gateway_test.go", upgradeLanes},
		{"test/e2e/upgrade_fixture.go", upgradeLanes},
	} {
		t.Run(test.path, func(t *testing.T) {
			routing, err := runRouting(t, "normal", test.path, "", eventPullRequest)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := buildPlan(eventPullRequest, "refs/pull/1/merge", "owner/repo", "tested-sha",
				prEvent("owner/repo"), routing)
			if err != nil {
				t.Fatal(err)
			}
			if plan.E2E.Full || plan.E2E.IncludePentest {
				t.Fatal("fixture changes must retain the default PR selector and pentest policy")
			}
			if lanes := plannedLanes(plan); strings.Join(lanes, ",") != test.lanes {
				t.Fatalf("fixture change planned %v, want %s", lanes, test.lanes)
			}
		})
	}
}

func TestFixtureContractChangesSelectSupportTestsOnTrustedAndForkPRs(t *testing.T) {
	for _, path := range []string{
		"test/e2e/fixture_contract_test.go",
		"test/e2e/backup_observation_contract_test.go",
	} {
		t.Run(path, func(t *testing.T) {
			routing, err := runRouting(t, "normal", path, "", eventPullRequest)
			if err != nil {
				t.Fatal(err)
			}
			for _, head := range []string{"owner/repo", "fork/repo"} {
				t.Run(head, func(t *testing.T) {
					plan, err := buildPlan(eventPullRequest, "refs/pull/1/merge", "owner/repo", "tested-sha",
						prEvent(head), routing)
					if err != nil {
						t.Fatal(err)
					}
					for _, job := range []string{"test-unit", "test-integration"} {
						if !plan.Checks[job] {
							t.Errorf("contract test change must select %s", job)
						}
					}
					if head == "fork/repo" {
						for _, job := range []string{"build-artifacts", "build-e2e-image-artifacts", "e2e-matrix",
							"fork-e2e", "fork-operator-upgrade-e2e"} {
							if !plan.Checks[job] {
								t.Errorf("contract test change must select fork qualification %s", job)
							}
						}
						for _, job := range []string{"build-e2e-images", "operator-upgrade-e2e", "e2e"} {
							if plan.Checks[job] {
								t.Errorf("contract test change must not select %s on a fork PR", job)
							}
						}
					}
				})
			}
		})
	}
}
