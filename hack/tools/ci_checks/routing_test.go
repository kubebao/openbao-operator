package main

import (
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
	for _, name := range []string{"e2e-lifecycle-paths.txt", "e2e-seal-paths.txt"} {
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
