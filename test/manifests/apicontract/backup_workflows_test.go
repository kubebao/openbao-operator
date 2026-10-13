package apicontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupDependencyChangeRouting(t *testing.T) {
	w := readWorkflow(t, "ci.yml")
	script := findStep(t, w.Jobs["changes"], "Detect chart changes").Run
	bin := t.TempDir()
	git := "#!/usr/bin/env bash\ncase \"$1\" in\n" +
		"fetch|cat-file) exit 0 ;;\ndiff) printf '%s\\n' \"${CHANGED_PATHS}\" ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want bool
	}{
		{"go.mod", true},
		{"go.sum", true},
		{"vendor/modules.txt", true},
		{"vendor/github.com/aws/aws-sdk-go-v2/service/s3/api_op_PutObject.go", true},
		{"vendor/cloud.google.com/go/storage/writer.go", true},
		{"vendor/github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob/client.go", true},
		{"internal/adapter/storage/gcs.go", true},
		{"internal/adapter/storageenv/credentials.go", true},
		{"internal/service/backup/manager.go", true},
		{"internal/service/restore/manager.go", true},
		{"cmd/bao-backup/main.go", true},
		{"Dockerfile.backup", true},
		{"hack/ci/release-please/package-lock.json", false},
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
			want := "e2e_backup=false\n"
			if tc.want {
				want = "e2e_backup=true\n"
				if !strings.Contains(string(data), "e2e=true\n") {
					t.Fatalf("backup changes must schedule the E2E matrix: %s", data)
				}
			}
			if !strings.Contains(string(data), want) {
				t.Fatalf("routing for %s: want %s, got\n%s", tc.path, want, data)
			}
		})
	}
}
