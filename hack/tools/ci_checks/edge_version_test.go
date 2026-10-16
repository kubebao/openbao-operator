package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEdgeVersionIsIndependentOfReleaseVersion(t *testing.T) {
	t.Parallel()
	w := readChannelWorkflow(t, filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml"))
	var script string
	for _, step := range w.Jobs["prepare-edge-candidate"].Steps {
		if step.Name == "Compute edge variables" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("missing edge variable calculation")
	}
	for _, tc := range []struct{ name, chartVersion, attempt string }{
		{"no release chart", "", "1"},
		{"previous release", "0.5.1", "1"},
		{"planned release", "0.6.0-rc.1", "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			gitCommand(t, dir, "init", "--quiet")
			writeFile(t, filepath.Join(dir, "README.md"), "edge fixture\n")
			if tc.chartVersion != "" {
				writeFile(t, filepath.Join(dir, "charts/openbao-operator/Chart.yaml"), "version: "+tc.chartVersion+"\n")
			}
			sha := commitFixture(t, dir)
			outputPath := filepath.Join(t.TempDir(), "outputs")
			cmd := exec.Command("bash", "-ec", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_RUN_ID=123", "GITHUB_RUN_ATTEMPT="+tc.attempt,
				"GITHUB_REPOSITORY_OWNER=kubebao", "GITHUB_OUTPUT="+outputPath)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("compute edge variables: %v\n%s", err, output)
			}
			data, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			outputs := make(map[string]string)
			for _, line := range strings.Split(string(data), "\n") {
				if key, value, ok := strings.Cut(line, "="); ok {
					outputs[key] = value
				}
			}
			want := fmt.Sprintf("0.0.0-edge.123.%s.g%s", tc.attempt, sha[:12])
			if outputs["chart_version"] != want || outputs["version"] != "edge-"+sha[:12] || outputs["sha"] != sha {
				t.Fatalf("unexpected candidate identity: %v", outputs)
			}
		})
	}
}
