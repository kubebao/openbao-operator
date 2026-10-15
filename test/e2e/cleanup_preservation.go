//go:build e2e

package e2e

import (
	"bytes"
	"os"
)

// Workers share this file through suiteBootstrap. A worker that cannot clean up
// its fixtures must also prevent node 1 from deleting their evidence.
func requestCleanupPreservation(path string) error {
	return os.WriteFile(path, []byte("preserve\n"), 0o600)
}

func cleanupPreservationRequested(path string) bool {
	state, err := os.ReadFile(path)
	return err != nil || !bytes.Equal(state, []byte("cleanup\n"))
}
