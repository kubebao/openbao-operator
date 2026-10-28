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

const (
	identityProducer      = "producer"
	identityUploadAction  = "actions/upload-artifact"
	identityControllerRef = "${{ github.workflow_sha }}"
)

func TestReleaseIdentityPilotPermissions(t *testing.T) {
	for _, role := range []string{identityProducer, "consumer"} {
		t.Run(role, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", ".github", "workflows", "release-evidence-identity-"+role+".yml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				On          map[string]yaml.Node `yaml:"on"`
				Permissions map[string]string    `yaml:"permissions"`
				Jobs        map[string]struct {
					If          string            `yaml:"if"`
					Uses        string            `yaml:"uses"`
					RunsOn      string            `yaml:"runs-on"`
					Environment yaml.Node         `yaml:"environment"`
					Permissions map[string]string `yaml:"permissions"`
					Steps       []maintenanceStep `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(data, &workflow); err != nil {
				t.Fatal(err)
			}
			read := map[string]string{"contents": "read", "actions": "read", "attestations": "read"}
			want := read
			if role == identityProducer {
				want = map[string]string{"contents": "read"}
			}
			if len(workflow.On) != 1 || workflow.On["workflow_dispatch"].Kind == 0 || len(workflow.Jobs) != 1 ||
				!reflect.DeepEqual(workflow.Permissions, want) {
				t.Fatal("identity pilot must remain dispatch-only with one job and least-privilege defaults")
			}
			for _, job := range workflow.Jobs {
				if job.Environment.Kind != 0 || job.Uses != "" || job.RunsOn != "ubuntu-24.04" {
					t.Fatal("identity pilot must use a fresh hosted job without deployment or reusable publication")
				}
				if role == identityProducer {
					want = map[string]string{"contents": "read", "actions": "read", "id-token": "write", "attestations": "write"}
					if !reflect.DeepEqual(job.Permissions, want) {
						t.Fatal("producer may only read source/metadata and sign attestations")
					}
				} else if len(job.Permissions) != 0 {
					t.Fatal("consumer may not elevate its read-only permissions")
				}
				guard := "github.repository == 'kubebao/openbao-operator' && github.ref == 'refs/heads/main' && " +
					"github.sha == github.workflow_sha && github.workflow_sha == inputs.controller_sha"
				if job.If != guard {
					t.Fatal("identity pilot must bind the reviewed controller, event SHA and canonical main")
				}
				checkIdentitySteps(t, role, job.Steps)
			}
			for _, forbidden := range []string{
				"secrets.", "secrets: inherit", "git push", "gh release", "gh workflow", "docker push",
				"packages: write", "contents: write",
			} {
				if strings.Contains(string(data), forbidden) {
					t.Fatalf("identity workflow contains forbidden capability %q", forbidden)
				}
			}
		})
	}
}

func checkIdentitySteps(t *testing.T, role string, steps []maintenanceStep) {
	t.Helper()
	attests := 0
	for _, step := range steps {
		if step.Uses == "" {
			continue
		}
		action, revision, _ := strings.Cut(step.Uses, "@")
		allowed := action == checkoutAction || action == identityUploadAction ||
			(role == identityProducer && action == "actions/attest-build-provenance")
		if !allowed || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(revision) {
			t.Fatalf("unreviewed identity action %q", step.Uses)
		}
		if action == checkoutAction && (step.With["ref"] != identityControllerRef ||
			step.With["persist-credentials"] != routingFalse || step.With["token"] != "") {
			t.Fatal("identity pilot must check out only its trusted controller without retained credentials")
		}
		if action == "actions/attest-build-provenance" {
			attests++
			if step.With["subject-path"] != "dist/identity-producer/probe.json" ||
				step.With["push-to-registry"] != routingFalse || step.With["create-storage-record"] != routingFalse {
				t.Fatal("producer must sign only the identity probe without registry publication")
			}
		}
	}
	if (role == identityProducer && attests != 1) || (role == "consumer" && attests != 0) {
		t.Fatal("only the producer may attest one generated identity probe")
	}
}
