package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func runCapture(opts options) error {
	scenarios, manifest, err := selectedScenarios(opts)
	if err != nil {
		return err
	}
	if err := prepareScenarioArtifactDirs(opts, scenarios); err != nil {
		return err
	}

	var samples []sampleDocument
	for _, scenario := range scenarios {
		if err := scenarioRequiresExistingClusterSupport(opts, scenario); err != nil {
			return err
		}
		scenarioSamples, err := executeScenarioSamples(opts, manifest, scenario)
		if err != nil {
			return err
		}
		samples = append(samples, scenarioSamples...)
		if err := writeScenarioBaseline(opts, scenario, scenarioSamples); err != nil {
			return err
		}
	}

	failedSamples := 0
	for _, sample := range samples {
		if sample.Status == sampleStatusPass || sample.Warmup {
			continue
		}
		failedSamples++
	}
	if failedSamples > 0 {
		if !opts.ContinueOnSampleError {
			return fmt.Errorf("capture completed with scenario errors; inspect %s", opts.ArtifactDir)
		}
		fmt.Fprintf(
			os.Stderr,
			"warning: capture completed with %d failed measured samples; baselines use passing samples only\n",
			failedSamples,
		)
	}
	fmt.Printf("wrote v2 baselines under %s\n", opts.BaselineDir)
	fmt.Printf("wrote v2 sample artifacts under %s\n", opts.ArtifactDir)
	return nil
}

func runVerify(opts options) error {
	scenarios, manifest, err := selectedScenarios(opts)
	if err != nil {
		return err
	}
	if err := prepareScenarioArtifactDirs(opts, scenarios); err != nil {
		return err
	}

	for _, scenario := range scenarios {
		if err := scenarioRequiresExistingClusterSupport(opts, scenario); err != nil {
			return err
		}
		if _, err := executeScenarioSamples(opts, manifest, scenario); err != nil {
			return err
		}
	}

	summary, err := summarizeRun(opts)
	if err != nil {
		return err
	}
	if err := writeSummaryArtifacts(opts, summary); err != nil {
		return err
	}
	printTerminalSummary(summary)
	if summary.Totals.Fail > 0 {
		return fmt.Errorf("performance verification failed (%d failing scenarios)", summary.Totals.Fail)
	}
	return nil
}

func prepareScenarioArtifactDirs(opts options, scenarios []scenarioSpec) error {
	for _, scenario := range scenarios {
		if err := os.RemoveAll(scenarioArtifactDir(opts, scenario.Name)); err != nil {
			return fmt.Errorf("clear scenario artifacts for %s: %w", scenario.Name, err)
		}
	}
	return nil
}

func runReport(opts options) error {
	summary, err := summarizeRun(opts)
	if err != nil {
		return err
	}
	if err := writeSummaryArtifacts(opts, summary); err != nil {
		return err
	}
	printTerminalSummary(summary)
	if opts.FailOnFailures && summary.Totals.Fail > 0 {
		return fmt.Errorf("performance report contains %d failing scenarios", summary.Totals.Fail)
	}
	return nil
}

func executeScenarioSamples(
	opts options,
	manifest scenarioManifest,
	scenario scenarioSpec,
) ([]sampleDocument, error) {
	warmups := effectiveWarmups(opts, manifest, scenario)
	samples := effectiveSamples(opts, manifest, scenario)
	timeout := effectiveSampleTimeout(opts, manifest, scenario)

	out := make([]sampleDocument, 0, warmups+samples)
	for i := 1; i <= warmups+samples; i++ {
		warmup := i <= warmups
		var sampleNumber int
		if warmup {
			sampleNumber = i
		} else {
			sampleNumber = i - warmups
		}
		sample, err := executeScenarioSample(opts, manifest, scenario, sampleNumber, warmup, timeout)
		if err != nil {
			return nil, err
		}
		out = append(out, sample)
		if sample.Status != sampleStatusPass && !opts.ContinueOnSampleError {
			return out, fmt.Errorf(
				"scenario %q sample=%d warmup=%t failed with status %q; inspect %s",
				scenario.Name,
				sampleNumber,
				warmup,
				sample.Status,
				scenarioArtifactDir(opts, scenario.Name),
			)
		}
	}
	return out, nil
}

