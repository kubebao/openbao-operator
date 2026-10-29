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
	qualificationPlanJob        = "plan"
	qualificationImportJob      = "import"
	qualificationImageJob       = "images"
	qualificationRuntimeJob     = "e2e"
	qualificationAuditJob       = "qualify"
	qualificationConsumeJob     = "consume"
	qualificationCandidatePath  = "source-under-test"
	qualificationDownloadAction = "actions/download-artifact"
)

func TestQualificationBuildAuthoritySeparation(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "release-qualification-pilot.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On          map[string]yaml.Node `yaml:"on"`
		Env         map[string]string    `yaml:"env"`
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
	read := map[string]string{"contents": "read"}
	if workflow.Env["GOWORK"] != "off" || workflow.Env["GOENV"] != "off" {
		t.Fatal("qualification must disable candidate workspace and local Go environment configuration")
	}
	if len(workflow.On) != 1 || workflow.On["workflow_dispatch"].Kind == 0 ||
		!reflect.DeepEqual(workflow.Permissions, read) || len(workflow.Jobs) != 12 {
		t.Fatal("build pilot must remain a separate dispatch-only twelve-stage experiment")
	}
	guard := "github.repository == 'kubebao/openbao-operator' && github.ref == 'refs/heads/main' && " +
		"github.sha == github.workflow_sha && github.workflow_sha == inputs.controller_sha"
	if workflow.Jobs[qualificationPlanJob].If != guard {
		t.Fatal("planning must bind the controller and actual main event identity")
	}
	for name, job := range workflow.Jobs {
		want := read
		switch name {
		case qualificationPlanJob:
			want = map[string]string{"contents": "read", "pull-requests": "read"}
		case qualificationImageJob, "files", "compile", "api", "source-security":
		case "security-images", qualificationRuntimeJob, "upgrade":
			want = map[string]string{"contents": "read", "actions": "read", "packages": "read"}
		case qualificationAuditJob:
			want = map[string]string{"contents": "read", "actions": "read", "packages": "read",
				"id-token": "write", "attestations": "write"}
		case qualificationConsumeJob:
			want = map[string]string{"contents": "read", "actions": "read"}
		case qualificationImportJob:
			want = map[string]string{"contents": "read", "actions": "read", "packages": "write", "id-token": "write"}
		default:
			t.Fatalf("unreviewed qualification stage %q", name)
		}
		if !reflect.DeepEqual(job.Permissions, want) || job.Environment.Kind != 0 ||
			job.Uses != "" || job.RunsOn != codeQLRunner {
			t.Fatalf("%s changed its authority or fresh-runner boundary", name)
		}
		checkQualificationSteps(t, name, job.Steps)
	}
	for _, forbidden := range []string{"secrets.", "secrets: inherit", "git push", "gh release", "gh workflow"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("qualification build pilot contains production capability %q", forbidden)
		}
	}
}

func checkQualificationSteps(t *testing.T, job string, steps []maintenanceStep) {
	t.Helper()
	allowed := map[string]bool{
		checkoutAction: true, qualificationDownloadAction: true, identityUploadAction: true,
		"actions/setup-go": true, "docker/setup-buildx-action": true, "docker/build-push-action": true,
		"azure/setup-helm": true, "sigstore/cosign-installer": true, "docker/login-action": true,
		"actions/attest-build-provenance": true,
	}
	for _, step := range steps {
		if step.Run != "" && qualificationTrustedJob(job) &&
			!qualificationTrustedCommandAllowed(job, strings.TrimSpace(step.Run)) {
			t.Fatalf("%s invokes an unreviewed controller entry point", job)
		}
		if step.Uses == "" {
			continue
		}
		if strings.HasPrefix(step.Uses, "./.github/actions/") {
			permitted := map[string]bool{"./.github/actions/setup-helm": true,
				"./.github/actions/setup-kubernetes-tools": true, "./.github/actions/prepare-e2e-lane": true,
				"./.github/actions/setup-trivy": true}
			if !permitted[step.Uses] || (job != qualificationRuntimeJob && job != "upgrade" && job != "security-images") {
				t.Fatal("unreviewed local controller action or authority boundary")
			}
			continue
		}
		action, revision, _ := strings.Cut(step.Uses, "@")
		if !allowed[action] || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(revision) {
			t.Fatalf("unreviewed qualification action %q", step.Uses)
		}

		switch action {
		case checkoutAction:
			checkQualificationCheckout(t, job, step)
		case identityUploadAction, qualificationDownloadAction:
			checkQualificationArtifact(t, action, step)
		case "docker/build-push-action":
			checkQualificationImageBuild(t, job, step)
		}
	}
}

func qualificationTrustedJob(job string) bool {
	return job == qualificationPlanJob || job == qualificationImportJob ||
		job == qualificationAuditJob || job == qualificationConsumeJob
}

