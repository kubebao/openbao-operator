package upgrade

import (
	"fmt"

	"github.com/go-logr/logr"

	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	platformsemver "github.com/kubebao/openbao-operator/internal/platform/semver"
)

// VersionChange represents the type of version change detected.
type VersionChange string

const (
	// VersionChangeNone indicates no version change.
	VersionChangeNone VersionChange = "None"
	// VersionChangePatch indicates a patch-level upgrade (e.g., 2.4.0 -> 2.4.1).
	VersionChangePatch VersionChange = "Patch"
	// VersionChangeMinor indicates a minor-level upgrade (e.g., 2.4.0 -> 2.5.0).
	VersionChangeMinor VersionChange = "Minor"
	// VersionChangeMajor indicates a major-level upgrade (e.g., 2.x -> 3.x).
	VersionChangeMajor VersionChange = "Major"
	// VersionChangeDowngrade indicates a version downgrade.
	VersionChangeDowngrade VersionChange = "Downgrade"
)

// SemVer represents a parsed semantic version.
type SemVer struct {
	Major      int
	Minor      int
	Patch      int
	Prerelease string
	Build      string
}

// String returns the string representation of the version.
func (v SemVer) String() string {
	version := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Prerelease != "" {
		version += "-" + v.Prerelease
	}
	if v.Build != "" {
		version += "+" + v.Build
	}
	return version
}

// ParseVersion parses a semantic version string.
// Supports formats:
// - 2.4.0
// - 2.4.0-rc1
// - 2.4.0+build123
// - 2.4.0-rc1+build123
// - v2.4.0 (optional 'v' prefix)
func ParseVersion(version string) (*SemVer, error) {
	parsed, err := platformsemver.Parse(version)
	if err != nil {
		return nil, err
	}

	return &SemVer{
		Major:      parsed.Major(),
		Minor:      parsed.Minor(),
		Patch:      parsed.Patch(),
		Prerelease: parsed.Prerelease(),
		Build:      parsed.Build(),
	}, nil
}

// ValidateVersion validates that a version string is a valid semantic version.
func ValidateVersion(version string) error {
	_, err := ParseVersion(version)
	return err
}

// ValidateUpgradeTargetVersion enforces version-policy rules for a requested
// target version while logging non-blocking warnings for higher-risk upgrades.
func ValidateUpgradeTargetVersion(logger logr.Logger, currentVersion, targetVersion string) error {
	if err := ValidateVersion(targetVersion); err != nil {
		return operatorerrors.WithReason(
			ReasonInvalidVersion,
			operatorerrors.WrapPermanentConfig(fmt.Errorf(MessageInvalidVersion+": %w", targetVersion, err)),
		)
	}

	if currentVersion == "" {
		return nil
	}

	if IsDowngrade(currentVersion, targetVersion) {
		logger.Info("Downgrade detected and blocked",
			"from", currentVersion,
			"to", targetVersion)
		return operatorerrors.WithReason(
			ReasonDowngradeBlocked,
			operatorerrors.WrapPermanentConfig(fmt.Errorf(MessageDowngradeBlocked, currentVersion, targetVersion)),
		)
	}

	change, err := CompareVersions(currentVersion, targetVersion)
	if err != nil {
		logger.V(1).Info("Skipping version-change classification due to unparsable current version",
			"currentVersion", currentVersion,
			"targetVersion", targetVersion,
			"error", err)
		return nil
	}

	if err := validateVersionCompatibility(currentVersion, targetVersion); err != nil {
		return err
	}

	if change == VersionChangeMajor {
		logger.Info("Major version upgrade detected; proceed with caution",
			"from", currentVersion,
			"to", targetVersion)
	}
	if IsSkipMinorUpgrade(currentVersion, targetVersion) {
		logger.Info("Minor version skip detected; some intermediate versions may be skipped",
			"from", currentVersion,
			"to", targetVersion)
	}

	return nil
}

// CompareVersions compares two version strings and returns the type of change.
// Returns an error if either version is invalid.
func CompareVersions(from, to string) (VersionChange, error) {
	fromVer, err := platformsemver.Parse(from)
	if err != nil {
		return VersionChangeNone, fmt.Errorf("invalid 'from' version: %w", err)
	}

	toVer, err := platformsemver.Parse(to)
	if err != nil {
		return VersionChangeNone, fmt.Errorf("invalid 'to' version: %w", err)
	}

	cmp := toVer.Compare(fromVer)
	switch {
	case cmp < 0:
		return VersionChangeDowngrade, nil
	case cmp == 0:
		return VersionChangeNone, nil
	}

	if toVer.Major() != fromVer.Major() {
		return VersionChangeMajor, nil
	}
	if toVer.Minor() != fromVer.Minor() {
		return VersionChangeMinor, nil
	}
	return VersionChangePatch, nil
}

// IsDowngrade returns true if changing from 'from' to 'to' would be a downgrade.
func IsDowngrade(from, to string) bool {
	change, err := CompareVersions(from, to)
	if err != nil {
		// If we can't parse versions, be conservative and don't consider it a downgrade
		return false
	}
	return change == VersionChangeDowngrade
}

// IsSkipMinorUpgrade returns true if the upgrade skips minor versions.
// For example, 2.4.0 -> 2.6.0 skips 2.5.x.
func IsSkipMinorUpgrade(from, to string) bool {
	fromVer, err := ParseVersion(from)
	if err != nil {
		return false
	}

	toVer, err := ParseVersion(to)
	if err != nil {
		return false
	}

	// Only applies to same-major upgrades
	if fromVer.Major != toVer.Major {
		return false
	}

	// Check if more than one minor version is skipped
	return toVer.Minor-fromVer.Minor > 1
}
