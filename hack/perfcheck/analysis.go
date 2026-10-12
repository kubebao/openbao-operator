package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func summarizeRun(opts options) (runSummaryDocument, error) {
	policy, err := loadPolicyDocument(opts.PolicyPath)
	if err != nil {
		return runSummaryDocument{}, err
	}
	manifest, err := loadScenarioManifest(opts.ScenarioPath)
	if err != nil {
		return runSummaryDocument{}, err
	}
	scenariosByName := scenarioMap(manifest.Scenarios)
	samples, err := readSampleDocuments(opts.ArtifactDir)
	if err != nil {
		return runSummaryDocument{}, err
	}
	if len(samples) == 0 {
		return runSummaryDocument{}, fmt.Errorf("no sample artifacts found under %s", opts.ArtifactDir)
	}

	byScenario := make(map[string][]sampleDocument)
	for _, sample := range samples {
		if len(opts.ScenarioNames) > 0 && !scenarioSelected(opts.ScenarioNames, sample.Scenario) {
			continue
		}
		byScenario[sample.Scenario] = append(byScenario[sample.Scenario], sample)
	}
	if len(byScenario) == 0 {
		return runSummaryDocument{}, fmt.Errorf("no selected sample artifacts found under %s", opts.ArtifactDir)
	}

	summary := runSummaryDocument{
		Version:     versionV2,
		GeneratedAt: time.Now().UTC(),
		RunID:       opts.RunID,
		ArtifactDir: opts.ArtifactDir,
		BaselineDir: opts.BaselineDir,
		PolicyPath:  opts.PolicyPath,
		Scenarios:   make(map[string]scenarioSummary, len(byScenario)),
	}
	if strings.TrimSpace(opts.PreviousSummaryPath) != "" {
		summary.PreviousRun = opts.PreviousSummaryPath
	}

	names := make([]string, 0, len(byScenario))
	for name := range byScenario {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, scenarioName := range names {
		scenarioSpec, ok := scenariosByName[scenarioName]
		if !ok {
			return runSummaryDocument{}, fmt.Errorf("sample artifacts contain unknown scenario %q", scenarioName)
		}
		baseline, baselineErr := readScenarioBaseline(opts, scenarioName)
		scenario := analyzeScenarioSamples(scenarioSpec, byScenario[scenarioName], baseline, baselineErr, policy)
		summary.Scenarios[scenarioName] = scenario
		summary.Totals.Scenarios++
		summary.Totals.Samples += scenario.Samples
		switch scenario.Status {
		case measurementSeverityFail:
			summary.Totals.Fail++
		case measurementSeverityWarn:
			summary.Totals.Warn++
		default:
			summary.Totals.Pass++
		}
	}

	if strings.TrimSpace(opts.PreviousSummaryPath) != "" {
		previous, err := readRunSummary(opts.PreviousSummaryPath)
		if err != nil {
			return runSummaryDocument{}, err
		}
		applyConsecutivePrimaryRegressionPolicy(&summary, previous, policy)
	}
	return summary, nil
}

func analyzeScenarioSamples(
	scenarioSpec scenarioSpec,
	samples []sampleDocument,
	baseline baselineDocument,
	baselineErr error,
	policy policyDocument,
) scenarioSummary {
	scenarioName := scenarioSpec.Name
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Warmup != samples[j].Warmup {
			return samples[i].Warmup
		}
		return samples[i].Sample < samples[j].Sample
	})

	scenario := scenarioSummary{
		Status:       sampleStatusPass,
		Measurements: make(map[string]measurementSummary),
	}
	values := make(map[string][]float64)
	var findings []analysisFinding
	allowedMeasurements := scenarioMeasurementSet(scenarioSpec)

	for _, sample := range samples {
		if sample.Warmup {
			scenario.Warmups++
		} else {
			scenario.Samples++
		}
		if sample.Status != sampleStatusPass {
			severity := measurementSeverityFail
			classification := sample.Status
			if sample.Status == sampleStatusMeasurementError {
				severity = measurementSeverityWarn
			}
			message := sample.Error
			if sample.Warmup {
				message = fmt.Sprintf("warmup %d: %s", sample.Sample, message)
			}
			findings = append(findings, analysisFinding{
				Scenario:       scenarioName,
				Severity:       severity,
				Classification: classification,
				Message:        message,
			})
			continue
		}
		if sample.Warmup {
			continue
		}
		for metric, value := range sample.Measurements {
			if _, allowed := allowedMeasurements[metric]; !allowed {
				continue
			}
			values[metric] = append(values[metric], value)
		}
	}
	if scenario.Samples == 0 && statusFromFindings(findings) != measurementSeverityFail {
		findings = append(findings, analysisFinding{
			Scenario: scenarioName, Severity: measurementSeverityFail,
			Classification: sampleStatusScenarioError, Message: "scenario produced no measured samples",
		})
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		scenario.Measurements[key] = summarizeValues(values[key])
	}
	findings = append(findings, missingMeasurementFindings(scenarioSpec, values, scenario.Samples, policy)...)

	if baselineErr != nil {
		findings = append(findings, analysisFinding{
			Scenario:       scenarioName,
			Severity:       measurementSeverityWarn,
			Classification: "baseline_missing",
			Message:        baselineErr.Error(),
		})
	} else {
		findings = append(findings, compareMeasurements(scenarioName, scenario.Measurements, baseline, policy)...)
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return findings[i].Severity < findings[j].Severity
		}
		if findings[i].Measurement != findings[j].Measurement {
			return findings[i].Measurement < findings[j].Measurement
		}
		return findings[i].Message < findings[j].Message
	})
	scenario.Findings = findings
	scenario.Status = statusFromFindings(findings)
	return scenario
}