func executeScenarioSample(
	opts options,
	manifest scenarioManifest,
	scenario scenarioSpec,
	sampleIndex int,
	warmup bool,
	timeout time.Duration,
) (sampleDocument, error) {
	started := time.Now().UTC()
	cluster := clusterNameForScenario(scenario.Name, sampleIndex)
	if opts.ExistingClusterContext != "" {
		cluster = opts.ExistingClusterContext
	}

	sample := sampleDocument{
		Version:      versionV2,
		Scenario:     scenario.Name,
		Sample:       sampleIndex,
		Warmup:       warmup,
		Cluster:      cluster,
		Environment:  collectRunEnvironment(opts, cluster),
		StartedAt:    started,
		Status:       sampleStatusPass,
		Phases:       []phaseEvent{{Name: "sample_started", At: started, Source: "harness"}},
		Measurements: make(map[string]float64),
		Artifacts:    make(map[string]string),
	}

	fmt.Printf("running scenario=%s sample=%d warmup=%t cluster=%s\n", scenario.Name, sampleIndex, warmup, cluster)
	scenarioDir := scenarioArtifactDir(opts, scenario.Name)
	if err := os.MkdirAll(scenarioDir, 0o755); err != nil {
		return sampleDocument{}, fmt.Errorf("create scenario artifact directory: %w", err)
	}

	cleanupPolicy := effectiveCleanup(manifest, scenario)
	shouldCleanup, prepareErr := prepareScenarioSample(opts, scenario, cleanupPolicy, cluster)
	if prepareErr != nil {
		sample.Status = sampleStatusScenarioError
		sample.Error = prepareErr.Error()
		if err := collectKubernetesArtifacts(opts, scenarioDir, cluster, sample, ""); err != nil {
			fmt.Fprintf(os.Stderr, "warning: collecting Kubernetes setup artifacts failed: %v\n", err)
		}
		return finishAndWriteSample(opts, sample)
	}
	defer func() {
		if shouldCleanup {
			cleanupClusterWithWarning(opts, cluster)
		}
	}()

	beforeCtx, cancelBefore := context.WithTimeout(context.Background(), 90*time.Second)
	before, beforeText, beforePresent, beforeErr := scrapeMetricsSnapshot(beforeCtx, opts, cluster, true)
	cancelBefore()
	if beforeErr != nil {
		sample.Status = sampleStatusMeasurementError
		sample.Error = beforeErr.Error()
		before = emptySnapshot()
	} else if beforeText != "" {
		path, err := writeTextArtifact(scenarioDir, sampleArtifactName(sample, "metrics-before.prom"), beforeText)
		if err != nil {
			return sampleDocument{}, err
		}
		sample.Artifacts["metricsBefore"] = path
	}
	if !beforePresent {
		before = emptySnapshot()
	}

	scenarioStarted := time.Now().UTC()
	runCtx, cancelRun := context.WithTimeout(context.Background(), timeout)
	execResult, runErr := runScenarioExecutor(runCtx, opts, cluster, scenario, sample, scenarioDir)
	cancelRun()
	scenarioDuration := time.Since(scenarioStarted).Seconds()
	defer func() {
		if execResult.Cleanup == nil {
			return
		}
		if runErr != nil && opts.KeepOnFailure {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), opts.CleanupTimeout)
		defer cancelCleanup()
		execResult.Cleanup(cleanupCtx)
	}()
	sample.Phases = append(sample.Phases, execResult.Phases...)
	for key, value := range execResult.Measurements {
		sample.Measurements[key] = value
	}
	if _, exists := sample.Measurements[metricScenarioRunSeconds]; !exists {
		sample.Measurements[metricScenarioRunSeconds] = scenarioDuration
	}
	for key, value := range execResult.Artifacts {
		sample.Artifacts[key] = value
	}
	if runErr != nil {
		sample.Status = sampleStatusScenarioError
		sample.Error = runErr.Error()
		if opts.KeepOnFailure && opts.ExistingClusterContext == "" {
			shouldCleanup = false
			fmt.Fprintf(os.Stderr, "keeping cluster %s for debugging\n", cluster)
		}
	} else {
		completed := time.Now().UTC()
		sample.Phases = append(sample.Phases, phaseEvent{Name: "scenario_completed", At: completed, Source: "harness"})
	}

	afterCtx, cancelAfter := context.WithTimeout(context.Background(), 2*time.Minute)
	after, afterText, _, afterErr := scrapeMetricsSnapshot(
		afterCtx,
		opts,
		cluster,
		opts.ExistingClusterContext != "",
	)
	cancelAfter()
	if afterErr != nil && sample.Status == sampleStatusPass {
		sample.Status = sampleStatusMeasurementError
		sample.Error = afterErr.Error()
		after = emptySnapshot()
	}
	if afterText != "" {
		path, err := writeTextArtifact(scenarioDir, sampleArtifactName(sample, "metrics-after.prom"), afterText)
		if err != nil {
			return sampleDocument{}, err
		}
		sample.Artifacts["metricsAfter"] = path
	}

	for key, value := range computeDiagnosticMeasurements(before, after) {
		if _, exists := sample.Measurements[key]; !exists {
			sample.Measurements[key] = value
		}
	}
	if err := collectWorkloadMetrics(opts, cluster, scenarioDir, &sample, execResult.Namespace); err != nil {
		return sampleDocument{}, err
	}
	if _, exists := sample.Measurements[metricSampleTotalSeconds]; !exists {
		sample.Measurements[metricSampleTotalSeconds] = time.Since(started).Seconds()
	}

	if err := collectKubernetesArtifacts(opts, scenarioDir, cluster, sample, execResult.Namespace); err != nil {
		fmt.Fprintf(os.Stderr, "warning: collecting Kubernetes artifacts failed: %v\n", err)
	}
	if sample.Status != sampleStatusPass && cleanupPolicy == cleanupOnSuccess {
		shouldCleanup = false
	}
	return finishAndWriteSample(opts, sample)
}

