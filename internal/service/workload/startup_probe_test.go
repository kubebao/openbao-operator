package workload

import (
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestStartupProbeBudget(t *testing.T) {
	for _, tt := range []struct {
		name      string
		probes    *api.ProbeConfig
		threshold int32
	}{
		{name: "omitted", threshold: 60},
		{name: "empty configuration", probes: &api.ProbeConfig{}, threshold: 60},
		{name: "default before API defaulting", probes: &api.ProbeConfig{Startup: &api.StartupProbeConfig{}}, threshold: 60},
		{name: "fifteen minute startup", probes: &api.ProbeConfig{Startup: &api.StartupProbeConfig{FailureThreshold: 180}}, threshold: 180},
		{name: "one hour startup", probes: &api.ProbeConfig{Startup: &api.StartupProbeConfig{FailureThreshold: 720}}, threshold: 720},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("slow-start", "default")
			cluster.Spec.Probes = tt.probes
			set, err := buildStatefulSet(cluster, "test-config", true, "", "", "")
			require.NoError(t, err)
			probe := set.Spec.Template.Spec.Containers[0].StartupProbe
			require.Equal(t, tt.threshold, probe.FailureThreshold)
			require.EqualValues(t, 10, probe.InitialDelaySeconds)
			require.EqualValues(t, 5, probe.PeriodSeconds)
			require.EqualValues(t, 10, probe.TimeoutSeconds)
			require.Contains(t, probe.Exec.Command, "-mode=startup")
			require.EqualValues(t, 6, set.Spec.Template.Spec.Containers[0].LivenessProbe.FailureThreshold)
			require.EqualValues(t, 6, set.Spec.Template.Spec.Containers[0].ReadinessProbe.FailureThreshold)
		})
	}
}
