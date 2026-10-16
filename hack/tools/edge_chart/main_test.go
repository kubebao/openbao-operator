package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testConfig() config {
	sha := strings.Repeat("a", 40)
	return config{
		sha: sha, version: "edge-" + sha[:12], chartVersion: "0.0.0-edge.123.1.g" + sha[:12],
		images: []image{
			{name: "openbao-operator", ref: "ghcr.io/kubebao/openbao-operator",
				digest: "sha256:" + strings.Repeat("1", 64)},
			{name: "openbao-init", ref: "ghcr.io/kubebao/openbao-init",
				digest: "sha256:" + strings.Repeat("2", 64), key: "init"},
			{name: "openbao-backup", ref: "ghcr.io/kubebao/openbao-backup",
				digest: "sha256:" + strings.Repeat("3", 64), key: "backup"},
			{name: "openbao-upgrade", ref: "ghcr.io/kubebao/openbao-upgrade",
				digest: "sha256:" + strings.Repeat("4", 64), key: "upgrade"},
		},
	}
}

func TestPreparePinsCandidateWithoutChangingTenancyOrPolicy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"Chart.yaml", "values.yaml"} {
		data, err := os.ReadFile(filepath.Join("../../../charts/openbao-operator", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o644))
	}
	before, err := readYAML(filepath.Join(dir, "values.yaml"))
	require.NoError(t, err)
	cfg := testConfig()
	require.NoError(t, prepare(dir, cfg))
	chart, err := readYAML(filepath.Join(dir, "Chart.yaml"))
	require.NoError(t, err)
	require.Equal(t, cfg.chartVersion, chart["version"])
	require.Equal(t, cfg.version, chart["appVersion"])
	annotations := mapping(chart, "annotations")
	require.Equal(t, "true", annotations["artifacthub.io/prerelease"])
	require.NotContains(t, annotations, "artifacthub.io/changes")
	require.NotContains(t, annotations, "artifacthub.io/images")
	require.Equal(t, cfg.sha, annotations["org.opencontainers.image.revision"])
	values, err := readYAML(filepath.Join(dir, "values.yaml"))
	require.NoError(t, err)
	require.Equal(t, cfg.images[0].digest, mapping(values, "image")["digest"])
	require.Equal(t, cfg.version, values["operatorVersion"])
	for _, key := range []string{"tenancy", "admissionPolicies", "provisioner", "controller", "platform"} {
		require.Equal(t, before[key], values[key], key)
	}
	for i, key := range []string{"init", "backup", "upgrade"} {
		require.Equal(t, cfg.images[i+1].ref+"@"+cfg.images[i+1].digest, mapping(values, "helperImages")[key])
	}
}

func TestRejectInvalidCandidateBeforeWriting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*config)
	}{
		{"release version", func(c *config) { c.chartVersion = "0.5.0" }},
		{"nightly version", func(c *config) { c.chartVersion = "0.5.0-nightly.123" }},
		{"version path", func(c *config) { c.chartVersion = "../0.5.0" }},
		{"wrong commit", func(c *config) { c.version = "edge-bbbbbbbbbbbb" }},
		{"abbreviated commit", func(c *config) { c.sha = c.sha[:12] }},
		{"missing helper", func(c *config) { c.images = c.images[:3] }},
		{"tag instead of digest", func(c *config) { c.images[0].digest = "edge" }},
		{"wrong image", func(c *config) { c.images[1].ref = "ghcr.io/kubebao/openbao-operator" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			tc.change(&cfg)
			require.Error(t, prepare(t.TempDir(), cfg))
		})
	}
}