func collectWorkloadMetrics(
	opts options,
	cluster string,
	scenarioDir string,
	sample *sampleDocument,
	namespace string,
) error {
	workloadCtx, cancelWorkload := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelWorkload()

	workloadAfter, workloadText, workloadPresent, workloadErr := scrapeWorkloadMetricsSnapshot(
		workloadCtx,
		opts,
		cluster,
		namespace,
		true,
	)
	if workloadErr != nil {
		fmt.Fprintf(os.Stderr, "warning: scraping OpenBao workload metrics failed: %v\n", workloadErr)
		return nil
	}
	if !workloadPresent {
		return nil
	}
	if workloadText != "" {
		path, err := writeTextArtifact(
			scenarioDir,
			sampleArtifactName(*sample, "openbao-workload-metrics-after.prom"),
			workloadText,
		)
		if err != nil {
			return err
		}
		sample.Artifacts["openBaoWorkloadMetricsAfter"] = path
	}
	for key, value := range computeDiagnosticMeasurements(emptySnapshot(), workloadAfter) {
		if _, exists := sample.Measurements[key]; !exists {
			sample.Measurements[key] = value
		}
	}
	return nil
}

func prepareScenarioSample(
	opts options,
	scenario scenarioSpec,
	cleanupPolicy string,
	cluster string,
) (bool, error) {
	shouldCleanup := opts.ExistingClusterContext == "" && cleanupPolicy == cleanupAlways
	if opts.ExistingClusterContext != "" {
		if scenario.Executor != executorNativeGo {
			return shouldCleanup, nil
		}
		return shouldCleanup, prepareNativeExistingCluster(opts, cluster)
	}
	if err := setupKindCluster(opts, cluster); err != nil {
		return shouldCleanup, err
	}
	if scenario.Executor == executorNativeGo {
		if err := prepareNativeKindCluster(opts, cluster, scenario); err != nil {
			return shouldCleanup, err
		}
	}
	if cleanupPolicy == cleanupOnSuccess {
		shouldCleanup = true
	}
	return shouldCleanup, nil
}

func finishAndWriteSample(opts options, sample sampleDocument) (sampleDocument, error) {
	sample.CompletedAt = time.Now().UTC()
	if sample.Measurements == nil {
		sample.Measurements = make(map[string]float64)
	}
	if sample.Artifacts == nil {
		sample.Artifacts = make(map[string]string)
	}
	path := filepath.Join(scenarioArtifactDir(opts, sample.Scenario), sampleFileName(sample))
	if err := writeJSONFile(path, sample); err != nil {
		return sampleDocument{}, err
	}
	return sample, nil
}

