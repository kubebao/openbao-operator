package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSummarizeValues(t *testing.T) {
	got := summarizeValues([]float64{10, 4, 6})
	if got.Median != 6 {
		t.Fatalf("median = %v, want 6", got.Median)
	}
	if got.UpperSample != 10 {
		t.Fatalf("upper sample = %v, want 10", got.UpperSample)
	}
	if got.Min != 4 || got.Max != 10 || got.Count != 3 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestAnalyzeScenarioReportsWarmupFailuresWithoutMeasuringWarmups(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		warmupStatus string
		measured     bool
		wantStatus   string
		wantFinding  string
	}{
		{"failed warmup only", sampleStatusScenarioError, false, measurementSeverityFail, "warmup 1: setup failed"},
		{"failed warmup followed by sample", sampleStatusScenarioError, true,
			measurementSeverityFail, "warmup 1: setup failed"},
		{"measurement error followed by sample", sampleStatusMeasurementError, true,
			measurementSeverityWarn, "warmup 1: setup failed"},
		{"successful warmup only", sampleStatusPass, false, measurementSeverityFail, "scenario produced no measured samples"},
		{"successful warmup excluded", sampleStatusPass, true, sampleStatusPass, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec := scenarioSpec{Name: "restore", Primary: []string{metricRestoreTotalSeconds}}
			samples := []sampleDocument{{Scenario: spec.Name, Sample: 1, Warmup: true, Status: tt.warmupStatus,
				Error: "setup failed", Measurements: map[string]float64{metricRestoreTotalSeconds: 900}}}
			if tt.measured {
				samples = append(samples, sampleDocument{Scenario: spec.Name, Sample: 1, Status: sampleStatusPass,
					Measurements: map[string]float64{metricRestoreTotalSeconds: 30}})
			}
			policy := policyDocument{Defaults: measurementPolicy{Policy: measurementPolicyInformational, MinimumSamples: 1}}
			got := analyzeScenarioSamples(spec, samples, baselineDocument{}, nil, policy)
			if got.Status != tt.wantStatus || got.Warmups != 1 {
				t.Fatalf("summary = %+v, want status %q and one warmup", got, tt.wantStatus)
			}
			if tt.measured && (got.Samples != 1 || got.Measurements[metricRestoreTotalSeconds].Median != 30) {
				t.Fatalf("warmup affected measured results: %+v", got)
			}
			if !tt.measured && (got.Samples != 0 || len(got.Measurements) != 0) {
				t.Fatalf("warmup was included in measurements: %+v", got)
			}
			if tt.wantFinding != "" && (len(got.Findings) == 0 || got.Findings[0].Message != tt.wantFinding) {
				t.Fatalf("findings = %+v, want %q", got.Findings, tt.wantFinding)
			}
		})
	}
}

func TestWriteScenarioBaselineRequiresMinimumSuccessfulSamples(t *testing.T) {
	t.Parallel()

	opts := defaultOptions("capture")
	opts.BaselineDir = t.TempDir()
	opts.MinimumSuccessfulSamples = 2
	scenario := scenarioSpec{
		Name:    "lifecycle-convergence",
		Primary: []string{metricClusterAvailableSeconds},
	}
	samples := []sampleDocument{
		{
			Version:     versionV2,
			Scenario:    scenario.Name,
			Sample:      1,
			Status:      sampleStatusPass,
			Environment: runEnvironment{Commit: "abc123"},
			Measurements: map[string]float64{
				metricClusterAvailableSeconds: 30,
			},
		},
		{
			Version:      versionV2,
			Scenario:     scenario.Name,
			Sample:       2,
			Status:       sampleStatusScenarioError,
			Error:        "setup failed",
			Measurements: map[string]float64{},
		},
	}

	err := writeScenarioBaseline(opts, scenario, samples)
	if err == nil {
		t.Fatalf("expected minimum successful sample error")
	}
	if !strings.Contains(err.Error(), "produced 1 passing measured samples, need at least 2") {
		t.Fatalf("unexpected error: %v", err)
	}

	opts.MinimumSuccessfulSamples = 1
	if err := writeScenarioBaseline(opts, scenario, samples); err != nil {
		t.Fatalf("write baseline with enough samples: %v", err)
	}
	doc, err := readScenarioBaseline(opts, scenario.Name)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if got := len(doc.Samples[metricClusterAvailableSeconds]); got != 1 {
		t.Fatalf("baseline sample count = %d, want 1", got)
	}
}

