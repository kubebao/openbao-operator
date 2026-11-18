//go:build apimigration_e2e || integration

package main

// Seed defaults before recording workload identities. Creating configuration
// later just to prove controller login would otherwise change rendered HCL.
func migrationFixtureConfiguration() map[string]any {
	return map[string]any{"logLevel": "info", "ui": true}
}
