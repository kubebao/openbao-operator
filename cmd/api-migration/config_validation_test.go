package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfigRequiresOneDocument(t *testing.T) {
	data, err := json.Marshal(validConfig())
	require.NoError(t, err)
	for _, suffix := range []string{"\n{}", "\ntrailing text"} {
		path := filepath.Join(t.TempDir(), "config.json")
		require.NoError(t, os.WriteFile(path, append(data, []byte(suffix)...), 0o600))
		_, err := loadConfig(path)
		require.ErrorContains(t, err, "exactly one JSON object")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, append(data, []byte("\n \t\n")...), 0o600))
	_, err = loadConfig(path)
	require.NoError(t, err)
}
