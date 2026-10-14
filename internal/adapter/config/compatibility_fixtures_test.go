package config

import (
	"strings"
	"testing"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestRenderCompatibilityRaftNonVoter(t *testing.T) {
	for _, blueGreen := range []bool{false, true} {
		t.Run(map[bool]string{false: "read replica", true: "blue green"}[blueGreen], func(t *testing.T) {
			cluster := newMinimalCluster("raft-options", "default")
			cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{
				Raft: &openbaov1alpha1.RaftConfig{PerformanceMultiplier: int32Ptr(3)},
			}
			infra := InfrastructureDetails{Namespace: cluster.Namespace, APIPort: 8200, ClusterPort: 8201}
			if blueGreen {
				infra.TargetRevisionForJoin = "blue"
			} else {
				infra.RetryJoinAsNonVoter = true
			}
			got, err := RenderHCL(cluster, infra)
			require.NoError(t, err)
			require.Contains(t, string(got), "retry_join_as_non_voter = true")
			require.NotContains(t, string(got), "election_timeout")
			if !blueGreen {
				compareGolden(t, "render_hcl_raft_non_voter", got)
			}
		})
	}
}

func TestRenderCompatibilityTelemetry(t *testing.T) {
	cluster := newMinimalCluster("telemetry-options", "default")
	cluster.Spec.Telemetry = &openbaov1alpha1.TelemetryConfig{
		UsageGaugePeriod: "10m", MaximumGaugeCardinality: int32Ptr(2000), DisableHostname: true,
		EnableHostnameLabel: true, MetricsPrefix: "compat", LeaseMetricsEpsilon: "1h",
		PrometheusRetentionTime: "30s", StatsiteAddress: "127.0.0.1:8125", StatsdAddress: "127.0.0.1:8126",
		DogStatsdAddress: "127.0.0.1:8127", DogStatsdTags: []string{"env:test"},
		CirconusAPIKey: "test-token", CirconusAPIApp: "test-app", CirconusAPIURL: "https://api.example.test",
		CirconusSubmissionInterval: "10s", CirconusCheckID: "1", CirconusCheckForceMetricActivation: "true",
		CirconusCheckInstanceID: "compat", CirconusCheckSearchTag: "env:test", CirconusCheckDisplayName: "compat",
		CirconusCheckTags: "env:test", CirconusBrokerID: "1", CirconusBrokerSelectTag: "env:test",
		StackdriverProjectID: "test-project", StackdriverLocation: "test-region",
		StackdriverNamespace: "compat", StackdriverDebugLogs: true,
	}
	got, err := RenderHCL(cluster, InfrastructureDetails{Namespace: cluster.Namespace, APIPort: 8200, ClusterPort: 8201})
	require.NoError(t, err)
	compareGolden(t, "render_hcl_telemetry_all_options", got)
}

func TestRenderCompatibilityACMECARoot(t *testing.T) {
	cluster := newMinimalCluster("acme-ca", "default")
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeACME
	cluster.Spec.TLS.ACME = &openbaov1alpha1.ACMEConfig{DirectoryURL: "https://acme.example.test/directory"}
	cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{ACMECARoot: "/etc/acme/ca.crt"}
	got, err := RenderHCL(cluster, InfrastructureDetails{Namespace: cluster.Namespace, APIPort: 8200, ClusterPort: 8201})
	require.NoError(t, err)
	compareGolden(t, "render_hcl_acme_ca_root", got)
}

func TestRenderCompatibilitySealOptions(t *testing.T) {
	tests := []struct {
		name   string
		unseal openbaov1alpha1.UnsealConfig
	}{
		{"transit", openbaov1alpha1.UnsealConfig{Type: "transit", Transit: &openbaov1alpha1.TransitSealConfig{
			Address: "https://transit.example.test", KeyName: "unseal", MountPath: "transit", Token: "test-token",
			DisableRenewal: boolPtr(true), TLSClientCert: "/etc/seal/client.crt", TLSClientKey: "/etc/seal/client.key",
			TLSServerName: "transit.example.test",
		}}},
		{"awskms", openbaov1alpha1.UnsealConfig{Type: "awskms", AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
			Region: "eu-west-1", KMSKeyID: "test-key", AccessKey: "test-access", SecretKey: "test-secret", SessionToken: "test-session",
		}}},
		{"azurekeyvault", openbaov1alpha1.UnsealConfig{Type: "azurekeyvault", AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
			VaultName: "test-vault", KeyName: "test-key", TenantID: "test-tenant", ClientID: "test-client",
			ClientSecret: "test-secret", Environment: "AZUREPUBLICCLOUD",
		}}},
		{"kmip", openbaov1alpha1.UnsealConfig{Type: "kmip", KMIP: &openbaov1alpha1.KMIPSealConfig{
			Endpoint: "kmip.example.test:5696", KMSKeyID: "test-key", Disabled: boolPtr(true),
		}}},
		{"ocikms", openbaov1alpha1.UnsealConfig{Type: "ocikms", OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{KeyID: "test-key", CryptoEndpoint: "https://kms.example.test", Disabled: boolPtr(true)}}},
		{"pkcs11", openbaov1alpha1.UnsealConfig{Type: "pkcs11", PKCS11: &openbaov1alpha1.PKCS11SealConfig{
			Lib: "/usr/lib/test-pkcs11.so", Slot: "1", PIN: "1234", KeyLabel: "test-key", Disabled: boolPtr(true),
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("seal-options", "default")
			cluster.Spec.Unseal = &tt.unseal
			got, err := RenderHCL(cluster, InfrastructureDetails{Namespace: cluster.Namespace, APIPort: 8200, ClusterPort: 8201})
			require.NoError(t, err)
			compareGolden(t, "render_hcl_"+tt.name+"_all_options", got)
		})
	}
}