func TestCompareMeasurementsRequiresAbsoluteAndRelativeRegression(t *testing.T) {
	current := map[string]measurementSummary{
		metricSampleTotalSeconds: {Median: 125, UpperSample: 125, Min: 125, Max: 125, Count: 3},
	}
	baseline := baselineDocument{
		Summary: map[string]measurementSummary{
			metricSampleTotalSeconds: {Median: 100, UpperSample: 100, Min: 100, Max: 100, Count: 7},
		},
	}
	policy := policyDocument{
		Measurements: map[string]measurementPolicy{
			metricSampleTotalSeconds: {
				Policy:          measurementPolicyUpperBound,
				Severity:        measurementSeverityFail,
				Compare:         compareMedian,
				AllowedRelative: 0.20,
				AllowedAbsolute: 30,
				MinimumSamples:  3,
			},
		},
	}

	if findings := compareMeasurements("lifecycle", current, baseline, policy); len(findings) != 0 {
		t.Fatalf("relative-only regression should not fail: %v", findings)
	}

	current[metricSampleTotalSeconds] = measurementSummary{Median: 140, UpperSample: 140, Min: 140, Max: 140, Count: 3}
	findings := compareMeasurements("lifecycle", current, baseline, policy)
	if len(findings) != 1 {
		t.Fatalf("findings len = %d, want 1 (%v)", len(findings), findings)
	}
	if findings[0].Severity != measurementSeverityFail {
		t.Fatalf("severity = %q, want %q", findings[0].Severity, measurementSeverityFail)
	}
}

func TestReportSummarizesSyntheticSamples(t *testing.T) {
	tmp := t.TempDir()
	artifactDir := filepath.Join(tmp, "dist", "perf")
	baselineDir := filepath.Join(tmp, "baselines")
	policyPath := filepath.Join(tmp, "weekly.yaml")
	scenarioPath := filepath.Join(tmp, "scenarios.yaml")

	writeTestFile(t, scenarioPath, `
version: v2
scenarios:
  - name: lifecycle-convergence
    executor: native-go
    primaryMeasurements:
      - sample_total_seconds
`)
	writeTestFile(t, policyPath, `
version: v2
measurements:
  sample_total_seconds:
    policy: upper_bound
    severity: warn
    compare: median
    allowedRelativeRegression: 0.5
    allowedAbsoluteRegressionSeconds: 10
    minimumSamples: 1
`)
	writeTestJSON(t, filepath.Join(baselineDir, "lifecycle-convergence", "kind-v1.34.3.json"), baselineDocument{
		Version:    versionV2,
		Scenario:   "lifecycle-convergence",
		CapturedAt: time.Unix(0, 0).UTC(),
		Samples: map[string][]float64{
			metricSampleTotalSeconds: {100, 102, 104},
		},
		Summary: map[string]measurementSummary{
			metricSampleTotalSeconds: {Median: 102, UpperSample: 104, Min: 100, Max: 104, Count: 3},
		},
	})
	writeTestJSON(t, filepath.Join(artifactDir, "scenarios", "lifecycle-convergence", "sample-001.json"), sampleDocument{
		Version:  versionV2,
		Scenario: "lifecycle-convergence",
		Sample:   1,
		Status:   sampleStatusPass,
		Measurements: map[string]float64{
			metricSampleTotalSeconds: 105,
		},
	})

	opts := defaultOptions("report")
	opts.ArtifactDir = artifactDir
	opts.BaselineDir = baselineDir
	opts.PolicyPath = policyPath
	opts.ScenarioPath = scenarioPath
	opts.ReportOut = filepath.Join(tmp, "report.md")
	opts.SummaryOut = filepath.Join(tmp, "summary.json")

	if err := runReport(opts); err != nil {
		t.Fatalf("runReport() error = %v", err)
	}
	report, err := os.ReadFile(opts.ReportOut)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(report), "lifecycle-convergence") {
		t.Fatalf("report should mention scenario, got:\n%s", string(report))
	}
	if !strings.Contains(string(report), metricSampleTotalSeconds) {
		t.Fatalf("report should mention measurement, got:\n%s", string(report))
	}
}

