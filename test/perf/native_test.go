package perf

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestBoundedDNSLabelSanitizesRunIDs(t *testing.T) {
	t.Parallel()

	got := boundedDNSLabel("Run_2026-05-29T10:20:30Z/Alpha.Beta ")
	want := "run-2026-05-29t10-20-30z-alpha-beta"
	if got != want {
		t.Fatalf("boundedDNSLabel() = %q, want %q", got, want)
	}
	if got := boundedDNSLabel(strings.Repeat("a", 80)); len(got) > 63 {
		t.Fatalf("boundedDNSLabel() length = %d, want <= 63", len(got))
	}
	if got := boundedDNSLabel("$$$"); got != "perf" {
		t.Fatalf("boundedDNSLabel() for invalid value = %q, want perf", got)
	}
}

func TestNativeResourceNameLeavesRoomForPodSuffixes(t *testing.T) {
	t.Parallel()

	got := nativeResourceName("perf-life", strings.Repeat("long-run-id-", 8))
	if len(got) > 40 {
		t.Fatalf("nativeResourceName() length = %d, want <= 40", len(got))
	}
	if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
		t.Fatalf("nativeResourceName() = %q, should not have edge hyphens", got)
	}
}

func TestTenantChurnNamespaceNameIsStableAndBounded(t *testing.T) {
	t.Parallel()

	native := &nativeScenarioContext{runID: strings.Repeat("tenant-run-", 8)}
	got := native.tenantChurnNamespaceName(8)
	if len(got) > 40 {
		t.Fatalf("tenantChurnNamespaceName() length = %d, want <= 40", len(got))
	}
	if !strings.HasPrefix(got, "perf-tenant-09-") {
		t.Fatalf("tenantChurnNamespaceName() = %q, want indexed prefix", got)
	}
}

func TestNativeSelfInitRequestsUseJSONPayload(t *testing.T) {
	t.Parallel()

	requests := nativeSelfInitRequests("perf-example")
	var rolePayload map[string]any
	for _, request := range requests {
		if request.Name != "create-perf-role" {
			continue
		}
		if request.Data == nil {
			t.Fatalf("create-perf-role Data is nil")
		}
		if err := json.Unmarshal(request.Data.Raw, &rolePayload); err != nil {
			t.Fatalf("unmarshal create-perf-role Data: %v", err)
		}
	}
	if rolePayload == nil {
		t.Fatalf("create-perf-role request missing")
	}
	if got, want := rolePayload["bound_subject"], "system:serviceaccount:perf-example:default"; got != want {
		t.Fatalf("bound_subject = %v, want %q", got, want)
	}
}

func TestNativeClustersUseTargetSelfInit(t *testing.T) {
	t.Parallel()

	native := &nativeScenarioContext{namespace: "perf-test", opts: Config{
		UpgradeFromVersion: "2.6.1", UpgradeFromImage: "openbao/openbao:2.6.1",
	}}
	for _, cluster := range []*api.OpenBaoCluster{
		native.buildCluster("perf-life", "2.6.4", "openbao/openbao:2.6.4", 1),
		native.buildRollingUpgradeCluster(),
	} {
		t.Run(cluster.Name, func(t *testing.T) {
			require.Equal(t, api.ControllerJWTModeTarget, cluster.Spec.ControllerJWTMode)
			require.NotNil(t, cluster.Spec.SelfInit)
			require.True(t, cluster.Spec.SelfInit.Enabled)
			require.NotNil(t, cluster.Spec.SelfInit.OIDC)
			require.True(t, cluster.Spec.SelfInit.OIDC.Enabled)
			require.NotEmpty(t, cluster.Spec.SelfInit.Requests)
		})
	}
}