func checkQualificationCheckout(t *testing.T, job string, step maintenanceStep) {
	t.Helper()
	want := identityControllerRef
	if step.With["path"] == qualificationCandidatePath {
		candidateJobs := map[string]bool{qualificationImageJob: true, "files": true, "compile": true,
			"api": true, "source-security": true, qualificationRuntimeJob: true, "upgrade": true}
		if !candidateJobs[job] {
			t.Fatal("trusted jobs must not check out candidate source")
		}
		want = "${{ needs.plan.outputs.candidate }}"
	}
	if step.With["ref"] != want || step.With["persist-credentials"] != routingFalse ||
		step.With["repository"] != "" || step.With["token"] != "" {
		t.Fatal("qualification checkout lost its pinned source or credential boundary")
	}
}

func checkQualificationArtifact(t *testing.T, action string, step maintenanceStep) {
	t.Helper()
	selector := step.With["name"] + step.With["pattern"]
	if !strings.HasPrefix(selector, "release-qualification-${{ github.run_id }}-${{ github.run_attempt }}-") ||
		step.With["overwrite"] != "" || step.With["run-id"] != "" || step.With["repository"] != "" {
		t.Fatal("qualification artifacts must be immutable and bound to the current run and attempt")
	}
	if action == identityUploadAction && step.With["retention-days"] != "30" {
		t.Fatal("qualification artifacts must retain the declared experimental retention")
	}
}

func checkQualificationImageBuild(t *testing.T, job string, step maintenanceStep) {
	t.Helper()
	if job != qualificationImageJob {
		t.Fatal("candidate builds must stay outside trusted signing")
	}
	for key, want := range map[string]string{
		"push": routingFalse, "provenance": routingFalse, "sbom": routingFalse, "no-cache": "true",
		"platforms": "linux/amd64,linux/arm64",
	} {
		if step.With[key] != want {
			t.Fatalf("candidate build changed %s", key)
		}
	}
	for _, key := range []string{"tags", "attests", "cache-from", "cache-to", "secrets", "ssh"} {
		if step.With[key] != "" {
			t.Fatalf("candidate build must not receive %s", key)
		}
	}
}

func qualificationTrustedCommandAllowed(job, command string) bool {
	if job == qualificationPlanJob {
		return command == "bash hack/ci/install-qualification-oras.sh" ||
			command == "bash hack/ci/freeze-qualification-plan.sh" ||
			command == "python3 hack/ci/release-qualification.py inspect-source --controller \"${CONTROLLER}\" "+
				"--pr \"${PR}\" --head \"${HEAD}\" --output dist/source"
	}
	if job == qualificationImportJob {
		return command == "bash hack/ci/install-qualification-oras.sh" ||
			command == "bash hack/ci/sign-qualification-images.sh" ||
			command == "python3 hack/ci/qualification-artifacts.py --plan dist/plan/plan.json "+
				"import-images --output dist/import"
	}
	if job == qualificationAuditJob {
		return command == "python3 hack/ci/qualification-audit.py --plan dist/plan/plan.json --output dist/audit audit" ||
			command == "bash hack/ci/retain-qualification-record.sh"
	}
	if job == qualificationConsumeJob {
		return command == "bash hack/ci/install-identity-pilot-gh.sh" ||
			command == "python3 hack/ci/qualification-audit.py --plan dist/plan/plan.json --output dist/consumer consume"
	}

	return false
}

func TestQualificationTrustedJobsRejectCandidateCommands(t *testing.T) {
	for _, job := range []string{
		qualificationPlanJob, qualificationImportJob, qualificationAuditJob, qualificationConsumeJob,
	} {
		for _, command := range []string{
			"docker run ghcr.io/kubebao/openbao-operator-qualification@sha256:" + strings.Repeat("a", 64),
			"bash source-under-test/hack/ci/operator-upgrade-e2e.sh", "./source-under-test/manager",
			"make -C source-under-test", "python3 -c 'print(1)'",
			"bash hack/ci/sign-qualification-images.sh && docker run candidate:latest",
		} {
			if qualificationTrustedCommandAllowed(job, command) {
				t.Fatalf("%s accepted candidate execution or a substituted entry point: %s", job, command)
			}
		}
	}
}

func TestQualificationRequiresEveryGate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "release-qualification-pilot.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs yaml.Node
			If    string
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	expected := []string{"plan", "images", "files", "import", "compile", "api", "source-security",
		"security-images", qualificationRuntimeJob, "upgrade"}
	for job, needs := range map[string][]string{qualificationAuditJob: expected,
		qualificationConsumeJob: {"plan", qualificationAuditJob}} {
		got := []string{}
		for _, node := range workflow.Jobs[job].Needs.Content {
			got = append(got, node.Value)
		}
		if !reflect.DeepEqual(got, needs) || workflow.Jobs[job].If != "" {
			t.Fatalf("%s must wait for every prerequisite to succeed", job)
		}
	}
}
