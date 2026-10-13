package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclwrite"
	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	platformsemver "github.com/kubebao/openbao-operator/internal/platform/semver"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

const (
	configUnsealKeyPath         = "file:///etc/bao/unseal/key"
	configUnsealKeyID           = "operator-generated-v1"
	configMaxRequestDuration    = "90s"
	configNodeIDTemplate        = "${HOSTNAME}"
	configScrapeProfileAllNodes = "AllNodes"

	jwtPolicyHealthStepDownAutopilot = `path "sys/health" { capabilities = ["read"] }
path "sys/step-down" { capabilities = ["sudo", "update"] }
path "sys/storage/raft/configuration" { capabilities = ["read"] }
path "sys/storage/raft/remove-peer" { capabilities = ["update"] }
path "sys/storage/raft/autopilot/configuration" { capabilities = ["read", "update"] }
path "sys/storage/raft/autopilot/state" { capabilities = ["read"] }`

	jwtPolicyUpgradeRolling = `path "sys/health" { capabilities = ["read"] }
path "sys/step-down" { capabilities = ["sudo", "update"] }
path "sys/storage/raft/snapshot" { capabilities = ["read"] }
path "sys/storage/raft/autopilot/state" { capabilities = ["read"] }`

	jwtPolicyUpgradeBlueGreen = `path "sys/health" { capabilities = ["read"] }
path "sys/step-down" { capabilities = ["sudo", "update"] }
path "sys/storage/raft/snapshot" { capabilities = ["read"] }
path "sys/storage/raft/autopilot/state" { capabilities = ["read"] }
path "sys/storage/raft/join" { capabilities = ["update"] }
path "sys/storage/raft/configuration" { capabilities = ["read", "update"] }
path "sys/storage/raft/remove-peer" { capabilities = ["update"] }
path "sys/storage/raft/promote" { capabilities = ["update"] }
path "sys/storage/raft/demote" { capabilities = ["update"] }`

	jwtPolicyBackupSnapshot  = `path "sys/storage/raft/snapshot" { capabilities = ["read"] }`
	jwtPolicyRestoreSnapshot = `path "sys/storage/raft/snapshot" { capabilities = ["update"] }
path "sys/storage/raft/snapshot-force" { capabilities = ["update"] }`
)

// OperatorBootstrapConfig holds configuration for operator bootstrap.
type OperatorBootstrapConfig struct {
	OIDCIssuerURL         string
	OIDCDiscoveryURL      string
	OIDCDiscoveryCAPEM    string
	OIDCJWKSURL           string
	OIDCJWKSCAPEM         string
	JWTKeysPEM            []string
	OperatorNS            string
	OperatorSA            string
	JWTAuthAudience       string
	ControllerJWTAudience string
}

func shouldUseDynamicOIDCDiscovery(config OperatorBootstrapConfig) bool {
	if strings.TrimSpace(config.OIDCDiscoveryURL) == "" {
		return false
	}

	if strings.TrimSpace(config.OIDCDiscoveryCAPEM) != "" && len(config.JWTKeysPEM) > 0 {
		return false
	}

	return true
}

func shouldUseDynamicJWKS(config OperatorBootstrapConfig) bool {
	jwksURL := strings.TrimSpace(config.OIDCJWKSURL)
	if jwksURL == "" {
		return false
	}
	if strings.TrimSpace(config.OIDCJWKSCAPEM) != "" {
		return true
	}

	issuerURL := strings.TrimSpace(config.OIDCIssuerURL)
	if issuerURL == "" {
		return false
	}

	jwks, err := url.Parse(jwksURL)
	if err != nil || jwks.Scheme == "" || jwks.Host == "" {
		return false
	}
	issuer, err := url.Parse(issuerURL)
	if err != nil || issuer.Scheme == "" || issuer.Host == "" {
		return false
	}

	return strings.EqualFold(jwks.Scheme, issuer.Scheme) && strings.EqualFold(jwks.Host, issuer.Host)
}

