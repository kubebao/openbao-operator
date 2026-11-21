//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"

	"github.com/kubebao/openbao-operator/test/e2e/upgradeversions"
)

const defaultOpenBaoVersion = "2.6.5"

const defaultRestoreOpenBaoVersion = "2.7.2"

// defaultUpgradeFromVersion is the version to start with for upgrade tests.
// This should be set to a stable, known-good version.
const defaultUpgradeFromVersion = "2.6.4"

// defaultUpgradeToVersion is the target version for upgrade tests.
// This should be set to a newer version than defaultUpgradeFromVersion.
// Update this when new OpenBao versions are released.
const defaultUpgradeToVersion = "2.6.5"

// Both strategies use a maintained patch pair without crossing the pre-2.6
// request-forwarding protocol boundary. A separate regression checks rejection.
const (
	defaultBlueGreenUpgradeFromVersion = "2.6.4"
	defaultBlueGreenUpgradeToVersion   = "2.6.5"
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

// twoStepUpgradeVersions is independent of the single-hop upgrade overrides.
func twoStepUpgradeVersions() ([3]string, error) {
	sequence := envOrDefault("E2E_UPGRADE_SEQUENCE", upgradeversions.DefaultSequence)
	return upgradeversions.Parse(sequence)
}

func strategySwitchUpgradeVersions() ([3]string, error) {
	sequence := envOrDefault("E2E_UPGRADE_SEQUENCE", upgradeversions.DefaultSequence)
	return upgradeversions.ParseStrategySwitch(sequence)
}
