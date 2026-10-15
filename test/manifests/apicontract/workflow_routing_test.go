package apicontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowChangeRouting(t *testing.T) {
	w := readWorkflow(t, "ci.yml")
	script := findStep(t, w.Jobs["changes"], "Detect chart changes").Run
	bin := t.TempDir()
	git := "#!/usr/bin/env bash\ncase \"$1\" in\n" +
		"fetch) exit 0 ;;\ndiff) printf '%s\\n' \"${CHANGED_PATHS}\" ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want bool
	}{
		{"hack/ci/release-please/package.json", true},
		{"hack/ci/release-please/package-lock.json", true},
		{"hack/ci/run-release-please-exact.js", true},
		{"hack/ci/test-release-automation.sh", true},
		{"hack/ci/validate-release-as-request.sh", true},
		{"hack/ci/resolve-release-as-override.sh", true},
		{"hack/ci/prepare-release-chart.sh", true},
		{"hack/ci/normalize-spdx-json.sh", true},
		{"hack/ci/test-remove-unpublished-release.sh", true},
		{"hack/ci/verify-post-release.sh", true},
		{"hack/ci/test-post-release.py", true},
		{".github/workflows/release-please.yml", true},
		{".github/actions/setup-repo-tools/action.yml", true},
		{"hack/dev/tool-versions.env", true},
		{"internal/adapter/config/builder.go", false},
		{"website/content/contribute/testing.md", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "outputs")
			env := []string{
				"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"EVENT_NAME=pull_request", "GITHUB_SHA=head", "PR_BASE_SHA=base", "PR_HEAD_SHA=head",
				"GITHUB_OUTPUT=" + outputPath, "CHANGED_PATHS=" + tc.path,
			}
			output, err := runCommand(t, repositoryRoot(t), env, "bash", "-ec", script)
			if err != nil {
				t.Fatalf("run change detector: %v\n%s", err, output)
			}
			data, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			want := "workflow=false\n"
			if tc.want {
				want = "workflow=true\n"
			}
			if !strings.Contains(string(data), want) {
				t.Fatalf("routing for %s: want %s, got\n%s", tc.path, want, data)
			}
		})
	}
}