func missingMeasurementFindings(
	scenario scenarioSpec,
	values map[string][]float64,
	measuredSamples int,
	policy policyDocument,
) []analysisFinding {
	if measuredSamples == 0 {
		return nil
	}
	required := scenarioMeasurementSet(scenario)
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)

	findings := make([]analysisFinding, 0)
	for _, metric := range names {
		if len(values[metric]) > 0 {
			continue
		}
		measurementPolicy := normalizeMeasurementPolicy(policy.Measurements[metric], policy.Defaults)
		severity := measurementPolicy.Severity
		if severity == measurementSeverityFail {
			severity = measurementSeverityWarn
		}
		if severity == measurementSeverityInfo {
			continue
		}
		findings = append(findings, analysisFinding{
			Scenario:       scenario.Name,
			Measurement:    metric,
			Severity:       severity,
			Classification: "measurement_missing",
			Message:        fmt.Sprintf("%s was not emitted by any measured sample", metric),
		})
	}
	return findings
}

func compareMeasurements(
	scenarioName string,
	current map[string]measurementSummary,
	baseline baselineDocument,
	policy policyDocument,
) []analysisFinding {
	findings := make([]analysisFinding, 0)
	for metric, currentSummary := range current {
		measurementPolicy := normalizeMeasurementPolicy(policy.Measurements[metric], policy.Defaults)
		if measurementPolicy.Policy == measurementPolicyInformational {
			continue
		}
		if measurementPolicy.MinimumSamples > 0 && currentSummary.Count < measurementPolicy.MinimumSamples {
			findings = append(findings, analysisFinding{
				Scenario:       scenarioName,
				Measurement:    metric,
				Severity:       measurementSeverityWarn,
				Classification: "insufficient_samples",
				Message: fmt.Sprintf(
					"%s has %d samples, policy requires %d",
					metric,
					currentSummary.Count,
					measurementPolicy.MinimumSamples,
				),
				Current: float64(currentSummary.Count),
			})
			continue
		}

		if measurementPolicy.Policy == measurementPolicyMustBeZero {
			currentValue := comparisonValue(currentSummary, measurementPolicy.Compare)
			if currentValue > 0 {
				findings = append(findings, analysisFinding{
					Scenario:       scenarioName,
					Measurement:    metric,
					Severity:       measurementPolicy.Severity,
					Classification: findingPerformanceFailure,
					Message:        fmt.Sprintf("%s must remain zero (current=%.3f)", metric, currentValue),
					Current:        currentValue,
				})
			}
			continue
		}

		baseSummary, ok := baseline.Summary[metric]
		if !ok || baseSummary.Count == 0 {
			findings = append(findings, analysisFinding{
				Scenario:       scenarioName,
				Measurement:    metric,
				Severity:       measurementSeverityWarn,
				Classification: "baseline_missing",
				Message:        fmt.Sprintf("baseline for %s is missing", metric),
			})
			continue
		}
		currentValue := comparisonValue(currentSummary, measurementPolicy.Compare)
		baselineValue := comparisonValue(baseSummary, measurementPolicy.Compare)
		if !violatesPolicy(currentValue, baselineValue, measurementPolicy) {
			continue
		}
		findings = append(findings, analysisFinding{
			Scenario:       scenarioName,
			Measurement:    metric,
			Severity:       measurementPolicy.Severity,
			Classification: findingPerformanceFailure,
			Message: fmt.Sprintf(
				"%s regressed (current=%.3f baseline=%.3f compare=%s)",
				metric,
				currentValue,
				baselineValue,
				measurementPolicy.Compare,
			),
			Current:  currentValue,
			Baseline: baselineValue,
		})
	}
	return findings
}

