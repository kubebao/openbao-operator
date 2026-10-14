package config

import (
	"strings"
	"testing"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
)

func TestRenderPluginCompatibility(t *testing.T) {
	digest := strings.Repeat("a", 64)
	tests := []struct {
		name      string
		version   string
		plugin    openbaov1alpha1.Plugin
		wantError string
	}{
		{name: "digest with embedded version", version: "2.7.0", plugin: openbaov1alpha1.Plugin{Image: "registry.example.com/plugin:v1.0.0@sha256:" + digest}},
		{name: "digest with separate version", version: "2.7.0", plugin: openbaov1alpha1.Plugin{Image: "registry.example.com/plugin@sha256:" + digest, Version: "v1.0.0"}},
		{name: "digest without version", version: "2.7.0", plugin: openbaov1alpha1.Plugin{Image: "registry.example.com/plugin@sha256:" + digest}, wantError: "version is required"},
		{name: "tag without checksum", version: "2.7.0", plugin: openbaov1alpha1.Plugin{Image: "registry.example.com/plugin:v1.0.0"}, wantError: "sha256sum is required"},
		{name: "tag with checksum", version: "2.7.0", plugin: openbaov1alpha1.Plugin{Image: "registry.example.com/plugin:v1.0.0", SHA256Sum: digest}},
		{name: "command KMS", version: "2.7.0", plugin: openbaov1alpha1.Plugin{Command: "plugin"}},
		{name: "command auth without version", version: "2.7.0", plugin: openbaov1alpha1.Plugin{Type: "auth", Command: "plugin"}, wantError: "version is required"},
		{name: "absolute command", version: "2.7.0", plugin: openbaov1alpha1.Plugin{Command: "/tmp/plugin"}, wantError: "relative to the plugin directory"},
		{name: "escaping command", version: "2.7.0", plugin: openbaov1alpha1.Plugin{Command: "../plugin"}, wantError: "relative to the plugin directory"},
		{name: "both sources", version: "2.7.0", plugin: openbaov1alpha1.Plugin{Command: "plugin", Image: "registry.example.com/plugin:v1.0.0"}, wantError: "exactly one"},
		{name: "missing source", version: "2.7.0", wantError: "exactly one"},
		{name: "old version needs explicit fields", version: "2.6.3", plugin: openbaov1alpha1.Plugin{Command: "plugin"}, wantError: "required before OpenBao 2.7.0"},
		{name: "legacy complete declaration", version: "2.6.3", plugin: openbaov1alpha1.Plugin{Command: "plugin", Version: "v1.0.0", BinaryName: "plugin", SHA256Sum: digest}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("plugins", "default")
			cluster.Spec.Version = tt.version
			tt.plugin.Name = "example"
			if tt.plugin.Type == "" {
				tt.plugin.Type = "kms"
			}
			cluster.Spec.Plugins = []openbaov1alpha1.Plugin{tt.plugin}
			got, err := RenderHCL(cluster, InfrastructureDetails{Namespace: "default"})
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
			for key, value := range map[string]string{"version": tt.plugin.Version, "binary_name": tt.plugin.BinaryName, "sha256sum": tt.plugin.SHA256Sum} {
				if value == "" {
					require.NotRegexp(t, `(?m)^\s*`+key+`\s*=`, string(got))
				}
			}
		})
	}
}

func TestRenderRemovedBuiltinSeal(t *testing.T) {
	cluster := newMinimalCluster("awskms", "default")
	cluster.Spec.Version = "2.7.0"
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{Type: "awskms", AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{Region: "us-east-1", KMSKeyID: "test-key"}}
	_, err := RenderHCL(cluster, InfrastructureDetails{Namespace: "default"})
	require.ErrorContains(t, err, "install the external KMS plugin")
	cluster.Spec.Plugins = []openbaov1alpha1.Plugin{{Type: "kms", Name: "awskms", Command: "openbao-plugin-kms-awskms"}}
	got, err := RenderHCL(cluster, InfrastructureDetails{Namespace: "default"})
	require.NoError(t, err)
	require.Contains(t, string(got), `seal "awskms"`)
	require.Contains(t, string(got), `plugin "kms" "awskms"`)
	require.Contains(t, string(got), `kms_key_id = "test-key"`)
}

