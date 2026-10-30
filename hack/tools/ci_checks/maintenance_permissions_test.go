package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type maintenanceStep struct {
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

type maintenanceWorkflow struct {
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		Needs       string            `yaml:"needs"`
		Permissions map[string]string `yaml:"permissions"`
		Steps       []maintenanceStep `yaml:"steps"`
	} `yaml:"jobs"`
}

func readMaintenanceWorkflow(t *testing.T, name string) maintenanceWorkflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var workflow maintenanceWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for scope, permission := range workflow.Permissions {
		if permission != "read" && permission != "none" {
			t.Fatalf("workflow default grants %s: %s", scope, permission)
		}
	}
	return workflow
}

func TestMaintenancePreparationHasNoWriteToken(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		workflow, prepare, publish, artifact string
		permissions                          map[string]string
	}{
		{"pages.yml", "build", "deploy", "rendered-docs", map[string]string{"contents": "write"}},
		{"trusted-root-refresh.yml", "prepare", "refresh", "verified-trusted-root",
			map[string]string{"contents": "write", "pull-requests": "write"}},
		{"ghcr-housekeeping.yml", "build", "housekeeping", "ghcr-housekeeping-tool",
			map[string]string{"packages": "write"}},
	} {
		t.Run(test.workflow, func(t *testing.T) {
			t.Parallel()
			workflow := readMaintenanceWorkflow(t, test.workflow)
			prepare, exists := workflow.Jobs[test.prepare]
			if !exists {
				t.Fatal("missing preparation job")
			}
			for scope, permission := range prepare.Permissions {
				if permission != "read" && permission != "none" {
					t.Fatalf("preparation job grants %s: %s", scope, permission)
				}
			}
			uploaded := false
			for _, step := range prepare.Steps {
				action, _, _ := strings.Cut(step.Uses, "@")
				if action == "actions/checkout" && step.With["persist-credentials"] != routingFalse {
					t.Fatal("preparation checkout persists credentials")
				}
				if action == "actions/upload-artifact" && step.With["name"] == test.artifact {
					uploaded = step.With["path"] != "" && step.With["if-no-files-found"] == "error"
				}
			}
			if !uploaded {
				t.Fatal("preparation must fail if its output artifact is absent")
			}
			publish, exists := workflow.Jobs[test.publish]
			if !exists || publish.Needs != test.prepare || !reflect.DeepEqual(publish.Permissions, test.permissions) {
				t.Fatal("mutation job must depend on preparation and declare only its required writes")
			}
			downloaded := false
			for _, step := range publish.Steps {
				action, _, _ := strings.Cut(step.Uses, "@")
				switch action {
				case "actions/download-artifact":
					if step.With["run-id"] != "" || step.With["repository"] != "" {
						t.Fatal("mutation job must consume artifacts from its own workflow run")
					}
					downloaded = step.With["name"] == test.artifact && step.With["path"] != ""
				case "", "actions/checkout", "actions/upload-artifact", "peter-evans/create-pull-request":
				default:
					t.Fatalf("mutation job runs preparation action %q with a write token", action)
				}
				for _, command := range []string{"go build ", "go run ", "make "} {
					if strings.Contains(step.Run, command) {
						t.Fatalf("mutation job runs preparation command %q with a write token", command)
					}
				}
			}
			if !downloaded {
				t.Fatal("mutation job does not download its preparation artifact")
			}
		})
	}
}

func TestLabelDispatchHasOnlyActionsWrite(t *testing.T) {
	t.Parallel()
	workflow := readMaintenanceWorkflow(t, "ci-label-dispatch.yml")
	job, exists := workflow.Jobs["dispatch"]
	if !exists || !reflect.DeepEqual(job.Permissions, map[string]string{"actions": "write"}) {
		t.Fatal("label dispatch must declare only actions: write")
	}
	for _, step := range job.Steps {
		if step.Uses != "" {
			t.Fatal("label dispatch must not check out or execute external code")
		}
	}
}
