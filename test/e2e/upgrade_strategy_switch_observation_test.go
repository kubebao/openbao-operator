//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestE2EFixtureStrategySwitchBlueGreenRollback(t *testing.T) {
	require.NoError(t, strategySwitchBlueGreenRollbackError(nil))
	for _, phase := range []api.BlueGreenPhase{
		api.PhaseIdle, api.PhaseDeployingGreen, api.PhaseJoiningMesh, api.PhaseSyncing,
		api.PhasePromoting, api.PhaseDemotingBlue, api.PhaseCleanup, api.PhaseRestoringReadReplicas,
	} {
		t.Run(string(phase), func(t *testing.T) {
			// A bounded executor retry is not a terminal upgrade failure.
			status := &api.BlueGreenStatus{Phase: phase, JobFailureCount: 1, LastJobFailure: "retryable-job"}
			require.NoError(t, strategySwitchBlueGreenRollbackError(status))
		})
	}
	for _, phase := range []api.BlueGreenPhase{api.PhaseRollingBack, api.PhaseRollbackCleanup, api.PhaseIdle} {
		t.Run("rollback-"+string(phase), func(t *testing.T) {
			status := &api.BlueGreenStatus{Phase: phase}
			if phase == api.PhaseIdle {
				status.RollbackReason = "demotion failed"
			}
			err := strategySwitchBlueGreenRollbackError(status)
			require.ErrorContains(t, err, "phase="+string(phase))
			if phase == api.PhaseIdle {
				require.ErrorContains(t, err, "demotion failed")
			}
		})
	}
}
