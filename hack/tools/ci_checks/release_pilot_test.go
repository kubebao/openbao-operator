package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleasePilotCannotPublish(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "release-artifact-pilot.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Name        string               `yaml:"name"`
		On          map[string]yaml.Node `yaml:"on"`
		Permissions map[string]string    `yaml:"permissions"`
		Jobs        map[string]struct {
			If          string            `yaml:"if"`
			Uses        string            `yaml:"uses"`
			Environment yaml.Node         `yaml:"environment"`
			Permissions map[string]string `yaml:"permissions"`
			Steps       []maintenanceStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.Name != "Release Artifact Pilot" || len(workflow.On) != 1 || workflow.On["workflow_dispatch"].Kind == 0 {
		t.Fatal("pilot must retain a distinct name and dispatch-only trigger")
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("pilot must grant only contents: read")
	}
	entryGuard := "github.repository == 'kubebao/openbao-operator' && github.ref == 'refs/heads/main'"
	if workflow.Jobs["prepare"].If != entryGuard {
		t.Fatal("pilot must run only from the canonical repository's main branch")
	}
	if strings.Contains(string(data), "secrets.") || strings.Contains(string(data), "secrets: inherit") {
		t.Fatal("pilot must not access release credentials")
	}
	builds := 0
	for name, job := range workflow.Jobs {
		if job.Uses != "" || len(job.Permissions) != 0 || job.Environment.Kind != 0 {
			t.Fatalf("%s must not call a publishing workflow, elevate permissions, or enter an environment", name)
		}
		for _, step := range job.Steps {
			builds += checkReleasePilotStep(t, step)
		}
	}
	if builds != 1 || len(workflow.Jobs) != 4 {
		t.Fatal("pilot must retain preparation, image, file, and comparison stages")
	}
}

func checkReleasePilotStep(t *testing.T, step maintenanceStep) int {
	t.Helper()
	allowed := map[string]bool{
		checkoutAction: true, "actions/upload-artifact": true, "actions/download-artifact": true,
		"actions/setup-go": true, "azure/setup-helm": true,
		"docker/setup-buildx-action": true, "docker/build-push-action": true,
	}
	pinned := regexp.MustCompile(`^[0-9a-f]{40}$`)
	builds := 0
	if step.Uses != "" {
		action, revision, _ := strings.Cut(step.Uses, "@")
		if !allowed[action] || !pinned.MatchString(revision) {
			t.Fatalf("unreviewed pilot action %q", step.Uses)
		}
		switch action {
		case checkoutAction:
			if step.With["persist-credentials"] != routingFalse || step.With["token"] != "" {
				t.Fatal("pilot checkout must not retain credentials or use an App token")
			}
			want := "${{ github.workflow_sha }}"
			if step.With["path"] == "source-under-test" {
				want = "${{ needs.prepare.outputs[matrix.source] }}"
			}
			if step.With["ref"] != want {
				t.Fatal("pilot checkout lost its source/controller identity binding")
			}
		case "actions/download-artifact":
			if step.With["run-id"] != "" || step.With["repository"] != "" {
				t.Fatal("pilot must consume artifacts from its own run")
			}
			selector := step.With["name"] + step.With["pattern"]
			if !strings.HasPrefix(selector, "release-pilot-${{ github.run_id }}-${{ github.run_attempt }}-") {
				t.Fatal("pilot artifact selection must bind the run and attempt")
			}
		case "docker/build-push-action":
			builds++
			for key, want := range map[string]string{
				"push": routingFalse, "provenance": routingFalse, "sbom": routingFalse, "no-cache": "true",
				"platforms": "linux/amd64,linux/arm64", "outputs": "type=oci,dest=dist/images/${{ matrix.image }}.tar",
				"build-args": "SOURCE_DATE_EPOCH=${{ needs.prepare.outputs.epoch }}",
			} {
				if step.With[key] != want {
					t.Fatalf("pilot build input %s differs from %q", key, want)
				}
			}
			for _, key := range []string{"tags", "attests", "cache-from", "cache-to", "secrets", "ssh"} {
				if step.With[key] != "" {
					t.Fatalf("pilot build must not configure %s", key)
				}
			}
		}
	}
	for _, command := range []string{
		"git push", "gh release", "gh workflow", "docker push", "cosign sign", "gh attestation",
	} {
		if strings.Contains(step.Run, command) {
			t.Fatalf("pilot contains publishing command %q", command)
		}
	}
	return builds
}
