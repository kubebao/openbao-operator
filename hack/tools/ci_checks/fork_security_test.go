package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestForkWorkflowKeepsExecutionUnprivileged(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		Uses string            `yaml:"uses"`
		With map[string]string `yaml:"with"`
	}
	var workflow struct {
		On          map[string]yaml.Node `yaml:"on"`
		Permissions map[string]string    `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			RunsOn      string            `yaml:"runs-on"`
			Environment string            `yaml:"environment"`
			Steps       []step            `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"pull_request_target", "workflow_run", "issue_comment"} {
		if _, exists := workflow.On[event]; exists {
			t.Fatalf("untrusted test execution must not use %s", event)
		}
	}
	var raw struct {
		Jobs map[string]yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]string{"contents": "read"}
	for _, name := range []string{
		changesJob, "build-artifacts", "e2e-matrix", "build-e2e-image-artifacts", "fork-e2e",
		"fork-operator-upgrade-e2e", "ci-required",
	} {
		job, exists := workflow.Jobs[name]
		if !exists {
			t.Fatalf("missing fork dependency %s", name)
		}
		permissions := job.Permissions
		if permissions == nil {
			permissions = workflow.Permissions
		}
		if !reflect.DeepEqual(permissions, readOnly) || job.RunsOn != "ubuntu-24.04" || job.Environment != "" {
			t.Fatalf("fork dependency %s must use read-only hosted execution without environment secrets", name)
		}
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["persist-credentials"] != "false" {
				t.Fatalf("fork dependency %s persists checkout credentials", name)
			}
		}
		encoded, err := yaml.Marshal(raw.Jobs[name])
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "secrets.") || strings.Contains(string(encoded), "registry-token:") {
			t.Fatalf("fork dependency %s receives registry credentials or repository secrets", name)
		}
	}
	forkLane := workflow.Jobs["fork-e2e"]
	shared := false
	for _, step := range forkLane.Steps {
		if step.Uses == "./.github/actions/run-e2e-lane" {
			shared = true
			if step.With["lane"] != "${{ toJSON(matrix) }}" || step.With["image-transport"] != "artifact" ||
				step.With["image-artifact"] != "${{ needs.build-e2e-image-artifacts.outputs.artifact_name }}" ||
				step.With["image-build-attempt"] != "${{ needs.build-e2e-image-artifacts.outputs.build_attempt }}" {
				t.Fatal("fork lanes must reuse the selected plan and the successful producer's artifact")
			}
		}
	}
	if !shared {
		t.Fatal("fork lanes do not use the shared execution action")
	}
}

func TestForkToolsAndImagesKeepTheirTrustScope(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, name := range []string{"setup-repo-tools", "load-e2e-image-artifacts"} {
		data, err := os.ReadFile(filepath.Join(root, ".github/actions", name, "action.yml"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if name == "setup-repo-tools" && strings.Count(text,
			"github.event.pull_request.head.repo.full_name == github.repository") != 2 {
			t.Fatal("fork runs must disable both Go and repo-tool cache persistence")
		}
		if name == "load-e2e-image-artifacts" && (strings.Contains(text, "run-id:") ||
			strings.Contains(text, "github-token:") || strings.Contains(text, "pattern:")) {
			t.Fatal("image transport must select one exact same-run artifact")
		}
	}
}

func TestForkAndTrustedPRsRetainIdenticalScenarioSelection(t *testing.T) {
	for _, label := range []string{"", "ci:full-e2e", "backup", "upgrades", "security"} {
		routing := emptyRouting()
		routing["e2e"] = routingTrue
		trusted, err := buildPlan(eventPullRequest, "refs/pull/1/merge", "owner/repo", "sha",
			prEvent("owner/repo", label), routing)
		if err != nil {
			t.Fatal(err)
		}
		fork, err := buildPlan(eventPullRequest, "refs/pull/1/merge", "owner/repo", "sha",
			prEvent("fork/repo", label), routing)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(trusted.E2E, fork.E2E) {
			t.Fatalf("fork PR changed case selection for label %q", label)
		}
	}
}
