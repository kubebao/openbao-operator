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
		t.Setenv(key, "2.7.2")
	}
	got, err := twoStepUpgradeVersions()
	require.NoError(t, err)
	require.Equal(t, [3]string{"2.4.4", "2.5.5", "2.6.5"}, got)

	t.Setenv("E2E_UPGRADE_SEQUENCE", "2.5.5,2.6.4,2.7.2")
	got, err = twoStepUpgradeVersions()
	require.NoError(t, err)
	require.Equal(t, [3]string{"2.5.5", "2.6.4", "2.7.2"}, got)

	t.Setenv("E2E_UPGRADE_SEQUENCE", "2.6.4,2.7.2")
	_, err = twoStepUpgradeVersions()
	require.Error(t, err)
}

func TestE2EFixtureStrategySwitchUpgradeVersions(t *testing.T) {
	t.Setenv("E2E_UPGRADE_SEQUENCE", "")
	got, err := strategySwitchUpgradeVersions()
	require.NoError(t, err)
	require.Equal(t, [3]string{"2.4.4", "2.5.5", "2.6.5"}, got)

	t.Setenv("E2E_UPGRADE_SEQUENCE", "2.6.4,2.7.0,2.7.2")
	got, err = strategySwitchUpgradeVersions()
	require.NoError(t, err)
	require.Equal(t, [3]string{"2.6.4", "2.7.0", "2.7.2"}, got)

	t.Setenv("E2E_UPGRADE_SEQUENCE", "2.5.5,2.6.4,2.7.2")
	_, err = strategySwitchUpgradeVersions()
	require.ErrorContains(t, err, "BlueGreen step")
	_, err = twoStepUpgradeVersions()
	require.NoError(t, err)
}
