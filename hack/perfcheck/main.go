package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	subcommand := os.Args[1]
	switch subcommand {
	case "capture":
		opts, err := parseCaptureFlags(os.Args[2:])
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				os.Exit(0)
			}
			exitWithError(err)
		}
		if err := runCapture(opts); err != nil {
			exitWithError(err)
		}
	case "verify":
		opts, err := parseVerifyFlags(os.Args[2:])
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				os.Exit(0)
			}
			exitWithError(err)
		}
		if err := runVerify(opts); err != nil {
			exitWithError(err)
		}
	case "report":
		opts, err := parseReportFlags(os.Args[2:])
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				os.Exit(0)
			}
			exitWithError(err)
		}
		if err := runReport(opts); err != nil {
			exitWithError(err)
		}
	default:
		printUsage()
		exitWithError(fmt.Errorf("unknown subcommand %q", subcommand))
	}
}

func parseCaptureFlags(args []string) (options, error) {
	opts := defaultOptions("capture")
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	bindExecutionFlags(fs, &opts)
	fs.StringVar(&opts.BaselineDir, "baseline-dir", defaultBaselineDir, "output directory for v2 baseline JSON")

	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	return finalizeOptions(opts)
}

func parseVerifyFlags(args []string) (options, error) {
	opts := defaultOptions("verify")
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	bindExecutionFlags(fs, &opts)
	fs.StringVar(&opts.PolicyPath, "policy", defaultPolicyPath, "v2 measurement policy YAML")
	fs.StringVar(&opts.BaselineDir, "baseline-dir", defaultBaselineDir, "directory containing v2 baseline JSON")
	fs.StringVar(
		&opts.PreviousSummaryPath,
		"previous-summary",
		"",
		"optional previous v2 summary JSON used to escalate consecutive primary regressions",
	)

	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	return finalizeOptions(opts)
}

func parseReportFlags(args []string) (options, error) {
	opts := defaultOptions("report")
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	bindCommonFlags(fs, &opts)
	fs.StringVar(&opts.PolicyPath, "policy", defaultPolicyPath, "v2 measurement policy YAML")
	fs.StringVar(&opts.BaselineDir, "baseline-dir", defaultBaselineDir, "directory containing v2 baseline JSON")
	fs.StringVar(
		&opts.PreviousSummaryPath,
		"previous-summary",
		"",
		"optional previous v2 summary JSON used to escalate consecutive primary regressions",
	)
	fs.StringVar(&opts.SummaryOut, "summary-out", "", "summary JSON output path")
	fs.StringVar(&opts.ReportOut, "out", "", "markdown report output path")
	fs.BoolVar(
		&opts.FailOnFailures,
		"fail-on-failures",
		false,
		"exit non-zero when the rendered summary contains fail-severity findings",
	)

	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	return finalizeOptions(opts)
}

func defaultOptions(mode string) options {
	return options{
		Mode:            mode,
		ScenarioPath:    defaultScenarioPath,
		PolicyPath:      defaultPolicyPath,
		BaselineDir:     defaultBaselineDir,
		ArtifactDir:     defaultArtifactDir,
		EnvironmentID:   defaultEnvironment,
		NodeImage:       "kindest/node:v1.34.3",
		KindBin:         "kind",
		MakeBin:         "make",
		OperatorImage:   envOrDefault("PERF_OPERATOR_IMAGE", "example.com/openbao-operator:0.0.1"),
		ConfigInitImage: envOrDefault("PERF_CONFIG_INIT_IMAGE", "openbao-init:dev"),
		BackupExecutorImage: envOrDefault(
			"PERF_BACKUP_EXECUTOR_IMAGE",
			"openbao-backup:dev",
		),
		UpgradeExecutorImage: envOrDefault(
			"PERF_UPGRADE_EXECUTOR_IMAGE",
			"openbao-upgrade:dev",
		),
		OpenBaoVersion:     envOrDefault("PERF_OPENBAO_VERSION", "2.6.5"),
		OpenBaoImage:       envOrDefault("PERF_OPENBAO_IMAGE", "openbao/openbao:2.6.5"),
		UpgradeFromVersion: envOrDefault("PERF_UPGRADE_FROM_VERSION", "2.6.1"),
		UpgradeFromImage:   envOrDefault("PERF_UPGRADE_FROM_IMAGE", "openbao/openbao:2.6.1"),
		UpgradeToVersion:   envOrDefault("PERF_UPGRADE_TO_VERSION", "2.6.5"),
		UpgradeToImage:     envOrDefault("PERF_UPGRADE_TO_IMAGE", "openbao/openbao:2.6.5"),
		APIServerCIDR:      envOrDefault("PERF_API_SERVER_CIDR", "10.96.0.0/12"),
		StorageClass:       envOrDefault("PERF_STORAGE_CLASS", ""),
		TenantChurnCount:   10,
		ClusterTimeout:     20 * time.Minute,
		CleanupTimeout:     10 * time.Minute,
		SamplesOverride:    0,
		WarmupsOverride:    -1,
		OperatorNS:         "openbao-operator-system",
		MetricsService:     "openbao-operator-controller-metrics-service",
		ServiceAccount:     "openbao-operator-controller",
		BindingName:        "openbao-operator-metrics-binding",
	}
}

