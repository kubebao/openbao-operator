//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestE2EFixtureTwoStepUpgradeVersions(t *testing.T) {
	t.Setenv("E2E_UPGRADE_SEQUENCE", "")
	// Pair overrides cannot supply a three-version sequence.
	for _, key := range []string{"E2E_UPGRADE_FROM_VERSION", "E2E_BLUEGREEN_UPGRADE_FROM_VERSION"} {
		t.Setenv(key, "2.6.4")
	}
	for _, key := range []string{"E2E_UPGRADE_TO_VERSION", "E2E_BLUEGREEN_UPGRADE_TO_VERSION"} {
		t.Setenv(key, "2.7.1")
	}
	got, err := twoStepUpgradeVersions()
	require.NoError(t, err)
	require.Equal(t, [3]string{"2.4.4", "2.5.5", "2.6.4"}, got)

	t.Setenv("E2E_UPGRADE_SEQUENCE", "2.5.5,2.6.4,2.7.1")
	got, err = twoStepUpgradeVersions()
	require.NoError(t, err)
	require.Equal(t, [3]string{"2.5.5", "2.6.4", "2.7.1"}, got)

	t.Setenv("E2E_UPGRADE_SEQUENCE", "2.6.4,2.7.1")
	_, err = twoStepUpgradeVersions()
	require.Error(t, err)
}
