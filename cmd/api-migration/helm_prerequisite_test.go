package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestHelmCommandsRecheckTheExecutableBeforeChangingState(t *testing.T) {
	for _, tc := range []struct {
		command, phase string
	}{
		{commandMigrate, phasePlanned},
		{commandMigrate, phaseMigrating},
		{commandMigrate, phaseVerified},
		{commandCommit, phaseVerified},
		{commandCommit, phaseCommitting},
		{commandRollback, phasePlanned},
		{commandRollback, phaseMigrating},
		{commandRollback, phaseVerified},
		{commandRollback, phaseCommitting},
		{commandRollback, phaseRollingBack},
	} {
		t.Run(tc.command+"/"+tc.phase, func(t *testing.T) {
			bin := t.TempDir()
			helm := filepath.Join(bin, "helm")
			writeHelm := func(version string) {
				t.Helper()
				script := "#!/bin/sh\n[ \"$1\" = version ] || exit 91\nprintf '%s\\n' '" + version + "'\n"
				require.NoError(t, os.WriteFile(helm, []byte(script), 0o700))
			}
			t.Setenv("PATH", bin)
			writeHelm("v4.3.1")
			p, source := retirementFixture(t, clusterKind, "source", clusterFinalizer)
			p.plan.Version, p.plan.Phase = planVersion, tc.phase
			p.plan.Operator.Install = installHelm
			state, err := openState(filepath.Join(t.TempDir(), "state"), true)
			require.NoError(t, err)
			p.state = state
			t.Cleanup(state.Close)
			require.NoError(t, p.state.Save(p.plan))
			before, err := os.ReadFile(p.state.path(planFile))
			require.NoError(t, err)
			sourceBefore := source.DeepCopy()
			require.NoError(t, p.kube.client.Get(t.Context(), client.ObjectKeyFromObject(source), sourceBefore))

			// Planning used Helm 4, but a later invocation finds Helm 3 on PATH.
			version, err := p.helm.Version(t.Context())
			require.NoError(t, err)
			require.NoError(t, requireHelm4(version))
			writeHelm("v3.20.2")
			switch tc.command {
			case commandMigrate:
				err = p.Migrate(t.Context())
			case commandCommit:
				err = p.Commit(t.Context())
			case commandRollback:
				err = p.Rollback(t.Context())
			}
			require.ErrorContains(t, err, "requires Helm 4")
			require.Equal(t, tc.phase, p.plan.Phase)
			after, err := os.ReadFile(p.state.path(planFile))
			require.NoError(t, err)
			require.Equal(t, before, after, "a prerequisite failure must preserve saved progress")
			require.NoError(t, p.kube.client.Get(t.Context(), client.ObjectKeyFromObject(source), source))
			require.Equal(t, sourceBefore, source, "a prerequisite failure must preserve the source")
		})
	}
}
