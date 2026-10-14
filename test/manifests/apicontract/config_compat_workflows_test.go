package apicontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigCompatibilityChangeRouting(t *testing.T) {
	script := findStep(t, readWorkflow(t, "ci.yml").Jobs["changes"], "Detect chart changes").Run
	bin := configCompatibilityFakeGit(t)
	paths := []string{
		"hack/ci/openbao-config-compat.sh", "hack/ci/openbao-config-compat/manifest.json",
		"hack/ci/openbao-config-compat/testdata/unknown_raft.hcl",
		"hack/tools/openbao/config_compat/contract.go", "hack/tools/openbao/config_compat/harness.go.tmpl",
		"hack/tools/openbao/config_schema/main.go", "hack/tools/openbao/operator_schema_drift/main.go",
		"hack/tools/openbao/internal/configschema/schema.go", "internal/adapter/config/render_storage.go",
		"internal/port/openbao/tls.go", "api/v1alpha1/openbaocluster_configuration_types.go",
		".github/actions/run-openbao-config-compat/action.yml", ".github/actions/setup-repo-tools/action.yml",
		".github/workflows/ci.yml", ".github/workflows/nightly.yml", "mk/development.mk",
		"vendor/modules.txt", "test/manifests/apicontract/config_compat_workflows_test.go",
		"website/content/contribute/testing.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "outputs")
			env := configCompatibilityRoutingEnv(bin, outputPath)
			env = append(env, "CHANGED_PATHS="+path)
			output, err := runCommand(t, repositoryRoot(t), env, "bash", "-ec", script)
			if err != nil {
				t.Fatalf("change detector failed: %v\n%s", err, output)
			}
			data, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			want := "config_compat=true\n"
			if strings.HasPrefix(path, "website/") {
				want = "config_compat=false\n"
			}
			if !strings.Contains(string(data), want) {
				t.Fatalf("%s must route to %s; got\n%s", path, want, data)
			}
		})
	}
}

func TestChangeRoutingFailsClosedWhenGitCannotResolveDiff(t *testing.T) {
	script := findStep(t, readWorkflow(t, "ci.yml").Jobs["changes"], "Detect chart changes").Run
	for _, failure := range []string{"fetch", "diff"} {
		t.Run(failure, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "outputs")
			env := append(configCompatibilityRoutingEnv(configCompatibilityFakeGit(t), outputPath), "FAIL_GIT="+failure)
			output, err := runCommand(t, repositoryRoot(t), env, "bash", "-ec", script)
			if err == nil {
				t.Fatalf("failed git %s must stop routing, got %s", failure, output)
			}
			data, readErr := os.ReadFile(outputPath)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if strings.Contains(string(data), "config_compat=false") {
				t.Fatal("diff failure was reported as no changes")
			}
		})
	}
}

func TestConfigCompatibilityVersionMatrixComesFromManifest(t *testing.T) {
	for _, name := range []string{"ci.yml", "nightly.yml"} {
		w := readWorkflow(t, name)
		found := false
		for _, step := range w.Jobs["openbao-config-compat"].Steps {
			if step.Uses != "./.github/actions/run-openbao-config-compat" {
				continue
			}
			found = true
			if step.With["versions"] != "" {
				t.Fatalf("%s duplicates the manifest version matrix", name)
			}
			if step.With["operator-schema-drift-image-tag"] != "" {
				t.Fatalf("%s limits schema coverage to one release", name)
			}
		}
		if !found {
			t.Fatalf("%s does not run config compatibility", name)
		}
	}
}

func configCompatibilityFakeGit(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	script := "#!/usr/bin/env bash\n" +
		"if [[ \"${FAIL_GIT:-}\" == \"$1\" ]]; then exit 23; fi\n" +
		"case \"$1\" in fetch) exit 0 ;; diff) printf '%s\\n' \"${CHANGED_PATHS:-}\" ;; *) exit 1 ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func configCompatibilityRoutingEnv(bin, output string) []string {
	return []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"EVENT_NAME=pull_request", "GITHUB_SHA=head", "PR_BASE_SHA=base", "PR_HEAD_SHA=head", "GITHUB_OUTPUT=" + output}
}