func runScenarioExecutor(
	ctx context.Context,
	opts options,
	cluster string,
	scenario scenarioSpec,
	sample sampleDocument,
	scenarioDir string,
) (scenarioExecutionResult, error) {
	switch scenario.Executor {
	case executorE2EGinkgo:
		return runScenarioTests(ctx, opts, cluster, scenario, sample, scenarioDir)
	case executorScript:
		return scenarioExecutionResult{}, runScenarioScript(ctx, scenario)
	case executorNativeGo:
		return runNativeScenario(ctx, opts, cluster, scenario)
	default:
		return scenarioExecutionResult{}, fmt.Errorf("unsupported executor %q", scenario.Executor)
	}
}

func runScenarioTests(
	ctx context.Context,
	opts options,
	cluster string,
	scenario scenarioSpec,
	sample sampleDocument,
	scenarioDir string,
) (scenarioExecutionResult, error) {
	result := scenarioExecutionResult{}
	jsonReport := filepath.Join(scenarioDir, sampleArtifactName(sample, "ginkgo.json"))
	env := map[string]string{
		"E2E_LABEL_FILTER":   scenario.LabelFilter,
		"E2E_JSON_REPORT":    jsonReport,
		"E2E_PARALLEL_NODES": "1",
		"E2E_SKIP_CLEANUP":   "true",
	}
	if opts.SkipImageBuild {
		env["E2E_SKIP_IMAGE_BUILD"] = "true"
	}
	target := "test-e2e-ci"
	if opts.ExistingClusterContext == "" {
		env["KIND"] = opts.KindBin
		env["KIND_CLUSTER"] = cluster
	} else {
		env["E2E_USE_EXISTING_CLUSTER"] = "true"
		env["KUBECONFIG_CONTEXT"] = opts.ExistingClusterContext
		if opts.Namespace != "" {
			env["E2E_NAMESPACE"] = opts.Namespace
		}
		if opts.NamespacePrefix != "" {
			env["E2E_NAMESPACE_PREFIX"] = opts.NamespacePrefix
		}
		target = "test-e2e-existing"
	}
	_, err := runCommand(ctx, env, opts.MakeBin, target)
	if phases, phaseErr := parseGinkgoPhaseEvents(jsonReport); phaseErr == nil {
		result.Phases = phases
		result.Artifacts = map[string]string{"ginkgoJSON": jsonReport}
	} else if !errors.Is(phaseErr, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "warning: parsing Ginkgo phase report failed: %v\n", phaseErr)
	}
	if err != nil {
		return result, fmt.Errorf("run e2e scenario %q: %w", scenario.Name, err)
	}
	return result, nil
}

func runScenarioScript(ctx context.Context, scenario scenarioSpec) error {
	if len(scenario.Command) == 0 {
		return fmt.Errorf("script scenario %q has no command", scenario.Name)
	}
	name := scenario.Command[0]
	args := scenario.Command[1:]
	_, err := runCommand(ctx, nil, name, args...)
	if err != nil {
		return fmt.Errorf("run script scenario %q: %w", scenario.Name, err)
	}
	return nil
}

func clusterNameForScenario(name string, sample int) string {
	replacer := strings.NewReplacer("_", "-", "/", "-", " ", "-", ".", "-")
	slug := replacer.Replace(strings.ToLower(strings.TrimSpace(name)))
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "scenario"
	}
	cluster := fmt.Sprintf("perf-%s-%d", slug, sample)
	if len(cluster) > 60 {
		cluster = cluster[:60]
	}
	return cluster
}

func setupKindCluster(opts options, cluster string) error {
	ctx, cancel := context.WithTimeout(context.Background(), opts.ClusterTimeout)
	defer cancel()

	env := map[string]string{
		"KIND":            opts.KindBin,
		"KIND_CLUSTER":    cluster,
		"KIND_NODE_IMAGE": opts.NodeImage,
	}
	_, err := runCommand(ctx, env, opts.MakeBin, "setup-test-e2e")
	if err != nil {
		return fmt.Errorf("setup kind cluster %q: %w", cluster, err)
	}
	return nil
}