// InfrastructureDetails captures the pieces of topology information required to
// render a complete config.hcl file.
type InfrastructureDetails struct {
	HeadlessServiceName string
	Namespace           string
	APIPort             int
	ClusterPort         int
	// TargetRevisionForJoin is an optional revision identifier for blue/green deployments.
	// When set, the retry_join label selector will include this revision to ensure
	// Green pods only discover Blue pods (not each other).
	TargetRevisionForJoin string
	// RetryJoinLabelSelector overrides the generated Kubernetes retry_join label selector.
	// This is used for workload topologies such as steady-state read replicas that must
	// only discover a subset of pods.
	RetryJoinLabelSelector string
	// RetryJoinAsNonVoter marks the joining node as a non-voter when using retry_join.
	RetryJoinAsNonVoter bool
}

// RenderHCL renders a complete OpenBao configuration using the provided cluster
// specification and infrastructure details.
//
// The generated configuration:
//   - Always includes operator-owned listener "tcp" and storage "raft" stanzas.
//   - Includes seal stanza based on spec.unseal (defaults to "static" if omitted).
//   - Uses a Kubernetes go-discover-based retry_join block for dynamic cluster membership.
//   - Renders user-tunable configuration from typed fields under spec.configuration.
//   - Renders audit devices from spec.audit (if configured).
//   - Renders plugins from spec.plugins (if configured).
//   - Renders telemetry configuration from spec.telemetry (if configured).
func RenderHCL(cluster *openbaov1alpha1.OpenBaoCluster, infra InfrastructureDetails) ([]byte, error) {
	file := hclwrite.NewEmptyFile()
	body := file.Body()

	infra, err := validateInfrastructureDetails(cluster, infra)
	if err != nil {
		return nil, err
	}

	if err := validateListenerProxyProtocol(cluster); err != nil {
		return nil, err
	}
	if err := validateConfigVersionCompatibility(cluster); err != nil {
		return nil, err
	}
	if err := validateAuditFileStorageConfiguration(cluster); err != nil {
		return nil, err
	}

	// General configuration. UI defaults to true, but can be overridden by user configuration.
	uiEnabled := true
	if cluster.Spec.Configuration != nil && cluster.Spec.Configuration.UI != nil {
		uiEnabled = *cluster.Spec.Configuration.UI
	}

	apiAddr := fmt.Sprintf("https://${HOSTNAME}.%s.%s.svc:%d", infra.HeadlessServiceName, infra.Namespace, infra.APIPort)
	clusterAddr := fmt.Sprintf("https://${HOSTNAME}.%s.%s.svc:%d", infra.HeadlessServiceName, infra.Namespace, infra.ClusterPort)

	var pluginDirectory *string
	if len(cluster.Spec.Plugins) > 0 {
		pluginDirectory = stringPtr(constants.PathPlugins)
	}

	gohcl.EncodeIntoBody(hclCoreAttributes{
		UI:              uiEnabled,
		ClusterName:     cluster.Name,
		APIAddr:         apiAddr,
		ClusterAddr:     clusterAddr,
		PluginDirectory: pluginDirectory,
	}, body)

	listenerBlocks, err := buildListenerBlocks(cluster)
	if err != nil {
		return nil, err
	}
	for _, listenerBlock := range listenerBlocks {
		body.AppendBlock(listenerBlock)
	}

	sealBlock, err := buildSealBlock(cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to render seal stanza: %w", err)
	}
	body.AppendBlock(sealBlock)

	body.AppendBlock(buildStorageBlock(cluster, infra))

	if cluster.Annotations[constants.AnnotationRestoreOrigin] == "" {
		body.AppendNewBlock("service_registration", []string{"kubernetes"})
	}

	if tokens := buildUserConfigTokens(cluster.Spec.Configuration); len(tokens) > 0 {
		body.AppendUnstructuredTokens(tokens)
	}

	// 8. Render audit devices if configured
	auditBlocks, err := buildAuditDeviceBlocks(cluster.Spec.Audit)
	if err != nil {
		return nil, fmt.Errorf("failed to render audit devices: %w", err)
	}
	for _, block := range auditBlocks {
		body.AppendBlock(block)
	}

	// 9. Render plugins if configured
	for _, block := range buildPluginBlocks(cluster.Spec.Plugins) {
		body.AppendBlock(block)
	}

	// 10. Render telemetry if configured (via Spec.Telemetry OR Spec.Observability)
	telemetryConfig := cluster.Spec.Telemetry
	if cluster.Spec.Observability != nil && cluster.Spec.Observability.Metrics != nil && cluster.Spec.Observability.Metrics.Enabled {
		if telemetryConfig == nil {
			telemetryConfig = &openbaov1alpha1.TelemetryConfig{}
		} else {
			// Create a shallow copy to avoid mutating the cache
			tc := *telemetryConfig
			telemetryConfig = &tc
		}

		// Apply defaults for Prometheus integration
		telemetryConfig.DisableHostname = true
		if telemetryConfig.PrometheusRetentionTime == "" {
			telemetryConfig.PrometheusRetentionTime = "30s"
		}
	}

	if telemetryBlock := buildTelemetryBlock(telemetryConfig); telemetryBlock != nil {
		body.AppendBlock(telemetryBlock)
	}

	// Note: Self-initialization stanzas are rendered separately via RenderSelfInitHCL
	// and stored in a separate ConfigMap that is only mounted for pod-0.

	return file.Bytes(), nil
}