func applyConsecutivePrimaryRegressionPolicy(
	current *runSummaryDocument,
	previous runSummaryDocument,
	policy policyDocument,
) {
	if current == nil {
		return
	}
	previousFailures := previousPerformanceFindingSet(previous)
	for name, scenario := range current.Scenarios {
		changed := false
		for i := range scenario.Findings {
			finding := &scenario.Findings[i]
			if finding.Severity != measurementSeverityWarn ||
				finding.Classification != findingPerformanceFailure ||
				finding.Measurement == "" {
				continue
			}
			measurementPolicy := normalizeMeasurementPolicy(policy.Measurements[finding.Measurement], policy.Defaults)
			if measurementPolicy.Role != measurementRolePrimary {
				continue
			}
			if _, exists := previousFailures[findingKey{
				Scenario:    name,
				Measurement: finding.Measurement,
			}]; !exists {
				continue
			}
			finding.Severity = measurementSeverityFail
			finding.Classification = findingPerformanceFailureConsecutive
			finding.Message = finding.Message + " (also regressed in the previous weekly run)"
			changed = true
		}
		if changed {
			scenario.Status = statusFromFindings(scenario.Findings)
			current.Scenarios[name] = scenario
		}
	}
	recomputeSummaryTotals(current)
}

type findingKey struct {
	Scenario    string
	Measurement string
}

func previousPerformanceFindingSet(summary runSummaryDocument) map[findingKey]struct{} {
	out := make(map[findingKey]struct{})
	for scenarioName, scenario := range summary.Scenarios {
		for _, finding := range scenario.Findings {
			if finding.Measurement == "" {
				continue
			}
			switch finding.Classification {
			case findingPerformanceFailure, findingPerformanceFailureConsecutive:
				out[findingKey{
					Scenario:    scenarioName,
					Measurement: finding.Measurement,
				}] = struct{}{}
			}
		}
	}
	return out
}

func recomputeSummaryTotals(summary *runSummaryDocument) {
	if summary == nil {
		return
	}
	summary.Totals = summaryTotals{}
	for name, scenario := range summary.Scenarios {
		scenario.Status = statusFromFindings(scenario.Findings)
		summary.Scenarios[name] = scenario
		summary.Totals.Scenarios++
		summary.Totals.Samples += scenario.Samples
		switch scenario.Status {
		case measurementSeverityFail:
			summary.Totals.Fail++
		case measurementSeverityWarn:
			summary.Totals.Warn++
		default:
			summary.Totals.Pass++
		}
	}
}

func violatesPolicy(current, baseline float64, policy measurementPolicy) bool {
	switch policy.Policy {
	case measurementPolicyLowerBound:
		return current < baseline
	case measurementPolicyUpperBound:
		diff := current - baseline
		if diff <= 0 {
			return false
		}
		absAllowed := policy.AllowedAbsolute
		relAllowed := policy.AllowedRelative
		if baseline <= 0 {
			return diff > absAllowed
		}
		relativeRegression := diff / baseline
		if absAllowed > 0 && relAllowed > 0 {
			return diff > absAllowed && relativeRegression > relAllowed
		}
		if absAllowed > 0 {
			return diff > absAllowed
		}
		if relAllowed > 0 {
			return relativeRegression > relAllowed
		}
		return diff > 0
	default:
		return false
	}
}

