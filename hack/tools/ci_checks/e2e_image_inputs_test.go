package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestE2EImageBuildsUsePinnedDockerfileDefaults(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "..")
	digestRef := regexp.MustCompile(`@sha256:[a-f0-9]{64}$`)
	for _, test := range []struct {
		fixture, target, argument string
	}{
		{"openbao-softhsm", "docker-build-e2e-openbao-softhsm", "OPENBAO_BASE_IMAGE"},
		{"pykmip-server", "docker-build-e2e-pykmip-server", "PYKMIP_BASE_IMAGE"},
	} {
		t.Run(test.fixture, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join(root, "test", "e2e", "images", test.fixture, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			var image string
			for _, line := range strings.Split(string(data), "\n") {
				if value, found := strings.CutPrefix(line, "ARG "+test.argument+"="); found {
					image = value
					break
				}
			}
			if !digestRef.MatchString(image) {
				t.Fatalf("fixture base image is not pinned to a manifest digest: %q", image)
			}
			cmd := exec.Command("make", "--no-print-directory", "-n", test.target)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("read fixture build command: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), "--build-arg "+test.argument+"="+image) {
				t.Fatalf("Make overrides the pinned Dockerfile default:\n%s", output)
			}
		})
	}
}