func bindExecutionFlags(fs *flag.FlagSet, opts *options) {
	bindCommonFlags(fs, opts)
	fs.IntVar(&opts.SamplesOverride, "samples", 0, "measured samples per scenario; 0 uses manifest defaults")
	fs.IntVar(&opts.WarmupsOverride, "warmups", -1, "warmup samples per scenario; -1 uses manifest defaults")
	fs.DurationVar(&opts.ScenarioTimeout, "timeout", 0, "per-sample timeout; 0 uses manifest defaults")
	fs.DurationVar(&opts.ScenarioTimeout, "scenario-timeout", 0, "alias for --timeout")
	fs.DurationVar(&opts.ClusterTimeout, "cluster-timeout", opts.ClusterTimeout, "kind setup timeout")
	fs.DurationVar(&opts.CleanupTimeout, "cleanup-timeout", opts.CleanupTimeout, "kind cleanup timeout")
	fs.BoolVar(&opts.KeepOnFailure, "keep-on-failure", false, "keep kind clusters if a sample fails")
	fs.BoolVar(
		&opts.ContinueOnSampleError,
		"continue-on-sample-error",
		false,
		"continue running remaining samples after a scenario or measurement error",
	)
	fs.IntVar(
		&opts.MinimumSuccessfulSamples,
		"minimum-successful-samples",
		0,
		"minimum passing measured samples required before writing a scenario baseline; 0 disables the guard",
	)
	fs.BoolVar(&opts.SkipImageBuild, "skip-image-build", false, "skip image build when supported by the executor")
	fs.StringVar(&opts.OperatorImage, "operator-image", opts.OperatorImage, "operator image for native scenarios")
	fs.StringVar(&opts.ConfigInitImage, "config-init-image", opts.ConfigInitImage, "config-init image")
	fs.StringVar(
		&opts.BackupExecutorImage,
		"backup-executor-image",
		opts.BackupExecutorImage,
		"backup and restore executor image",
	)
	fs.StringVar(
		&opts.UpgradeExecutorImage,
		"upgrade-executor-image",
		opts.UpgradeExecutorImage,
		"upgrade executor image",
	)
	fs.StringVar(&opts.OpenBaoVersion, "openbao-version", opts.OpenBaoVersion, "OpenBao version")
	fs.StringVar(&opts.OpenBaoImage, "openbao-image", opts.OpenBaoImage, "OpenBao image")
	fs.StringVar(
		&opts.UpgradeFromVersion,
		"upgrade-from-version",
		opts.UpgradeFromVersion,
		"rolling upgrade source OpenBao version",
	)
	fs.StringVar(&opts.UpgradeFromImage, "upgrade-from-image", opts.UpgradeFromImage, "rolling upgrade source image")
	fs.StringVar(
		&opts.UpgradeToVersion,
		"upgrade-to-version",
		opts.UpgradeToVersion,
		"rolling upgrade target OpenBao version",
	)
	fs.StringVar(&opts.UpgradeToImage, "upgrade-to-image", opts.UpgradeToImage, "rolling upgrade target image")
	fs.StringVar(&opts.APIServerCIDR, "api-server-cidr", opts.APIServerCIDR, "Kubernetes API service CIDR")
	fs.StringVar(&opts.StorageClass, "storage-class", opts.StorageClass, "storage class for native scenario PVCs")
	fs.IntVar(
		&opts.TenantChurnCount,
		"tenant-churn-count",
		opts.TenantChurnCount,
		"tenant namespaces to create in the tenant-churn scenario",
	)
	fs.StringVar(
		&opts.ExistingClusterContext,
		"existing-cluster-context",
		"",
		"explicit kubeconfig context for existing-cluster mode",
	)
	fs.StringVar(&opts.Namespace, "namespace", "", "namespace used for existing-cluster mode")
	fs.StringVar(&opts.NamespacePrefix, "namespace-prefix", "", "namespace prefix used for existing-cluster mode")
	fs.StringVar(&opts.KindBin, "kind", opts.KindBin, "path to kind binary")
	fs.StringVar(&opts.MakeBin, "make", opts.MakeBin, "path to make binary")
	fs.StringVar(&opts.NodeImage, "node-image", opts.NodeImage, "kind node image")
}

