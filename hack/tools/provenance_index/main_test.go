package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReleaseApprovalProvenance(t *testing.T) {
	dir := t.TempDir()
	var checksums strings.Builder
	for _, name := range []string{"operator-policy-approval.hcl", "operator-policy-approval-with-backup.hcl"} {
		contents := []byte("approval for " + name)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), contents, 0o600))
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(contents), name)
	}
	checksumsPath := filepath.Join(dir, "checksums.txt")
	require.NoError(t, os.WriteFile(checksumsPath, []byte(checksums.String()), 0o600))
	index, err := buildReleaseIndex(args{checksumsPath: checksumsPath})
	require.NoError(t, err)
	files := index["release_artifacts"].(map[string]any)["files"].([]map[string]any)
	require.Len(t, files, 3)
	for _, file := range files[1:] {
		require.Equal(t, true, file["included_in_checksums_txt"])
		require.Equal(t, file["sha256"], file["checksums_txt_sha256"])
	}
}

func TestChannelChartProvenance(t *testing.T) {
	t.Parallel()
	cfg := args{
		channel: "edge", repo: "kubebao/openbao-operator", owner: "kubebao",
		version: "edge-aaaaaaaaaaaa", commit: strings.Repeat("a", 40),
		checksumsPath: edgeMigrationFixture(t),
		chartRef:      "ghcr.io/kubebao/charts-edge/openbao-operator", chartVersion: "0.5.0-edge.123.1.gaaaaaaaaaaaa",
		chartDigest: "sha256:" + strings.Repeat("a", 64), sourceRef: "refs/heads/main",
		checksumsSignerWorkflow: "kubebao/openbao-operator/.github/workflows/publish-edge.yml",
	}
	index, err := buildChannelIndex(cfg)
	require.NoError(t, err)
	chart := index["chart"].(map[string]any)
	require.Equal(t, cfg.chartRef+"@"+cfg.chartDigest, chart["oci_subject"])
	require.Equal(t, cfg.chartVersion, chart["version"])
	require.Equal(t,
		"https://github.com/kubebao/openbao-operator/.github/workflows/publish-edge.yml@refs/heads/main",
		chart["signing_identity"])
	require.Equal(t, cfg.checksumsSignerWorkflow, chart["attestation_signer_workflow"])
	cli := index["api_migration"].(map[string]any)
	require.Equal(t, cfg.version, cli["version"])
	require.Equal(t, cfg.commit, cli["source_digest"])
	require.Equal(t, cfg.repo+"/.github/workflows/reusable-channel-hardening.yml", cli["attestation_signer_workflow"])
	require.Len(t, cli["files"], 3)
	wrongVersion := cfg
	wrongVersion.version = "edge-bbbbbbbbbbbb"
	_, err = buildChannelIndex(wrongVersion)
	require.ErrorContains(t, err, "edge migration version must identify the source commit")
	cfg.chartVersion = ""
	_, err = buildChannelIndex(cfg)
	require.Error(t, err)
	cfg.chartDigest = ""
	cfg.channel = "nightly"
	index, err = buildChannelIndex(cfg)
	require.NoError(t, err)
	require.NotContains(t, index, "chart")
	require.NotContains(t, index, "api_migration")
}

func edgeMigrationFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var checksums strings.Builder
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-arm64"} {
		name := "api-migration-" + platform
		data := []byte("binary " + platform)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(data), name)
	}
	path := filepath.Join(dir, "checksums.txt")
	require.NoError(t, os.WriteFile(path, []byte(checksums.String()), 0o600))
	return path
}

func TestEdgeMigrationProvenanceRejectsMissingOrChangedPayloads(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-arm64"} {
		t.Run(platform, func(t *testing.T) {
			cfg := args{channel: "edge", repo: "kubebao/openbao-operator", checksumsPath: edgeMigrationFixture(t),
				version: "edge-aaaaaaaaaaaa", commit: strings.Repeat("a", 40)}
			name := filepath.Join(filepath.Dir(cfg.checksumsPath), "api-migration-"+platform)
			require.NoError(t, os.WriteFile(name, []byte("tampered"), 0o600))
			_, err := buildChannelIndex(cfg)
			require.ErrorContains(t, err, "does not match checksums.txt")
			require.NoError(t, os.Remove(name))
			_, err = buildChannelIndex(cfg)
			require.ErrorContains(t, err, "hash edge migration CLI")
		})
	}
}