func TestRenderPluginVersionFloors(t *testing.T) {
	tests := []struct {
		name, target, current, kind, wantError string
	}{
		{"auth before declarative plugins", "2.4.4", "", "auth", ">= 2.5.0"},
		{"secret at minimum", "2.5.0", "", "secret", ""},
		{"database at minimum", "v2.5.0+build.1", "", "database", ""},
		{"declarative prerelease", "2.5.0-rc.1", "", "auth", ">= 2.5.0"},
		{"KMS before minimum", "2.5.5", "", "kms", ">= 2.6.0"},
		{"KMS prerelease", "2.6.0-rc.1", "", "kms", ">= 2.6.0"},
		{"KMS at minimum", "2.6.0", "", "kms", ""},
		{"KMS leading v and metadata", "v2.6.4+build.1", "", "kms", ""},
		{"KMS future minor prerelease", "2.7.0-rc.1", "", "kms", ""},
		{"declarative upgrade incomplete", testOpenBaoVersion264, "2.4.4", "secret", ">= 2.5.0"},
		{"KMS upgrade incomplete", testOpenBaoVersion264, "2.5.5", "kms", ">= 2.6.0"},
		{"KMS upgrade complete", testOpenBaoVersion264, testOpenBaoVersion264, "kms", ""},
		{"invalid running version", testOpenBaoVersion271, "invalid", "kms", "invalid semantic version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("plugin-version", "default")
			cluster.Spec.Version, cluster.Status.CurrentVersion = tt.target, tt.current
			cluster.Spec.Plugins = []openbaov1alpha1.Plugin{{Type: tt.kind, Name: "example", Command: "plugin",
				Version: "v1.0.0", BinaryName: "plugin", SHA256Sum: strings.Repeat("a", 64)}}
			_, err := RenderHCL(cluster, testInfrastructureDetails(cluster))
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
			}
		})
	}
}

func TestRenderPluginMetadataDuringUpgrade(t *testing.T) {
	for _, source := range []string{"command", "image"} {
		for _, omitted := range []string{"", "version", "binaryName", "sha256sum"} {
			for _, current := range []string{testOpenBaoVersion264, "2.7.0-rc.1", testOpenBaoVersion271, ""} {
				t.Run(source+"/omit-"+omitted+"/current-"+current, func(t *testing.T) {
					cluster := newMinimalCluster("plugin-upgrade", "default")
					cluster.Spec.Version, cluster.Status.CurrentVersion = testOpenBaoVersion271, current
					plugin := openbaov1alpha1.Plugin{Type: "kms", Name: "example", Version: "v1.0.0",
						BinaryName: "plugin", SHA256Sum: strings.Repeat("a", 64)}
					if source == "command" {
						plugin.Command = "plugin"
					} else {
						plugin.Image = "registry.example.test/plugin"
						if omitted != "" {
							plugin.Image += ":v1.0.0@sha256:" + strings.Repeat("b", 64)
						}
					}
					switch omitted {
					case "version":
						plugin.Version = ""
					case "binaryName":
						plugin.BinaryName = ""
					case "sha256sum":
						plugin.SHA256Sum = ""
					}
					cluster.Spec.Plugins = []openbaov1alpha1.Plugin{plugin}
					_, err := RenderHCL(cluster, testInfrastructureDetails(cluster))
					if omitted != "" && (current == testOpenBaoVersion264 || current == "2.7.0-rc.1") {
						require.ErrorContains(t, err, "required before OpenBao 2.7.0")
						require.ErrorContains(t, err, current)
					} else {
						require.NoError(t, err)
					}
				})
			}
		}
	}
}

func TestRenderPluginImageReferencesDuringUpgrade(t *testing.T) {
	for _, image := range []string{
		"registry.example.test:5000/plugin", "registry.example.test/plugin:v1.0.0",
		"registry.example.test/plugin:v1.0.0@sha256:" + strings.Repeat("a", 64),
	} {
		for _, current := range []string{testOpenBaoVersion264, testOpenBaoVersion271, ""} {
			t.Run(image+"/current-"+current, func(t *testing.T) {
				cluster := newMinimalCluster("plugin-image-upgrade", "default")
				cluster.Spec.Version, cluster.Status.CurrentVersion = testOpenBaoVersion271, current
				cluster.Spec.Plugins = []openbaov1alpha1.Plugin{{Type: "kms", Name: "example", Image: image,
					Version: "v1.0.0", BinaryName: "plugin", SHA256Sum: strings.Repeat("b", 64)}}
				_, err := RenderHCL(cluster, testInfrastructureDetails(cluster))
				if current == testOpenBaoVersion264 && !strings.Contains(image, ":5000/") {
					require.ErrorContains(t, err, "use an untagged repository and set version separately")
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}
