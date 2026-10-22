// Package upgradeversions validates the three versions used by two-step E2E upgrades.
package upgradeversions

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/version"
)

// DefaultSequence preserves the historical two-step regression coverage.
const DefaultSequence = "2.4.4,2.5.5,2.6.4"

// Parse requires exactly three increasing semantic versions without build metadata.
func Parse(sequence string) ([3]string, error) {
	return parse(sequence, false)
}

// ParseStrategySwitch applies the operator's current BlueGreen version restriction.
func ParseStrategySwitch(sequence string) ([3]string, error) {
	return parse(sequence, true)
}

func parse(sequence string, strategySwitch bool) ([3]string, error) {
	var result [3]string
	parts := strings.Split(sequence, ",")
	if len(parts) != len(result) {
		return result, fmt.Errorf("upgrade sequence requires three comma-separated versions")
	}
	var previous *version.Version
	for i, part := range parts {
		part = strings.TrimSpace(part)
		parsed, err := version.ParseSemantic(part)
		if err != nil || part != parsed.String() || parsed.BuildMetadata() != "" {
			return result, fmt.Errorf("upgrade sequence contains invalid version %q", part)
		}
		if previous != nil && !previous.LessThan(parsed) {
			return result, fmt.Errorf("upgrade sequence must increase at each step: %q", sequence)
		}
		// Match the operator's version policy, including 2.6 prereleases.
		if strategySwitch && i == 1 &&
			(previous.Major() < 2 || (previous.Major() == 2 && previous.Minor() < 6)) &&
			(parsed.Major() > 2 || (parsed.Major() == 2 && parsed.Minor() >= 6)) {
			return result, fmt.Errorf(
				"strategy-switch BlueGreen step from %s to %s is blocked by the operator's OpenBao 2.6 compatibility policy",
				previous, parsed,
			)
		}
		result[i], previous = part, parsed
	}
	return result, nil
}