func normalizeMeasurementPolicy(policy measurementPolicy, defaults measurementPolicy) measurementPolicy {
	if policy.Role == "" {
		policy.Role = defaults.Role
	}
	if policy.Role == "" {
		policy.Role = measurementRoleDiagnostic
	}
	if policy.Policy == "" {
		policy.Policy = defaults.Policy
	}
	if policy.Policy == "" {
		policy.Policy = measurementPolicyInformational
	}
	if policy.Severity == "" {
		policy.Severity = defaults.Severity
	}
	if policy.Severity == "" {
		policy.Severity = measurementSeverityInfo
	}
	if policy.Compare == "" {
		policy.Compare = defaults.Compare
	}
	if policy.Compare == "" {
		policy.Compare = compareMedian
	}
	if policy.AllowedRelative == 0 {
		policy.AllowedRelative = defaults.AllowedRelative
	}
	if policy.AllowedAbsolute == 0 {
		policy.AllowedAbsolute = defaults.AllowedAbsolute
	}
	if policy.MinimumSamples == 0 {
		policy.MinimumSamples = defaults.MinimumSamples
	}
	return policy
}

func comparisonValue(summary measurementSummary, compare string) float64 {
	switch compare {
	case compareMax:
		return summary.Max
	case compareUpperSample:
		return summary.UpperSample
	default:
		return summary.Median
	}
}

func statusFromFindings(findings []analysisFinding) string {
	status := sampleStatusPass
	for _, finding := range findings {
		if finding.Severity == measurementSeverityFail {
			return measurementSeverityFail
		}
		if finding.Severity == measurementSeverityWarn {
			status = measurementSeverityWarn
		}
	}
	return status
}

func summarizeValues(values []float64) measurementSummary {
	if len(values) == 0 {
		return measurementSummary{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	minimum := sorted[0]
	maximum := sorted[len(sorted)-1]
	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}
	return measurementSummary{
		Median:      median,
		UpperSample: maximum,
		Min:         minimum,
		Max:         maximum,
		Count:       len(sorted),
	}
}

func loadPolicyDocument(path string) (policyDocument, error) {
	if strings.TrimSpace(path) == "" {
		path = defaultPolicyPath
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return policyDocument{}, fmt.Errorf("read policy: %w", err)
	}
	var doc policyDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return policyDocument{}, fmt.Errorf("parse policy yaml: %w", err)
	}
	if doc.Version != versionV2 {
		return policyDocument{}, fmt.Errorf("policy version = %q, want %q", doc.Version, versionV2)
	}
	if len(doc.Measurements) == 0 {
		return policyDocument{}, fmt.Errorf("policy missing measurements")
	}
	return doc, nil
}

func readSampleDocuments(artifactDir string) ([]sampleDocument, error) {
	patterns := []string{
		filepath.Join(artifactDir, "scenarios", "*", "sample-*.json"),
		filepath.Join(artifactDir, "scenarios", "*", "warmup-*.json"),
	}
	var paths []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("glob samples: %w", err)
		}
		paths = append(paths, matches...)
	}
	sort.Strings(paths)
	samples := make([]sampleDocument, 0, len(paths))
	for _, path := range paths {
		if !isTimelineSampleFile(path) {
			continue
		}
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("read sample %s: %w", path, err)
		}
		var sample sampleDocument
		if err := json.Unmarshal(data, &sample); err != nil {
			return nil, fmt.Errorf("parse sample %s: %w", path, err)
		}
		if sample.Version != versionV2 {
			return nil, fmt.Errorf("sample %s version = %q, want %q", path, sample.Version, versionV2)
		}
		samples = append(samples, sample)
	}
	return samples, nil
}

func isTimelineSampleFile(path string) bool {
	base := filepath.Base(path)
	for _, prefix := range []string{"sample-", "warmup-"} {
		if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".json") {
			continue
		}
		index := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".json")
		if len(index) != 3 {
			return false
		}
		for _, r := range index {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	return false
}

