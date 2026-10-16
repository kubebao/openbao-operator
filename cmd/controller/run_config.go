package controller

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/cryptopolicy"
	"github.com/kubebao/openbao-operator/internal/platform/entrypoint"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

type runConfig struct {
	kubeconfig               string
	logOptions               zap.Options
	metricsAddr              string
	metricsCertPath          string
	metricsCertName          string
	metricsCertKey           string
	enableLeaderElection     bool
	probeAddr                string
	secureMetrics            bool
	enableHTTP2              bool
	platform                 string
	clientQPS                float64
	clientBurst              int
	clientCBFailureThreshold int
	clientCBOpenDuration     time.Duration
	jwtAuthStrategy          string
	admissionEnforcement     string
	admissionStartupTimeout  time.Duration
}

func parseRunConfig(args []string, output io.Writer) (runConfig, error) {
	cfg := runConfig{}
	fs := flag.NewFlagSet("controller", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&cfg.kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Only required if out-of-cluster.")

	entrypoint.BindManagerFlags(
		fs,
		&cfg.metricsAddr,
		&cfg.probeAddr,
		&cfg.enableLeaderElection,
		&cfg.secureMetrics,
	)
	fs.StringVar(&cfg.metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	fs.StringVar(
		&cfg.metricsCertName,
		"metrics-cert-name",
		"tls.crt",
		"The name of the metrics server certificate file.",
	)
	fs.StringVar(&cfg.metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	fs.BoolVar(&cfg.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics server")
	fs.StringVar(&cfg.platform, "platform", constants.PlatformAuto,
		"The target platform (auto, kubernetes, openshift). Defaults to auto. "+
			"This flag is deprecated and will be removed in a future release. "+
			"Use the OPERATOR_PLATFORM environment variable instead.")

	fs.Float64Var(&cfg.clientQPS, "openbao-client-qps", 50.0,
		"The queries per second (QPS) limit for OpenBao API clients.")
	fs.IntVar(&cfg.clientBurst, "openbao-client-burst", 100,
		"The burst limit for OpenBao API clients.")
	fs.IntVar(&cfg.clientCBFailureThreshold, "openbao-client-cb-failure-threshold", 50,
		"The number of consecutive failures before opening the circuit breaker.")
	fs.DurationVar(&cfg.clientCBOpenDuration, "openbao-client-cb-open-duration", 30*time.Second,
		"The duration the circuit breaker remains open before testing the connection.")

	entrypoint.BindAdmissionFlags(fs, &cfg.admissionEnforcement, &cfg.admissionStartupTimeout)

	cfg.logOptions = zap.Options{Development: false}
	cfg.logOptions.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if fs.NArg() != 0 {
		return runConfig{}, fmt.Errorf("unexpected positional argument %q", fs.Arg(0))
	}

	platform, err := configuredPlatform(cfg.platform, os.Getenv("OPERATOR_PLATFORM"))
	if err != nil {
		return runConfig{}, err
	}
	cfg.platform = platform

	jwtAuthStrategy, err := portopenbao.NormalizeJWTAuthStrategy(os.Getenv(constants.EnvOpenBaoJWTAuthStrategy))
	if err != nil {
		return runConfig{}, err
	}
	cfg.jwtAuthStrategy = jwtAuthStrategy

	admissionEnforcement, err := entrypoint.NormalizeAdmissionEnforcement(cfg.admissionEnforcement)
	if err != nil {
		return runConfig{}, err
	}
	cfg.admissionEnforcement = admissionEnforcement

	return cfg, nil
}

func buildMetricsServerOptions(cfg runConfig) metricsserver.Options {
	metricsServerOptions := metricsserver.Options{
		BindAddress:   cfg.metricsAddr,
		SecureServing: cfg.secureMetrics,
		TLSOpts:       buildTLSOptions(cfg.enableHTTP2),
	}

	if cfg.secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	if cfg.metricsCertPath != "" {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", cfg.metricsCertPath,
			"metrics-cert-name", cfg.metricsCertName,
			"metrics-cert-key", cfg.metricsCertKey,
		)
		metricsServerOptions.CertDir = cfg.metricsCertPath
		metricsServerOptions.CertName = cfg.metricsCertName
		metricsServerOptions.KeyName = cfg.metricsCertKey
	}

	return metricsServerOptions
}

func buildTLSOptions(enableHTTP2 bool) []func(*tls.Config) {
	if enableHTTP2 {
		return []func(*tls.Config){cryptopolicy.ConfigureServerTLS}
	}

	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	return []func(*tls.Config){cryptopolicy.ConfigureServerTLS, disableHTTP2}
}
