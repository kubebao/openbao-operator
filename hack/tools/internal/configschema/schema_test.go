package configschema

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamIncludesSharedProfilesAndRaft(t *testing.T) {
	for _, internal := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "internal"}[internal], func(t *testing.T) {
			root := t.TempDir()
			prefix := ""
			if internal {
				prefix = "internal/"
			}
			writeSource(t, root, prefix+"command/server/config.go", "package server\n"+
				"type Config struct { UI bool `hcl:\"ui\"` }\n"+
				"type AuditDevice struct { Options map[string]string `hcl:\"options\"` }\n"+
				"type PluginConfig struct { Command string `hcl:\"command\"` }\n")
			writeSource(t, root, prefix+"helper/configutil/config.go", "package configutil\n"+
				"type SharedConfig struct { LogLevel string `hcl:\"log_level\"` }\n"+
				"type Listener struct { Reload bool `hcl:\"tls_auto_reload\"`; Telemetry []ListenerTelemetry `hcl:\"telemetry\"` }\n"+
				"type ListenerTelemetry struct { MetricsOnly bool `hcl:\"metrics_only\"` }\n"+
				"type Telemetry struct { Prefix string `hcl:\"metrics_prefix\"` }\n")
			writeSource(t, root, prefix+"helper/profiles/config.go", "package profiles\n"+
				"type OuterConfig struct { When bool `hcl:\"when\"` }\n"+
				"type RequestConfig struct { Headers map[string]string `hcl:\"headers\"` }\n")
			writeSource(t, root, prefix+"physical/raft/raft.go", "package raft\n"+
				"const nonVoter = \"retry_join_as_non_voter\"\n"+
				"type LeaderJoinInfo struct { AutoJoin string `json:\"auto_join\"` }\n"+
				"func example(conf map[string]string) { _ = conf[nonVoter]; _ = conf[\"performance_multiplier\"]; println(\"election_timeout\") }\n")
			keys, err := Upstream(root)
			require.NoError(t, err)
			for _, key := range []string{"log_level", "listener.tls_auto_reload", "listener.telemetry.metrics_only",
				"telemetry.metrics_prefix", "plugin.command", "initialize.request.headers", "initialize.when",
				"storage.retry_join_as_non_voter", "storage.performance_multiplier", "storage.retry_join.auto_join"} {
				require.True(t, keys[key], "missing %s", key)
			}
			require.False(t, keys["storage.election_timeout"], "log messages must not become accepted settings")
		})
	}
}

func TestSchemaDoesNotTreatOpaquePayloadsAsServerKeys(t *testing.T) {
	for _, path := range []string{"seal.token", "audit.options.headers", "initialize.request.data.options.file_path"} {
		require.True(t, Opaque(path), path)
	}
	for _, path := range []string{"listener.tls_auto_relaod", "storage.retry_join_as_non_voterr", "initialize.request.path"} {
		require.False(t, Opaque(path), path)
	}
}

func TestUpstreamRejectsUnrecognizedLayout(t *testing.T) {
	_, err := Upstream(t.TempDir())
	require.ErrorContains(t, err, "source directory")
}

func writeSource(t *testing.T, root, path, text string) {
	t.Helper()
	file := filepath.Join(root, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o750))
	require.NoError(t, os.WriteFile(file, []byte(text), 0o600))
}