func TestSummarizeRunUsesScenarioMeasurementContract(t *testing.T) {
	tmp := t.TempDir()
	artifactDir := filepath.Join(tmp, "dist", "perf")
	baselineDir := filepath.Join(tmp, "baselines")
	policyPath := filepath.Join(tmp, "weekly.yaml")
	scenarioPath := filepath.Join(tmp, "scenarios.yaml")

	writeTestFile(t, scenarioPath, `
version: v2
scenarios:
  - name: contract
    executor: native-go
    primaryMeasurements:
      - expected_metric
      - missing_metric
    diagnosticMeasurements:
      - diagnostic_metric
`)
	writeTestFile(t, policyPath, `
version: v2
measurements:
  expected_metric:
    policy: upper_bound
    severity: warn
    compare: median
    allowedRelativeRegression: 1.0
    minimumSamples: 1
  missing_metric:
    policy: upper_bound
    severity: warn
    compare: median
    allowedRelativeRegression: 1.0
    minimumSamples: 1
  diagnostic_metric:
    policy: informational
    severity: info
`)
	writeTestJSON(t, filepath.Join(baselineDir, "contract", "kind-v1.34.3.json"), baselineDocument{
		Version:    versionV2,
		Scenario:   "contract",
		CapturedAt: time.Unix(0, 0).UTC(),
		Summary: map[string]measurementSummary{
			"expected_metric": {Median: 100, UpperSample: 100, Min: 100, Max: 100, Count: 3},
			"missing_metric":  {Median: 10, UpperSample: 10, Min: 10, Max: 10, Count: 3},
		},
	})
	writeTestJSON(t, filepath.Join(artifactDir, "scenarios", "contract", "sample-001.json"), sampleDocument{
		Version:  versionV2,
		Scenario: "contract",
		Sample:   1,
		Status:   sampleStatusPass,
		Measurements: map[string]float64{
			"expected_metric":  100,
			"unrelated_metric": 999,
		},
	})

	opts := defaultOptions("report")
	opts.ArtifactDir = artifactDir
	opts.BaselineDir = baselineDir
	opts.PolicyPath = policyPath
	opts.ScenarioPath = scenarioPath

	summary, err := summarizeRun(opts)
	if err != nil {
		t.Fatalf("summarizeRun() error = %v", err)
	}
	scenario := summary.Scenarios["contract"]
	if _, exists := scenario.Measurements["unrelated_metric"]; exists {
		t.Fatalf("unexpected unrelated metric in summary: %+v", scenario.Measurements)
	}
	if _, exists := scenario.Measurements["expected_metric"]; !exists {
		t.Fatalf("expected metric missing from summary: %+v", scenario.Measurements)
	}
	if !hasFinding(scenario.Findings, "missing_metric", "measurement_missing") {
		t.Fatalf("missing measurement finding not present: %+v", scenario.Findings)
	}
}

