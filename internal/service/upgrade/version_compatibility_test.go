package upgrade

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"

	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
)

func TestUpgradeRequestForwardingCompatibility(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, current, target string
		blocked               bool
	}{
		{name: "new cluster", target: "2.7.2"},
		{name: "unchanged legacy server during operator migration", current: "2.5.5", target: "2.5.5"},
		{name: "legacy generation", current: "2.4.4", target: "2.5.5"},
		{name: "protocol boundary", current: "2.5.5", target: "2.6.0", blocked: true},
		{name: "reproduced pair", current: "2.5.5", target: "2.6.5", blocked: true},
		{name: "later minor retains restriction", current: "2.5.5", target: "2.7.2", blocked: true},
		{name: "later major retains restriction", current: "2.5.5", target: "3.0.0", blocked: true},
		{name: "version prefixes", current: "v2.4.4", target: "v2.6.5", blocked: true},
		{name: "prerelease boundary", current: "2.5.5", target: "2.6.0-rc.1", blocked: true},
		{name: "same protocol prerelease", current: "2.6.0-rc.1", target: "2.6.0"},
		{name: "maintained patch", current: "2.6.4", target: "2.6.5"},
		{name: "maintained minor", current: "2.6.5", target: "2.7.2"},
		{name: "latest patch", current: "2.7.1", target: "2.7.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateUpgradeTargetVersion(logr.Discard(), tc.current, tc.target)
			if !tc.blocked {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, operatorerrors.ErrPermanentConfig)
			reason, ok := operatorerrors.Reason(err)
			require.True(t, ok)
			require.Equal(t, ReasonUpgradeVersionIncompatible, reason)
			require.Contains(t, err.Error(), tc.current)
			require.Contains(t, err.Error(), tc.target)
		})
	}
}
