//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
)

const defaultOpenBaoVersion = "2.6.4"

// defaultUpgradeFromVersion is the version to start with for upgrade tests.
// This should be set to a stable, known-good version.
const defaultUpgradeFromVersion = "2.5.5"

// defaultUpgradeToVersion is the target version for upgrade tests.
// This should be set to a newer version than defaultUpgradeFromVersion.
// Update this when new OpenBao versions are released.
const defaultUpgradeToVersion = "2.6.4"

// OpenBao 2.6.0-2.6.2 cannot exchange Raft Autopilot health with pre-2.6
// peers because the request-forwarding gRPC service name changed. Version
// 2.6.3 fixes forwarding, but a pre-2.6 blue/green transition is not qualified.
// Keep blue/green coverage on the qualified pair and rolling coverage on 2.6.4.
const (
	defaultBlueGreenUpgradeFromVersion = "2.4.4"
	defaultBlueGreenUpgradeToVersion   = "2.5.5"
)

var (
	openBaoVersion       string
	openBaoImage         string
	apiServerCIDR        string
	apiServerEndpointIPs []string
)

func init() {
	openBaoVersion = strings.TrimSpace(os.Getenv("E2E_OPENBAO_VERSION"))
	if openBaoVersion == "" {
		openBaoVersion = defaultOpenBaoVersion
	}

	openBaoImage = strings.TrimSpace(os.Getenv("E2E_OPENBAO_IMAGE"))
	if openBaoImage == "" {
		openBaoImage = fmt.Sprintf("openbao/openbao:%s", openBaoVersion)
	}

	apiServerCIDR = strings.TrimSpace(os.Getenv("E2E_API_SERVER_CIDR"))
}

func blueGreenUpgradeFromVersion() string {
	return envOrDefault(
		"E2E_BLUEGREEN_UPGRADE_FROM_VERSION",
		envOrDefault("E2E_UPGRADE_FROM_VERSION", defaultBlueGreenUpgradeFromVersion),
	)
}

func blueGreenUpgradeToVersion() string {
	return envOrDefault(
		"E2E_BLUEGREEN_UPGRADE_TO_VERSION",
		envOrDefault("E2E_UPGRADE_TO_VERSION", defaultBlueGreenUpgradeToVersion),
	)
}