func validateConfigVersionCompatibility(cluster *openbaov1alpha1.OpenBaoCluster) error {
	if cluster == nil {
		return fmt.Errorf("cluster is required")
	}
	if err := portopenbao.ValidateSealPlugins(cluster); err != nil {
		return err
	}
	if err := validatePluginVersionCompatibility(cluster); err != nil {
		return err
	}

	if err := validateMetricsOnlyListenerCompatibility(cluster); err != nil {
		return err
	}
	if err := validateListenerTLSPolicy(cluster); err != nil {
		return err
	}
	if err := validateAzureKeyVaultAuthMethod(cluster); err != nil {
		return err
	}

	if cluster.Spec.Configuration == nil || cluster.Spec.Configuration.Plugin == nil {
		return nil
	}

	plugin := cluster.Spec.Configuration.Plugin
	if plugin.AutoDownload == nil && plugin.AutoRegister == nil && strings.TrimSpace(plugin.DownloadBehavior) == "" {
		return nil
	}

	ok, err := openBaoVersionAtLeast(cluster.Spec.Version, 2, 5, 0)
	if err != nil {
		return fmt.Errorf("failed to validate config version compatibility: %w", err)
	}
	if ok {
		return nil
	}

	return fmt.Errorf("spec.configuration.plugin.{autoDownload,autoRegister,downloadBehavior} requires OpenBao >= 2.5.0 (spec.version=%q)", cluster.Spec.Version)
}

func validateMetricsOnlyListenerCompatibility(cluster *openbaov1alpha1.OpenBaoCluster) error {
	if !workloadMetricsEnabled(cluster) {
		return nil
	}

	metrics := cluster.Spec.Observability.Metrics
	allNodes := strings.TrimSpace(metrics.ScrapeProfile) == configScrapeProfileAllNodes
	listener := metrics.MetricsOnlyListener
	if allNodes && listener != nil && listener.Enabled != nil && !*listener.Enabled {
		return fmt.Errorf("spec.observability.metrics.metricsOnlyListener.enabled cannot be false when scrapeProfile is AllNodes")
	}

	listenerExplicitlyEnabled := listener != nil && listener.Enabled != nil && *listener.Enabled
	if !allNodes && !listenerExplicitlyEnabled {
		return nil
	}

	ok, err := openBaoVersionAtLeast(cluster.Spec.Version, 2, 5, 0)
	if err != nil {
		return fmt.Errorf("failed to validate metrics-only listener version compatibility: %w", err)
	}
	if ok {
		return nil
	}

	return fmt.Errorf("spec.observability.metrics.metricsOnlyListener requires OpenBao >= 2.5.0 (spec.version=%q)", cluster.Spec.Version)
}

func openBaoVersionAtLeast(version string, wantMajor, wantMinor, wantPatch int) (bool, error) {
	return platformsemver.AtLeast(version, wantMajor, wantMinor, wantPatch)
}

func upgradePolicyForCluster(cluster *openbaov1alpha1.OpenBaoCluster) string {
	if cluster.Spec.Upgrade != nil && cluster.Spec.Upgrade.Strategy == openbaov1alpha1.UpdateStrategyBlueGreen {
		return jwtPolicyUpgradeBlueGreen
	}
	return jwtPolicyUpgradeRolling
}