func cleanupClusterWithWarning(opts options, cluster string) {
	if err := cleanupKindCluster(opts, cluster); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cleanup failed for cluster %s: %v\n", cluster, err)
	}
}

func cleanupKindCluster(opts options, cluster string) error {
	ctx, cancel := context.WithTimeout(context.Background(), opts.CleanupTimeout)
	defer cancel()
	env := map[string]string{
		"KIND":         opts.KindBin,
		"KIND_CLUSTER": cluster,
	}
	_, err := runCommand(ctx, env, opts.MakeBin, "cleanup-test-e2e")
	if err != nil {
		return fmt.Errorf("cleanup kind cluster %q: %w", cluster, err)
	}
	return nil
}

func runCommand(ctx context.Context, env map[string]string, name string, args ...string) (string, error) {
	// This helper runs only repo-managed tools selected by validated perfcheck options.
	// nosemgrep
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, env[key]))
		}
	}

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return output.String(), fmt.Errorf("%s %s failed: %w\n%s", name, strings.Join(args, " "), err, output.String())
	}
	return output.String(), nil
}

func emptySnapshot() metricsSnapshot {
	return metricsSnapshot{
		Counters:   make(map[string]float64),
		GaugeMax:   make(map[string]float64),
		Histograms: make(map[string]map[float64]float64),
		Summaries:  make(map[string]summarySnapshot),
	}
}

func scrapeMetricsSnapshot(
	ctx context.Context,
	opts options,
	cluster string,
	allowMissing bool,
) (metricsSnapshot, string, bool, error) {
	clusterContext := kubeContext(opts, cluster)
	if err := ensureMetricsRoleBinding(ctx, opts, clusterContext); err != nil {
		if allowMissing {
			return emptySnapshot(), "", false, nil
		}
		return metricsSnapshot{}, "", false, err
	}

	token, err := createMetricsToken(ctx, opts, clusterContext)
	if err != nil {
		if allowMissing {
			return emptySnapshot(), "", false, nil
		}
		return metricsSnapshot{}, "", false, err
	}

	metricsText, err := fetchMetricsViaPortForward(ctx, opts, clusterContext, token)
	if err != nil {
		if allowMissing && looksLikeMissingResource(err) {
			return emptySnapshot(), "", false, nil
		}
		if allowMissing {
			return emptySnapshot(), "", false, nil
		}
		return metricsSnapshot{}, "", false, err
	}

	snapshot, err := parseMetricsSnapshot(metricsText)
	if err != nil {
		return metricsSnapshot{}, metricsText, true, err
	}
	return snapshot, metricsText, true, nil
}

func ensureMetricsRoleBinding(ctx context.Context, opts options, clusterContext string) error {
	if opts.ExistingClusterContext != "" {
		return nil
	}
	roleNames := []string{"openbao-operator-metrics-reader", "metrics-reader"}
	var lastErr error
	for _, role := range roleNames {
		_, err := runCommand(ctx, nil,
			"kubectl",
			"--context", clusterContext,
			"create", "clusterrolebinding", opts.BindingName,
			fmt.Sprintf("--clusterrole=%s", role),
			fmt.Sprintf("--serviceaccount=%s:%s", opts.OperatorNS, opts.ServiceAccount),
		)
		if err == nil {
			return nil
		}
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "alreadyexists") || strings.Contains(errStr, "already exists") {
			return nil
		}
		lastErr = err
		if strings.Contains(errStr, "notfound") || strings.Contains(errStr, "not found") {
			continue
		}
	}
	if lastErr != nil {
		return fmt.Errorf("ensure metrics role binding: %w", lastErr)
	}
	return fmt.Errorf("ensure metrics role binding: no role candidates succeeded")
}

func createMetricsToken(ctx context.Context, opts options, clusterContext string) (string, error) {
	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		out, err := runCommand(ctx, nil,
			"kubectl",
			"--context", clusterContext,
			"create", "token", opts.ServiceAccount,
			"-n", opts.OperatorNS,
			"--duration=1h",
		)
		if err == nil {
			token := strings.TrimSpace(out)
			if token != "" {
				return token, nil
			}
			lastErr = fmt.Errorf("token response was empty")
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out creating metrics token")
	}
	return "", fmt.Errorf("create metrics token: %w", lastErr)
}

