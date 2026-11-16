package workload

import (
	"time"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// Startup probe timings retain the released default budget.
const (
	StartupProbeInitialDelaySeconds     int32 = 10
	StartupProbePeriodSeconds           int32 = 5
	DefaultStartupProbeFailureThreshold int32 = 60
)

// StartupProbeFailureThreshold resolves the optional startup probe budget.
func StartupProbeFailureThreshold(probes *api.ProbeConfig) int32 {
	if probes == nil || probes.Startup == nil || probes.Startup.FailureThreshold == 0 {
		return DefaultStartupProbeFailureThreshold
	}
	return probes.Startup.FailureThreshold
}

// StartupBudgetExtension preserves the existing lifecycle readiness margin when
// the startup budget is extended. Probe scheduling and initial delay stay fixed.
func StartupBudgetExtension(probes *api.ProbeConfig) time.Duration {
	additionalFailures := StartupProbeFailureThreshold(probes) - DefaultStartupProbeFailureThreshold
	return time.Duration(additionalFailures) * time.Duration(StartupProbePeriodSeconds) * time.Second
}
