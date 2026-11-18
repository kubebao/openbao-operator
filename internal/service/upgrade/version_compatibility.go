package upgrade

import (
	"fmt"

	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
)

func validateVersionCompatibility(currentVersion, targetVersion string) error {
	current, err := ParseVersion(currentVersion)
	if err != nil {
		return nil
	}
	target, err := ParseVersion(targetVersion)
	if err != nil {
		return nil
	}

	if !isPre26Version(current) || !is26OrNewerVersion(target) {
		return nil
	}

	return operatorerrors.WithReason(
		ReasonUpgradeVersionIncompatible,
		operatorerrors.WrapPermanentConfig(fmt.Errorf(
			MessageUpgradeVersionIncompatible,
			currentVersion,
			targetVersion,
		)),
	)
}

func isPre26Version(version *SemVer) bool {
	if version == nil {
		return false
	}
	return version.Major < 2 || (version.Major == 2 && version.Minor < 6)
}

func is26OrNewerVersion(version *SemVer) bool {
	// Fail closed until an OpenBao release is explicitly verified to preserve
	// mixed-version request-forwarding compatibility with pre-2.6 peers.
	return version != nil &&
		(version.Major > 2 || (version.Major == 2 && version.Minor >= 6))
}
