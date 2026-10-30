package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const releaseWritePermission = "write"

type releaseApprovalJob struct {
	Needs       yaml.Node         `yaml:"needs"`
	Environment string            `yaml:"environment"`
	Permissions map[string]string `yaml:"permissions"`
	With        map[string]string `yaml:"with"`
	Env         map[string]string `yaml:"env"`
	Steps       []codeQLStep      `yaml:"steps"`
}

func TestReleaseWritesRequireApproval(t *testing.T) {
	for _, name := range []string{"release-candidate.yml", "release.yml"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", name))
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				Permissions map[string]string             `yaml:"permissions"`
				Jobs        map[string]releaseApprovalJob `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(data, &workflow); err != nil {
				t.Fatal(err)
			}
			for scope, permission := range workflow.Permissions {
				if permission == releaseWritePermission {
					t.Fatalf("workflow defaults grant %s writes outside approval dependencies", scope)
				}
			}
			environment := strings.TrimSuffix(name, ".yml")
			if name == "release.yml" {
				environment = "release-publish"
			}
			gate := workflow.Jobs["authorize"]
			if gate.Environment != environment {
				t.Fatalf("approval job must use protected environment %s", environment)
			}
			checked := false
			for _, step := range gate.Steps {
				if step.Run == "bash hack/ci/check-release-approval.sh" &&
					step.Env["RUN_ID"] == "${{ github.run_id }}" && step.Env["ENVIRONMENT_NAME"] == environment {
					checked = true
				}
			}
			if !checked {
				t.Fatal("approval job must verify actual review history for its own run")
			}
			for scope, permission := range gate.Permissions {
				if permission == releaseWritePermission {
					t.Fatalf("approval job grants %s writes before authenticating approval", scope)
				}
			}
			var approved func(string) bool
			approved = func(jobName string) bool {
				if jobName == "authorize" {
					return true
				}
				needs := workflow.Jobs[jobName].Needs
				var dependencies []string
				if needs.Kind == yaml.ScalarNode {
					dependencies = []string{needs.Value}
				} else if needs.Kind != 0 {
					if err := needs.Decode(&dependencies); err != nil {
						t.Fatal(err)
					}
				}
				for _, dependency := range dependencies {
					if approved(dependency) {
						return true
					}
				}
				return false
			}
			for jobName, job := range workflow.Jobs {
				if job.Permissions["packages"] == releaseWritePermission && !approved(jobName) {
					t.Fatalf("%s can publish registry objects without approval", jobName)
				}
			}
			if name == "release-candidate.yml" {
				if workflow.Jobs["build"].With["sign_images"] != "true" {
					t.Fatal("candidate helpers must be signed before Hardened runtime tests")
				}
				if workflow.Jobs["e2e"].Env["E2E_CANDIDATE_PR"] != "${{ github.event.pull_request.number }}" {
					t.Fatal("candidate E2E trust must name the actual PR")
				}
			}
		})
	}
}