func TestSummarizeRunEscalatesConsecutivePrimaryRegression(t *testing.T) {
	tmp := t.TempDir()
	artifactDir := filepath.Join(tmp, "dist", "perf")
	baselineDir := filepath.Join(tmp, "baselines")
	policyPath := filepath.Join(tmp, "weekly.yaml")
	scenarioPath := filepath.Join(tmp, "scenarios.yaml")
	previousSummaryPath := filepath.Join(tmp, "previous-summary.json")

	writeTestFile(t, scenarioPath, `
version: v2
scenarios:
  - name: lifecycle-convergence
    executor: native-go
    primaryMeasurements:
      - cluster_available_seconds
    diagnosticMeasurements:
      - observed_kubernetes_writes
`)
	writeTestFile(t, policyPath, `
version: v2
measurements:
  cluster_available_seconds:
    role: primary
    policy: upper_bound
    severity: warn
    compare: median
    allowedRelativeRegression: 0.10
    minimumSamples: 1
  observed_kubernetes_writes:
    role: diagnostic
    policy: upper_bound
    severity: warn
    compare: median
    allowedRelativeRegression: 0.10
    minimumSamples: 1
`)
	writeTestJSON(t, filepath.Join(baselineDir, "lifecycle-convergence", "kind-v1.34.3.json"), baselineDocument{
		Version:    versionV2,
		Scenario:   "lifecycle-convergence",
		CapturedAt: time.Unix(0, 0).UTC(),
		Summary: map[string]measurementSummary{
			"cluster_available_seconds":  {Median: 100, UpperSample: 100, Min: 100, Max: 100, Count: 3},
			"observed_kubernetes_writes": {Median: 100, UpperSample: 100, Min: 100, Max: 100, Count: 3},
		},
	})
	writeTestJSON(t, filepath.Join(artifactDir, "scenarios", "lifecycle-convergence", "sample-001.json"), sampleDocument{
		Version:  versionV2,
		Scenario: "lifecycle-convergence",
		Sample:   1,
		Status:   sampleStatusPass,
		Measurements: map[string]float64{
			"cluster_available_seconds":  150,
			"observed_kubernetes_writes": 150,
		},
	})
	writeTestJSON(t, previousSummaryPath, runSummaryDocument{
		Version: versionV2,
		Scenarios: map[string]scenarioSummary{
			"lifecycle-convergence": {
				Status: measurementSeverityWarn,
				Findings: []analysisFinding{
					{
						Scenario:       "lifecycle-convergence",
						Measurement:    "cluster_available_seconds",
						Severity:       measurementSeverityWarn,
						Classification: findingPerformanceFailure,
						Message:        "previous primary regression",
					},
					{
						Scenario:       "lifecycle-convergence",
						Measurement:    "observed_kubernetes_writes",
						Severity:       measurementSeverityWarn,
						Classification: findingPerformanceFailure,
						Message:        "previous diagnostic regression",
					},
				},
			},
		},
	})

	opts := defaultOptions("report")
	opts.ArtifactDir = artifactDir
	opts.BaselineDir = baselineDir
	opts.PolicyPath = policyPath
	opts.ScenarioPath = scenarioPath
	opts.PreviousSummaryPath = previousSummaryPath

	summary, err := summarizeRun(opts)
	if err != nil {
		t.Fatalf("summarizeRun() error = %v", err)
	}
	scenario := summary.Scenarios["lifecycle-convergence"]
	if scenario.Status != measurementSeverityFail {
		t.Fatalf("scenario status = %q, want %q; findings=%+v", scenario.Status, measurementSeverityFail, scenario.Findings)
	}
	if !hasFinding(scenario.Findings, "cluster_available_seconds", findingPerformanceFailureConsecutive) {
		t.Fatalf("primary consecutive finding not escalated: %+v", scenario.Findings)
	}
	if hasFinding(scenario.Findings, "observed_kubernetes_writes", findingPerformanceFailureConsecutive) {
		t.Fatalf("diagnostic finding should not be escalated: %+v", scenario.Findings)
	}
}

func TestIsTimelineSampleFileRejectsArtifacts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want bool
	}{
		{path: "sample-001.json", want: true},
		{path: "warmup-001.json", want: true},
		{path: "sample-001-events.json", want: false},
		{path: "sample-001-pods.json", want: false},
		{path: "sample-one.json", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := isTimelineSampleFile(tt.path); got != tt.want {
				t.Fatalf("isTimelineSampleFile(%q) = %t, want %t", tt.path, got, tt.want)
			}
		})
	}
}

func hasFinding(findings []analysisFinding, measurement, classification string) bool {
	for _, finding := range findings {
		if finding.Measurement == measurement && finding.Classification == classification {
			return true
		}
	}
	return false
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := writeJSONFile(path, value); err != nil {
		t.Fatalf("write json %s: %v", path, err)
	}
}