func readScenarioBaseline(opts options, scenario string) (baselineDocument, error) {
	path := scenarioBaselinePath(opts, scenario)
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return baselineDocument{}, fmt.Errorf("read baseline %s: %w", path, err)
	}
	var doc baselineDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return baselineDocument{}, fmt.Errorf("parse baseline %s: %w", path, err)
	}
	if doc.Version != versionV2 {
		return baselineDocument{}, fmt.Errorf("baseline %s version = %q, want %q", path, doc.Version, versionV2)
	}
	return doc, nil
}

func readRunSummary(path string) (runSummaryDocument, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return runSummaryDocument{}, fmt.Errorf("read previous summary %s: %w", path, err)
	}
	var doc runSummaryDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return runSummaryDocument{}, fmt.Errorf("parse previous summary %s: %w", path, err)
	}
	if doc.Version != versionV2 {
		return runSummaryDocument{}, fmt.Errorf("previous summary %s version = %q, want %q", path, doc.Version, versionV2)
	}
	return doc, nil
}

func writeScenarioBaseline(opts options, scenario scenarioSpec, samples []sampleDocument) error {
	values := make(map[string][]float64)
	var environment runEnvironment
	successfulSamples := 0
	allowedMeasurements := scenarioMeasurementSet(scenario)
	for _, sample := range samples {
		if environment == (runEnvironment{}) {
			environment = sample.Environment
		}
		if sample.Warmup || sample.Status != sampleStatusPass {
			continue
		}
		successfulSamples++
		for key, value := range sample.Measurements {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			if _, allowed := allowedMeasurements[key]; !allowed {
				continue
			}
			values[key] = append(values[key], value)
		}
	}
	if opts.MinimumSuccessfulSamples > 0 && successfulSamples < opts.MinimumSuccessfulSamples {
		return fmt.Errorf(
			"scenario %q produced %d passing measured samples, need at least %d",
			scenario.Name,
			successfulSamples,
			opts.MinimumSuccessfulSamples,
		)
	}
	if len(values) == 0 {
		return fmt.Errorf("scenario %q produced no baseline measurements", scenario.Name)
	}

	summary := make(map[string]measurementSummary, len(values))
	for key, metricValues := range values {
		summary[key] = summarizeValues(metricValues)
	}
	doc := baselineDocument{
		Version:     versionV2,
		Scenario:    scenario.Name,
		CapturedAt:  time.Now().UTC(),
		Commit:      environment.Commit,
		Environment: environment,
		Samples:     values,
		Summary:     summary,
	}
	return writeJSONFile(scenarioBaselinePath(opts, scenario.Name), doc)
}

func scenarioBaselinePath(opts options, scenario string) string {
	return filepath.Join(opts.BaselineDir, scenario, opts.EnvironmentID+".json")
}

func scenarioSelected(selected []string, scenario string) bool {
	for _, name := range selected {
		if name == scenario {
			return true
		}
	}
	return false
}

func printTerminalSummary(summary runSummaryDocument) {
	fmt.Printf(
		"performance summary: scenarios=%d pass=%d warn=%d fail=%d\n",
		summary.Totals.Scenarios,
		summary.Totals.Pass,
		summary.Totals.Warn,
		summary.Totals.Fail,
	)
	names := make([]string, 0, len(summary.Scenarios))
	for name := range summary.Scenarios {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		scenario := summary.Scenarios[name]
		fmt.Printf("scenario=%s status=%s samples=%d\n", name, scenario.Status, scenario.Samples)
		if len(scenario.Measurements) > 0 {
			fmt.Printf("  measurements=%s\n", formatScenarioMeasurements(scenario.Measurements))
		}
		for _, finding := range scenario.Findings {
			fmt.Printf("  %s: %s\n", finding.Severity, finding.Message)
		}
	}
}

func formatScenarioMeasurements(measurements map[string]measurementSummary) string {
	keys := make([]string, 0, len(measurements))
	for key := range measurements {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s.median=%.3f", key, measurements[key].Median))
	}
	return strings.Join(parts, " ")
}