func bindCommonFlags(fs *flag.FlagSet, opts *options) {
	fs.StringVar(&opts.ScenarioPath, "scenario-manifest", opts.ScenarioPath, "v2 scenario manifest YAML path")
	fs.StringVar(&opts.ArtifactDir, "artifact-dir", opts.ArtifactDir, "perf artifact directory")
	fs.StringVar(&opts.RunID, "run-id", "", "run identifier recorded in reports and artifacts")
	fs.StringVar(&opts.EnvironmentID, "environment", opts.EnvironmentID, "baseline environment id")
	fs.Func("scenario", "scenario to run; may be provided multiple times", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("scenario must not be empty")
		}
		opts.ScenarioNames = append(opts.ScenarioNames, strings.TrimSpace(value))
		return nil
	})
	fs.Func("scenarios", "comma-separated scenarios from the scenario manifest, or all", func(value string) error {
		selected, err := parseScenarioSelection(value)
		if err != nil {
			return err
		}
		opts.ScenarioNames = selected
		return nil
	})
}

func finalizeOptions(opts options) (options, error) {
	if opts.SamplesOverride < 0 {
		return options{}, fmt.Errorf("samples must be >= 0")
	}
	if opts.WarmupsOverride < -1 {
		return options{}, fmt.Errorf("warmups must be >= -1")
	}
	if strings.TrimSpace(opts.ArtifactDir) == "" {
		return options{}, fmt.Errorf("artifact-dir must not be empty")
	}
	if strings.TrimSpace(opts.EnvironmentID) == "" {
		return options{}, fmt.Errorf("environment must not be empty")
	}
	if opts.TenantChurnCount < 1 {
		return options{}, fmt.Errorf("tenant-churn-count must be >= 1")
	}
	if opts.MinimumSuccessfulSamples < 0 {
		return options{}, fmt.Errorf("minimum-successful-samples must be >= 0")
	}
	return opts, nil
}

func parseScenarioSelection(input string) ([]string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || trimmed == "all" {
		return nil, nil
	}

	parts := strings.Split(trimmed, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if name == "all" {
			return nil, nil
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no scenarios selected")
	}
	return out, nil
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: perfcheck <capture|verify|report> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "capture: run scenarios and write v2 sample artifacts plus distribution baselines")
	fmt.Fprintln(os.Stderr, "verify: run scenarios, write v2 artifacts, and compare with v2 baselines")
	fmt.Fprintln(os.Stderr, "report: summarize existing v2 sample artifacts")
}

func exitWithError(err error) {
	fmt.Fprintf(os.Stderr, "perfcheck: %v\n", err)
	os.Exit(1)
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