func fetchMetricsViaPortForward(ctx context.Context, opts options, clusterContext, token string) (string, error) {
	port, err := findFreeLocalPort()
	if err != nil {
		return "", err
	}

	serviceRef := fmt.Sprintf("service/%s", opts.MetricsService)
	portArg := fmt.Sprintf("%d:8443", port)
	cmd := exec.CommandContext(ctx,
		"kubectl",
		"--context", clusterContext,
		"port-forward",
		"--namespace", opts.OperatorNS,
		serviceRef,
		portArg,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start port-forward: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	if err := waitForLocalPort(ctx, port, 20*time.Second); err != nil {
		return "", err
	}

	metricsURL := fmt.Sprintf("https://127.0.0.1:%d/metrics", port)
	httpClient := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			//nolint:gosec // metrics are fetched via localhost port-forward in test infrastructure.
			TLSClientConfig: &tls.Config{ // nosemgrep
				MinVersion:         tls.VersionTLS13,
				InsecureSkipVerify: true, // nosemgrep
			},
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch metrics: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read metrics body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("metrics endpoint status %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

func scrapeWorkloadMetricsSnapshot(
	ctx context.Context,
	opts options,
	cluster string,
	namespace string,
	allowMissing bool,
) (metricsSnapshot, string, bool, error) {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return emptySnapshot(), "", false, nil
	}
	clusterContext := kubeContext(opts, cluster)
	services, err := listWorkloadMetricsServices(ctx, clusterContext, namespace)
	if err != nil {
		if allowMissing {
			return emptySnapshot(), "", false, nil
		}
		return metricsSnapshot{}, "", false, err
	}
	if len(services) == 0 {
		return emptySnapshot(), "", false, nil
	}

	merged := emptySnapshot()
	rawSections := make([]string, 0, len(services))
	present := false
	for _, service := range services {
		port := workloadMetricsServicePort(service)
		if port == 0 {
			continue
		}
		metricsText, err := fetchWorkloadMetricsViaPortForward(ctx, clusterContext, namespace, service.Name, port)
		if err != nil {
			if allowMissing {
				continue
			}
			return metricsSnapshot{}, "", false, err
		}
		snapshot, err := parseMetricsSnapshot(metricsText)
		if err != nil {
			if allowMissing {
				continue
			}
			return metricsSnapshot{}, metricsText, true, err
		}
		mergeMetricsSnapshot(&merged, snapshot)
		rawSections = append(rawSections, fmt.Sprintf(
			"# perfcheck_openbao_workload_metrics service=%q namespace=%q\n%s",
			service.Name,
			namespace,
			metricsText,
		))
		present = true
	}
	if !present {
		return emptySnapshot(), "", false, nil
	}
	return merged, strings.Join(rawSections, "\n"), true, nil
}

func listWorkloadMetricsServices(ctx context.Context, clusterContext, namespace string) ([]corev1.Service, error) {
	out, err := runCommand(ctx, nil,
		"kubectl",
		"--context", clusterContext,
		"-n", namespace,
		"get", "services",
		"-l", constants.LabelOpenBaoComponent+"=metrics",
		"-o", "json",
	)
	if err != nil {
		return nil, fmt.Errorf("list OpenBao workload metrics services: %w", err)
	}
	var services corev1.ServiceList
	if err := json.Unmarshal([]byte(out), &services); err != nil {
		return nil, fmt.Errorf("parse OpenBao workload metrics services: %w", err)
	}
	sort.Slice(services.Items, func(i, j int) bool {
		return services.Items[i].Name < services.Items[j].Name
	})
	return services.Items, nil
}

func workloadMetricsServicePort(service corev1.Service) int32 {
	for _, port := range service.Spec.Ports {
		if port.Name == "https-metrics" {
			return port.Port
		}
	}
	if len(service.Spec.Ports) == 1 {
		return service.Spec.Ports[0].Port
	}
	return 0
}

func fetchWorkloadMetricsViaPortForward(
	ctx context.Context,
	clusterContext string,
	namespace string,
	serviceName string,
	servicePort int32,
) (string, error) {
	port, err := findFreeLocalPort()
	if err != nil {
		return "", err
	}

	serviceRef := fmt.Sprintf("service/%s", serviceName)
	portArg := fmt.Sprintf("%d:%d", port, servicePort)
	cmd := exec.CommandContext(ctx,
		"kubectl",
		"--context", clusterContext,
		"port-forward",
		"--namespace", namespace,
		serviceRef,
		portArg,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start OpenBao workload metrics port-forward: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	if err := waitForLocalPort(ctx, port, 20*time.Second); err != nil {
		return "", err
	}

	metricsURL := fmt.Sprintf("https://127.0.0.1:%d/v1/sys/metrics?format=prometheus", port)
	httpClient := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			//nolint:gosec // metrics are fetched via localhost port-forward in test infrastructure.
			TLSClientConfig: &tls.Config{ // nosemgrep
				MinVersion:         tls.VersionTLS13,
				InsecureSkipVerify: true, // nosemgrep
			},
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch OpenBao workload metrics: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read OpenBao workload metrics body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("OpenBao workload metrics endpoint status %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

func mergeMetricsSnapshot(dst *metricsSnapshot, src metricsSnapshot) {
	if dst.Counters == nil {
		dst.Counters = make(map[string]float64)
	}
	if dst.GaugeMax == nil {
		dst.GaugeMax = make(map[string]float64)
	}
	if dst.Histograms == nil {
		dst.Histograms = make(map[string]map[float64]float64)
	}
	if dst.Summaries == nil {
		dst.Summaries = make(map[string]summarySnapshot)
	}

	for name, value := range src.Counters {
		dst.Counters[name] += value
	}
	for name, value := range src.GaugeMax {
		current, exists := dst.GaugeMax[name]
		if !exists || value > current {
			dst.GaugeMax[name] = value
		}
	}
	for name, buckets := range src.Histograms {
		if _, exists := dst.Histograms[name]; !exists {
			dst.Histograms[name] = make(map[float64]float64)
		}
		for bucket, value := range buckets {
			dst.Histograms[name][bucket] += value
		}
	}
	for name, summary := range src.Summaries {
		current := dst.Summaries[name]
		current.Count += summary.Count
		current.Sum += summary.Sum
		dst.Summaries[name] = current
	}
}

func findFreeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("listen for free local port: %w", err)
	}
	defer func() {
		_ = listener.Close()
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener addr type %T", listener.Addr())
	}
	return addr.Port, nil
}

func waitForLocalPort(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for local port %d", port)
}

func looksLikeMissingResource(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "notfound") || strings.Contains(errStr, "not found") {
		return true
	}
	if strings.Contains(errStr, "connection refused") {
		return true
	}
	if strings.Contains(errStr, "serviceaccount") && strings.Contains(errStr, "not found") {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func collectRunEnvironment(opts options, cluster string) runEnvironment {
	env := runEnvironment{
		NodeImage: opts.NodeImage,
		GoVersion: runtime.Version(),
		Commit:    commandOutputOrEmpty(context.Background(), nil, "git", "rev-parse", "HEAD"),
		Context:   kubeContext(opts, cluster),
	}
	if opts.ExistingClusterContext != "" {
		env.Runner = "existing-cluster"
	} else {
		env.Runner = "kind"
		env.KindVersion = commandOutputOrEmpty(context.Background(), nil, opts.KindBin, "version")
	}
	return env
}

func commandOutputOrEmpty(ctx context.Context, env map[string]string, name string, args ...string) string {
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := runCommand(commandCtx, env, name, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func kubeContext(opts options, cluster string) string {
	if opts.ExistingClusterContext != "" {
		return opts.ExistingClusterContext
	}
	return fmt.Sprintf("kind-%s", cluster)
}

func scenarioArtifactDir(opts options, scenario string) string {
	return filepath.Join(opts.ArtifactDir, "scenarios", scenario)
}

func sampleFileName(sample sampleDocument) string {
	if sample.Warmup {
		return fmt.Sprintf("warmup-%03d.json", sample.Sample)
	}
	return fmt.Sprintf("sample-%03d.json", sample.Sample)
}

func sampleArtifactName(sample sampleDocument, suffix string) string {
	prefix := strings.TrimSuffix(sampleFileName(sample), ".json")
	return fmt.Sprintf("%s-%s", prefix, suffix)
}

func writeTextArtifact(dir, name, body string) (string, error) {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("write artifact %s: %w", path, err)
	}
	return path, nil
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func collectKubernetesArtifacts(
	opts options,
	scenarioDir string,
	cluster string,
	sample sampleDocument,
	namespace string,
) error {
	clusterContext := kubeContext(opts, cluster)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	scopeArgs := []string{"--all-namespaces"}
	if opts.ExistingClusterContext != "" && namespace != "" {
		scopeArgs = []string{"--namespace", namespace}
	} else if opts.ExistingClusterContext != "" && opts.Namespace != "" {
		scopeArgs = []string{"--namespace", opts.Namespace}
	} else if opts.ExistingClusterContext != "" {
		return nil
	}

	writeKubectlArtifact := func(name string, args ...string) error {
		fullArgs := append([]string{"--context", clusterContext}, args...)
		out, err := runCommand(ctx, nil, "kubectl", fullArgs...)
		if err != nil {
			return nil
		}
		_, err = writeTextArtifact(scenarioDir, sampleArtifactName(sample, name), out)
		return err
	}

	jsonTargets := []struct {
		name string
		args []string
	}{
		{"pods.json", append([]string{"get", "pods"}, scopeArgs...)},
		{"deployments.json", append([]string{"get", "deployments"}, scopeArgs...)},
		{"replicasets.json", append([]string{"get", "replicasets"}, scopeArgs...)},
		{"jobs.json", append([]string{"get", "jobs"}, scopeArgs...)},
		{"events.json", append([]string{"get", "events"}, scopeArgs...)},
		{"statefulsets.json", append([]string{"get", "statefulsets"}, scopeArgs...)},
		{"services.json", append([]string{"get", "services"}, scopeArgs...)},
		{"endpointslices.json", append([]string{"get", "endpointslices.discovery.k8s.io"}, scopeArgs...)},
		{"persistentvolumeclaims.json", append([]string{"get", "persistentvolumeclaims"}, scopeArgs...)},
		{"openbaoclusters.json", append([]string{"get", "openbaoclusters.openbao.org"}, scopeArgs...)},
		{"openbaorestores.json", append([]string{"get", "openbaorestores.openbao.org"}, scopeArgs...)},
	}
	for _, target := range jsonTargets {
		args := append([]string{}, target.args...)
		args = append(args, "-o", "json")
		if err := writeKubectlArtifact(target.name, args...); err != nil {
			return err
		}
	}

	if err := writeKubectlArtifact(
		"openbaotenants.json",
		"get",
		"openbaotenants.openbao.org",
		"--namespace",
		opts.OperatorNS,
		"-o",
		"json",
	); err != nil {
		return err
	}

	describeTargets := []struct {
		name string
		args []string
	}{
		{"openbaoclusters-describe.txt", append([]string{"describe", "openbaoclusters.openbao.org"}, scopeArgs...)},
		{
			"openbaotenants-describe.txt",
			[]string{"describe", "openbaotenants.openbao.org", "--namespace", opts.OperatorNS},
		},
	}
	for _, target := range describeTargets {
		if err := writeKubectlArtifact(target.name, target.args...); err != nil {
			return err
		}
	}

	logTargets := []struct {
		name string
		args []string
	}{
		{
			"operator-controller-logs.txt",
			[]string{
				"logs",
				"deployment/openbao-operator-controller",
				"--namespace",
				opts.OperatorNS,
				"--all-containers",
				"--tail=1000",
				"--prefix",
			},
		},
		{
			"operator-provisioner-logs.txt",
			[]string{
				"logs",
				"deployment/openbao-operator-provisioner",
				"--namespace",
				opts.OperatorNS,
				"--all-containers",
				"--tail=1000",
				"--prefix",
			},
		},
	}
	for _, target := range logTargets {
		if err := writeKubectlArtifact(target.name, target.args...); err != nil {
			return err
		}
	}
	return nil
}
