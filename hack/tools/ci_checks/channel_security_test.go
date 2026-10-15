package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const channelEventSHA = "${{ github.sha }}"

type channelWorkflow struct {
	On   map[string]yaml.Node `yaml:"on"`
	Jobs map[string]struct {
		If    string            `yaml:"if"`
		Uses  string            `yaml:"uses"`
		With  map[string]string `yaml:"with"`
		Steps []struct {
			Name string            `yaml:"name"`
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			With map[string]string `yaml:"with"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readChannelWorkflow(t *testing.T, path string) channelWorkflow {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var workflow channelWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func TestChannelSourceIsBoundToEventSHA(t *testing.T) {
	root := filepath.Join("..", "..", "..", ".github", "workflows")
	workflow := readChannelWorkflow(t, filepath.Join(root, "reusable-channel-hardening.yml"))
	var call struct {
		Inputs map[string]yaml.Node `yaml:"inputs"`
	}
	callNode, exists := workflow.On["workflow_call"]
	if !exists {
		t.Fatal("channel workflow must retain its reusable entry point")
	}
	if err := callNode.Decode(&call); err != nil {
		t.Fatal(err)
	}
	if _, exists := call.Inputs["ref"]; exists {
		t.Fatal("channel candidates must not accept a caller-controlled source ref")
	}
	for _, name := range []string{"build", "rebuild"} {
		job := workflow.Jobs[name]
		if job.Uses != "./.github/workflows/reusable-build.yml" || job.With["ref"] != channelEventSHA {
			t.Fatalf("%s must build the event SHA through the shared image builder", name)
		}
	}
	sourceCheckouts, helperCheckouts, chartPackages := 0, 0, 0
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/checkout@") {
				if step.With["persist-credentials"] != "false" {
					t.Fatalf("%s/%s must not persist checkout credentials", name, step.Name)
				}
				switch step.With["path"] {
				case "source-under-test":
					sourceCheckouts++
					if step.With["ref"] != channelEventSHA {
						t.Fatal("candidate source checkout must use the event SHA")
					}
				case ".github-workflow-src", "":
					if step.With["path"] == "" && name != "helm-edge-smoke" {
						t.Fatalf("unexpected checkout into the workspace root in %s", name)
					}
					helperCheckouts++
					if step.With["ref"] != "${{ github.workflow_sha }}" {
						t.Fatal("workflow helpers must use the workflow SHA")
					}
				default:
					t.Fatalf("unexpected channel checkout path %q", step.With["path"])
				}
			}
			if strings.HasPrefix(step.Name, "Package Helm chart (") {
				chartPackages++
				if step.Env["SHA"] != channelEventSHA {
					t.Fatal("chart metadata must identify the same event SHA as the images and manifests")
				}
			}
		}
	}
	if sourceCheckouts != 1 || helperCheckouts != 3 || chartPackages != 2 {
		t.Fatalf("missing source, helper, or chart paths: %d/%d/%d", sourceCheckouts, helperCheckouts, chartPackages)
	}
}

func TestChannelCallersPreserveTrustedEntryPoints(t *testing.T) {
	root := filepath.Join("..", "..", "..", ".github", "workflows")
	paths, err := filepath.Glob(filepath.Join(root, "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	callers := make(map[string]string)
	for _, path := range paths {
		// Decode only jobs: unrelated triggers can use scalar or sequence syntax.
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var workflow struct {
			Jobs map[string]struct {
				If   string            `yaml:"if"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(data, &workflow); err != nil {
			t.Fatal(err)
		}
		for name, job := range workflow.Jobs {
			if job.Uses != "./.github/workflows/reusable-channel-hardening.yml" {
				continue
			}
			if _, exists := job.With["ref"]; exists {
				t.Fatalf("%s/%s must not select a separate candidate ref", path, name)
			}
			callers[filepath.Base(path)+"/"+name] = job.If
		}
	}
	want := map[string]string{
		"ci.yml/build-edge-candidate":         "github.event_name == 'push' && github.ref == 'refs/heads/main'",
		"nightly.yml/build-nightly-candidate": "",
	}
	if !reflect.DeepEqual(callers, want) {
		t.Fatalf("channel entry points changed: got %v, want %v", callers, want)
	}
	nightly := readChannelWorkflow(t, filepath.Join(root, "nightly.yml"))
	if len(nightly.On) != 2 {
		t.Fatal("nightly candidates must only run on schedule or maintainer dispatch")
	}
	for _, event := range []string{"schedule", "workflow_dispatch"} {
		if _, exists := nightly.On[event]; !exists {
			t.Fatalf("missing nightly trigger %s", event)
		}
	}
}
