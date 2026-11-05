package devtools_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func goCheckFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	sourceScript(t, "mk/development.mk", root)
	write(t, filepath.Join(root, "go.mod"), "module example.test/checks\n\ngo 1.27.3\n", 0644)
	write(t, filepath.Join(root, "Makefile"), `SHELL := /usr/bin/env bash
GOFLAGS_VENDOR := -mod=mod
GOLANGCI_LINT := ./lint
GOTESTSUM := ./gotestsum
ENVTEST := ./envtest
include mk/development.mk
.PHONY: controller-gen kustomize setup-envtest gotestsum golangci-lint
controller-gen kustomize setup-envtest gotestsum golangci-lint: ;
manifests generate verify-coverage test-e2e-support: ;
`, 0644)
	for _, dir := range []string{"api", "cmd", "hack", "internal", "test", "test/e2e"} {
		write(t, filepath.Join(root, dir, "source.go"), "package fixture\n", 0644)
	}
	for _, dir := range []string{".github", ".semgrep"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, "lint"), "#!"+bashPath(t)+`
set -euo pipefail
[[ $1 == run ]]
shift
if [[ ${1:-} == --fix ]]; then shift; fi
if [[ $# == 0 ]]; then set -- ./...; fi
exec go vet "$@"
`, 0755)
	write(t, filepath.Join(root, "gotestsum"), "#!"+bashPath(t)+`
set -euo pipefail
while [[ $1 != -- ]]; do shift; done
shift
exec go test "$@"
`, 0755)
	write(t, filepath.Join(root, "envtest"), "#!"+bashPath(t)+"\nprintf '.\\n'\n", 0755)
	return root
}

func runGoCheck(root, target string) ([]byte, error) {
	cmd := exec.Command("make", target)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOTOOLCHAIN=local")
	return cmd.CombinedOutput()
}

func TestMakeGoChecksIgnoreGeneratedArtifacts(t *testing.T) {
	for _, target := range []string{
		"fmt", "verify-fmt", "vet", "vet-tagged", "lint", "lint-fix", "test", "test-sum",
		"test-ci", "test-integration", "test-integration-sum",
	} {
		t.Run(target, func(t *testing.T) {
			root := goCheckFixture(t)
			const artifact = "package artifact\nvar ignored=undefinedArtifactSymbol\n"
			for _, dir := range []string{"artifacts/probe", "dist/test/probe", "scratch/probe"} {
				write(t, filepath.Join(root, dir, "probe.go"), artifact, 0644)
			}
			log, err := runGoCheck(root, target)
			if err != nil {
				t.Fatalf("%s included local generated Go artifacts: %v\n%s", target, err, log)
			}
			for _, dir := range []string{"artifacts/probe", "dist/test/probe", "scratch/probe"} {
				contents, err := os.ReadFile(filepath.Join(root, dir, "probe.go"))
				if err != nil || string(contents) != artifact {
					t.Fatalf("%s changed a generated artifact: %q, %v", target, contents, err)
				}
			}
		})
	}
}

func TestMakeGoChecksRejectSourceErrors(t *testing.T) {
	for _, target := range []string{"vet", "vet-tagged", "lint"} {
		for _, dir := range []string{"api", "cmd", "hack", "internal", "test"} {
			t.Run(target+"/"+dir, func(t *testing.T) {
				root := goCheckFixture(t)
				write(t, filepath.Join(root, dir, "source.go"),
					"package fixture\nvar value = undefinedSourceSymbol\n", 0644)
				log, err := runGoCheck(root, target)
				if err == nil || !strings.Contains(string(log), "undefinedSourceSymbol") {
					t.Fatalf("%s did not reject the source error: %v\n%s", target, err, log)
				}
			})
		}
	}
}
