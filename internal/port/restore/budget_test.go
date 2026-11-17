package restore

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestSnapshotBudget(t *testing.T) {
	for _, tc := range []struct {
		name         string
		maxBytes     int64
		scratchBytes int64
	}{
		{"default", 0, constants.DefaultRestoreScratchLimitBytes},
		{"explicit default", constants.DefaultRestoreSnapshotLimitBytes, constants.DefaultRestoreScratchLimitBytes},
		{"larger than legacy cap", 9 * 1024 * mib, 10138 * mib},
		{"sixteen GiB", 16 * 1024 * mib, 18023 * mib},
		{"round up", 10*mib + 1, 12 * mib},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget, err := ResolveSnapshotBudget(tc.maxBytes)
			require.NoError(t, err)
			require.Positive(t, budget.MaxBytes)
			require.Equal(t, tc.scratchBytes, budget.ScratchBytes)
			require.Equal(t, tc.scratchBytes+restoreRuntimeStorageBytes, budget.EphemeralStorageBytes)
			require.Greater(t, budget.ScratchBytes, budget.MaxBytes)
		})
	}
}

func TestSnapshotBudgetRejectsOverflow(t *testing.T) {
	for _, maxBytes := range []int64{-1, math.MaxInt64, math.MaxInt64 - mib} {
		_, err := ResolveSnapshotBudget(maxBytes)
		require.Error(t, err)
	}
}
