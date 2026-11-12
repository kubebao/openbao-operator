package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestRelativeStateSupportsAbsoluteManifestInputs(t *testing.T) {
	t.Chdir(t.TempDir())
	state, err := openState(filepath.Join("migration states", "current"), true)
	require.NoError(t, err)
	t.Cleanup(state.Close)
	p := checkpointUXProcedure(t, "")
	p.state = state
	p.plan.Operator.Install, p.plan.Operator.Flavour = installManifests, flavourKustomize
	inputs := t.TempDir()
	p.plan.Config.SourceManifests = filepath.Join(inputs, "source manifests")
	p.plan.Config.DestinationManifests = filepath.Join(inputs, "destination manifests")
	for _, path := range []string{p.plan.Config.SourceManifests, p.plan.Config.DestinationManifests} {
		require.NoError(t, os.MkdirAll(path, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(path, "kustomization.yaml"),
			[]byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n"), 0o600))
	}
	for _, checkpoint := range []operatorState{stateStopped, stateHeld, stateNormal, stateSource} {
		t.Run(checkpoint.name, func(t *testing.T) {
			_, err := p.writeCheckpoint(checkpoint)
			require.NoError(t, err)
			path := p.state.path(filepath.Join("checkpoints", checkpoint.name, "kustomization.yaml"))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			var rendered struct {
				Resources []string `json:"resources"`
			}
			require.NoError(t, yaml.Unmarshal(data, &rendered))
			require.Len(t, rendered.Resources, 1)
			resolved, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(path), rendered.Resources[0]))
			require.NoError(t, err)
			input := p.plan.Config.DestinationManifests
			if checkpoint == stateSource {
				input = p.plan.Config.SourceManifests
			}
			want, err := filepath.EvalSymlinks(input)
			require.NoError(t, err)
			require.Equal(t, want, resolved)
		})
	}
}