func TestRenderCompatibilityModernPlugin(t *testing.T) {
	for _, command := range []bool{false, true} {
		t.Run(map[bool]string{false: "OCI", true: "command"}[command], func(t *testing.T) {
			cluster := newMinimalCluster("modern-plugin", "default")
			cluster.Spec.Version = testOpenBaoVersion271
			plugin := openbaov1alpha1.Plugin{Type: "kms", Name: "example"}
			name := "render_hcl_plugin_oci_inferred"
			if command {
				plugin.Command = "test-kms-plugin"
				name = "render_hcl_plugin_command_inferred"
			} else {
				plugin.Image = "registry.example.test/kms:v1.0.0@sha256:" + strings.Repeat("a", 64)
			}
			cluster.Spec.Plugins = []openbaov1alpha1.Plugin{plugin}
			got, err := RenderHCL(cluster, InfrastructureDetails{Namespace: cluster.Namespace, APIPort: 8200, ClusterPort: 8201})
			require.NoError(t, err)
			compareGolden(t, name, got)
		})
	}
}

func TestSelfInitRequestControlsRequireSupportedVersion(t *testing.T) {
	for _, version := range []string{"2.4.4", "2.5.5", "2.6.0-rc.1", "2.6.0", "v2.7.1+build.1"} {
		for _, control := range []string{"headers", "when"} {
			t.Run(version+"/"+control, func(t *testing.T) {
				cluster := newMinimalCluster("profile-versions", "default")
				cluster.Spec.Version = version
				request := openbaov1alpha1.SelfInitRequest{Name: "test", Operation: openbaov1alpha1.SelfInitOperationRead, Path: "sys/health"}
				if control == "headers" {
					request.Headers = map[string][]string{"X-Trace": {"value"}}
				} else {
					request.When = &apiextensionsv1.JSON{Raw: []byte("false")}
				}
				cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{Enabled: true, Requests: []openbaov1alpha1.SelfInitRequest{request}}
				_, err := RenderSelfInitHCL(cluster, nil)
				if version == "2.6.0" || strings.HasPrefix(version, "v2.7") {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, "require OpenBao >= 2.6.0")
				}
			})
		}
	}
}
