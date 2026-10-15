package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/stretchr/testify/require"
)

func TestSecretInputPreflight(t *testing.T) {
	const activeConfig = `initialize "bootstrap" { request "login" { operation = "update" } }`
	tests := []struct {
		name, config, hostname string
		present, directory     bool
		wantError              string
	}{
		{name: "readable input", config: activeConfig, hostname: "bao-0", present: true},
		{name: "missing Secret or key", config: activeConfig, hostname: "bao-0",
			wantError: "required self-init Secret input"},
		{name: "directory is not a value", config: activeConfig, hostname: "bao-0",
			directory: true, wantError: "regular file"},
		{name: "missing bootstrap config fails closed", hostname: "bao-0", wantError: "configuration is missing"},
		{name: "completed bootstrap with stale Pod arguments", config: constants.CompletedSelfInitConfig, hostname: "bao-0"},
		{name: "joining replica needs no bootstrap inputs", config: activeConfig, hostname: "bao-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			template := filepath.Join(dir, "template.hcl")
			output := filepath.Join(dir, "output.hcl")
			config := filepath.Join(dir, "init.hcl")
			input := filepath.Join(dir, "input")
			require.NoError(t, os.WriteFile(template, []byte(testListenerTemplate), 0o644))
			if tt.config != "" {
				require.NoError(t, os.WriteFile(config, []byte(tt.config), 0o644))
			}
			if tt.present {
				require.NoError(t, os.WriteFile(input, []byte("sentinel-sensitive-value\n"), 0o600))
			}
			if tt.directory {
				require.NoError(t, os.Mkdir(input, 0o700))
			}
			err := renderConfig(template, output, tt.hostname, "10.0.0.1", config, input)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				require.NotContains(t, err.Error(), "sentinel-sensitive-value")
				_, statErr := os.Stat(output)
				require.True(t, os.IsNotExist(statErr), "failed preflight must not publish configuration")
				return
			}
			require.NoError(t, err)
			rendered, err := os.ReadFile(output)
			require.NoError(t, err)
			require.NotContains(t, string(rendered), "sentinel-sensitive-value")
		})
	}
}

func TestSecretInputWiring(t *testing.T) {
	inputPath := constants.PathSelfInitInputs + "/selected-input"
	content := []byte(fmt.Sprintf(`initialize "bootstrap" {
  request "login" {
    data {
      password = { eval_source = "file", eval_type = "string", path = %q }
    }
  }
}
`, inputPath))
	for _, tt := range []struct {
		name      string
		files     []string
		wantError bool
	}{
		{name: "matching projection", files: []string{inputPath}},
		{name: "new reference with stale template", files: []string{constants.PathSelfInitInputs + "/old-input"},
			wantError: true},
		{name: "new input added to template without inputs", wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSelfInitSecretWiring(content, tt.files)
			if tt.wantError {
				require.ErrorContains(t, err, "does not match the Pod projections")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