func TestWorkloadObservabilityMetricsOnlyListenerIsVersionAware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		version         string
		existingContext string
		wantConfig      bool
		wantListener    bool
	}{
		{
			name:         "pre 2.5 omits metrics only listener",
			version:      "2.4.4",
			wantConfig:   true,
			wantListener: false,
		},
		{
			name:         "2.5 enables metrics only listener",
			version:      "2.5.0",
			wantConfig:   true,
			wantListener: true,
		},
		{
			name:         "previous stable release keeps metrics only listener",
			version:      "2.6.0",
			wantConfig:   true,
			wantListener: true,
		},
		{
			name:         "invalid version omits metrics only listener",
			version:      "custom",
			wantConfig:   true,
			wantListener: false,
		},
		{
			name:            "existing cluster leaves observability unmanaged",
			version:         "2.6.2",
			existingContext: "kind-existing",
			wantConfig:      false,
			wantListener:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			native := &nativeScenarioContext{
				opts: Config{ExistingClusterContext: tt.existingContext},
			}
			got := native.workloadObservability(tt.version)
			if !tt.wantConfig {
				if got != nil {
					t.Fatalf("workloadObservability(%q) = %#v, want nil", tt.version, got)
				}
				return
			}
			if got == nil || got.Metrics == nil {
				t.Fatalf("workloadObservability(%q) missing metrics config: %#v", tt.version, got)
			}
			if got.Metrics.Enabled != true {
				t.Fatalf("metrics enabled = %v, want true", got.Metrics.Enabled)
			}
			if got.Metrics.ScrapeProfile != "Active" {
				t.Fatalf("scrape profile = %q, want Active", got.Metrics.ScrapeProfile)
			}
			listener := got.Metrics.MetricsOnlyListener
			if !tt.wantListener {
				if listener != nil {
					t.Fatalf("metrics only listener = %#v, want nil", listener)
				}
				return
			}
			if listener == nil || listener.Enabled == nil || !*listener.Enabled {
				t.Fatalf("metrics only listener not enabled: %#v", listener)
			}
			if listener.UnauthenticatedMetricsAccess == nil || !*listener.UnauthenticatedMetricsAccess {
				t.Fatalf("unauthenticated metrics access not enabled: %#v", listener)
			}
		})
	}
}

func TestRollingUpgradeClusterEnablesPublicService(t *testing.T) {
	t.Parallel()

	native := &nativeScenarioContext{
		namespace: "perf-test",
		runID:     "rolling-probe",
		opts: Config{
			UpgradeFromVersion:   "2.6.1",
			UpgradeFromImage:     "ghcr.io/openbao/openbao:2.6.1",
			UpgradeExecutorImage: "ghcr.io/kubebao/openbao-operator-upgrade:dev",
			ConfigInitImage:      "ghcr.io/kubebao/openbao-operator-config-init:dev",
		},
	}

	cluster := native.buildRollingUpgradeCluster()
	if cluster.Spec.Service == nil {
		t.Fatalf("rolling upgrade cluster should enable public service for availability probe")
	}
	if cluster.Spec.Service.Type != "" {
		t.Fatalf("public service type = %q, want operator default", cluster.Spec.Service.Type)
	}
	if cluster.Spec.Upgrade == nil || cluster.Spec.Upgrade.Image != native.opts.UpgradeExecutorImage {
		t.Fatalf("upgrade config = %#v, want executor image %q", cluster.Spec.Upgrade, native.opts.UpgradeExecutorImage)
	}
	if cluster.Spec.Replicas != 3 {
		t.Fatalf("replicas = %d, want 3", cluster.Spec.Replicas)
	}
}

func TestPhaseMeasurements(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)
	got := phaseMeasurements(map[string]time.Time{
		"start": start,
		"ready": start.Add(42 * time.Second),
	}, "start", map[string]string{
		"ready_seconds":   "ready",
		"missing_seconds": "missing",
	})

	assertApproxEqual(t, got["ready_seconds"], 42)
	if _, exists := got["missing_seconds"]; exists {
		t.Fatalf("phaseMeasurements() included missing phase")
	}
}

func TestPhaseMeasurementsClampNegativeDurations(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)
	got := phaseMeasurements(map[string]time.Time{
		"start":   start,
		"started": start.Add(-500 * time.Millisecond),
	}, "start", map[string]string{
		"started_seconds": "started",
	})

	assertApproxEqual(t, got["started_seconds"], 0)
}

func TestDurationPercentileSeconds(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)
	values := []time.Time{
		start.Add(1 * time.Second),
		start.Add(5 * time.Second),
		start.Add(9 * time.Second),
		start.Add(13 * time.Second),
	}

	assertApproxEqual(t, durationPercentileSeconds(start, values, 0.50), 5)
	assertApproxEqual(t, durationPercentileSeconds(start, values, 0.95), 13)
}

func TestResourceWriteTrackerCountsResourceVersionChanges(t *testing.T) {
	t.Parallel()

	tracker := newResourceWriteTracker()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:            "openbao-0",
		Namespace:       "perf",
		ResourceVersion: "1",
	}}

	tracker.track("Pod", pod)
	tracker.track("Pod", pod)
	if tracker.count != 1 {
		t.Fatalf("tracker count = %d, want 1 for duplicate resource version", tracker.count)
	}

	pod.ResourceVersion = "2"
	tracker.track("Pod", pod)
	if tracker.count != 2 {
		t.Fatalf("tracker count = %d, want 2 after resource version change", tracker.count)
	}
}

func assertApproxEqual(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.0001 {
		t.Fatalf("got %.6f, want %.6f", got, want)
	}
}
