package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

const (
	testOpenBaoVersion250          = "2.5.0"
	testOpenBaoVersion264          = "2.6.4"
	testOpenBaoVersion271          = "2.7.1"
	testOpenBaoImage250            = "openbao/openbao:2.5.0"
	testMetricsListenerVersionHint = "requires OpenBao >= 2.5.0"
)

func newMinimalCluster(name, namespace string) *openbaov1alpha1.OpenBaoCluster {
	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.4.4",
			Image:    "openbao/openbao:2.4.4",
			Replicas: 3,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled:        true,
				RotationPeriod: "720h",
			},
			Storage: openbaov1alpha1.StorageConfig{
				Size: "10Gi",
			},
			InitContainer: &openbaov1alpha1.InitContainerConfig{
				Image: "openbao/openbao-init:latest",
			},
		},
	}
}

func TestJWTPolicyCapabilityContracts(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "controller maintenance policy",
			got:  jwtPolicyHealthStepDownAutopilot,
			want: `path "sys/health" { capabilities = ["read"] }
path "sys/step-down" { capabilities = ["sudo", "update"] }
path "sys/storage/raft/configuration" { capabilities = ["read"] }
path "sys/storage/raft/remove-peer" { capabilities = ["update"] }
path "sys/storage/raft/autopilot/configuration" { capabilities = ["read", "update"] }
path "sys/storage/raft/autopilot/state" { capabilities = ["read"] }`,
		},
		{
			name: "rolling upgrade policy",
			got:  jwtPolicyUpgradeRolling,
			want: `path "sys/health" { capabilities = ["read"] }
path "sys/step-down" { capabilities = ["sudo", "update"] }
path "sys/storage/raft/snapshot" { capabilities = ["read"] }
path "sys/storage/raft/autopilot/state" { capabilities = ["read"] }`,
		},
		{
			name: "blue green upgrade policy",
			got:  jwtPolicyUpgradeBlueGreen,
			want: `path "sys/health" { capabilities = ["read"] }
path "sys/step-down" { capabilities = ["sudo", "update"] }
path "sys/storage/raft/snapshot" { capabilities = ["read"] }
path "sys/storage/raft/autopilot/state" { capabilities = ["read"] }
path "sys/storage/raft/join" { capabilities = ["update"] }
path "sys/storage/raft/configuration" { capabilities = ["read", "update"] }
path "sys/storage/raft/remove-peer" { capabilities = ["update"] }
path "sys/storage/raft/promote" { capabilities = ["update"] }
path "sys/storage/raft/demote" { capabilities = ["update"] }`,
		},
		{
			name: "backup policy",
			got:  jwtPolicyBackupSnapshot,
			want: `path "sys/storage/raft/snapshot" { capabilities = ["read"] }`,
		},
		{
			name: "restore policy",
			got:  jwtPolicyRestoreSnapshot,
			want: `path "sys/storage/raft/snapshot" { capabilities = ["update"] }
path "sys/storage/raft/snapshot-force" { capabilities = ["update"] }`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("policy changed:\n got:\n%s\nwant:\n%s", tt.got, tt.want)
			}
		})
	}
}

func TestOperatorJWTRoleDataHardening(t *testing.T) {
	subject := "system:serviceaccount:operator-ns:controller"
	audiences := []string{"openbao-internal"}

	got := operatorJWTRoleData(subject, nil, authPolicyNameOperator, audiences)

	if got.RoleType != authMethodJWT {
		t.Fatalf("RoleType=%q, want %q", got.RoleType, authMethodJWT)
	}
	if got.UserClaim != "sub" {
		t.Fatalf("UserClaim=%q, want sub", got.UserClaim)
	}
	if got.BoundSubject == nil || *got.BoundSubject != subject {
		t.Fatalf("BoundSubject=%v, want %q", got.BoundSubject, subject)
	}
	if len(got.BoundAudiences) != 1 || got.BoundAudiences[0] != audiences[0] {
		t.Fatalf("BoundAudiences=%v, want %v", got.BoundAudiences, audiences)
	}
	if len(got.TokenPolicies) != 1 || got.TokenPolicies[0] != authPolicyNameOperator {
		t.Fatalf("TokenPolicies=%v, want [%s]", got.TokenPolicies, authPolicyNameOperator)
	}
	if got.Policies == nil || len(*got.Policies) != 1 || (*got.Policies)[0] != authPolicyNameOperator {
		t.Fatalf("Policies=%v, want [%s]", got.Policies, authPolicyNameOperator)
	}
	if !got.TokenNoDefaultPolicy {
		t.Fatal("TokenNoDefaultPolicy=false, want true")
	}
	if got.TTL != operatorJWTTokenTTL || got.TokenTTL != operatorJWTTokenTTL || got.TokenMaxTTL != operatorJWTTokenTTL {
		t.Fatalf("role TTLs = (%q,%q,%q), want %q", got.TTL, got.TokenTTL, got.TokenMaxTTL, operatorJWTTokenTTL)
	}
	if got.ClockSkewLeeway != operatorJWTLeeway || got.ExpirationLeeway != operatorJWTLeeway || got.NotBeforeLeeway != operatorJWTLeeway {
		t.Fatalf("role leeways = (%q,%q,%q), want %q", got.ClockSkewLeeway, got.ExpirationLeeway, got.NotBeforeLeeway, operatorJWTLeeway)
	}
}

func TestOperatorJWTRoleDataAdditionalSubjects(t *testing.T) {
	defaultSubject := "system:serviceaccount:source:source-backup-serviceaccount"
	additional := []openbaov1alpha1.KubernetesServiceAccountSubject{
		"system:serviceaccount:recovery-b:target-backup-serviceaccount",
		"system:serviceaccount:recovery-a:target-backup-serviceaccount",
		openbaov1alpha1.KubernetesServiceAccountSubject(defaultSubject),
	}

	got := operatorJWTRoleData(defaultSubject, additional, authPolicyNameBackup, []string{"openbao-internal"})

	if got.BoundSubject != nil {
		t.Fatalf("BoundSubject=%q, want nil when multiple subjects are configured", *got.BoundSubject)
	}
	if got.BoundClaims == nil {
		t.Fatal("BoundClaims=nil, want exact subject claim allowlist")
	}
	want := []string{
		"system:serviceaccount:recovery-a:target-backup-serviceaccount",
		"system:serviceaccount:recovery-b:target-backup-serviceaccount",
		defaultSubject,
	}
	if actual := (*got.BoundClaims)["sub"]; !slices.Equal(actual, want) {
		t.Fatalf("BoundClaims[sub]=%v, want %v", actual, want)
	}
}

func TestRenderHCLIncludesCoreStanzas(t *testing.T) {
	cluster := newMinimalCluster("config-hcl", "security")
	cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{
		LogLevel: "debug",
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_core_stanzas", got)
}

func TestRenderHCLWithStructuredConfiguration(t *testing.T) {
	cluster := newMinimalCluster("structured-config", "default")
	cluster.Spec.Version = testOpenBaoVersion250
	cluster.Spec.Image = testOpenBaoImage250
	uiEnabled := true
	autoDownload := true
	autoRegister := false
	rotateBytes := int64(10485760) // 10MB
	rotateMaxFiles := int32(7)
	fileUID := int64(1000)

	cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{
		UI:       &uiEnabled,
		LogLevel: "info",
		Logging: &openbaov1alpha1.LoggingConfig{
			Format:         "json",
			File:           "/var/log/openbao/openbao.log",
			RotateDuration: "24h",
			RotateBytes:    &rotateBytes,
			RotateMaxFiles: &rotateMaxFiles,
			PIDFile:        "/var/run/openbao/openbao.pid",
		},
		Plugin: &openbaov1alpha1.PluginConfig{
			FileUID:          &fileUID,
			FilePermissions:  "0755",
			AutoDownload:     &autoDownload,
			AutoRegister:     &autoRegister,
			DownloadBehavior: "continue",
		},
		DefaultLeaseTTL: "720h",
		MaxLeaseTTL:     "8760h",
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_structured_config", got)
}

func TestRenderHCLRejectsPluginAutoConfigForUnsupportedVersions(t *testing.T) {
	tests := []struct {
		name    string
		version string
	}{
		{name: "older release", version: "2.4.4"},
		{name: "target prerelease", version: "2.5.0-rc.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("structured-config", "default")
			cluster.Spec.Version = tt.version
			autoDownload := true
			cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{
				Plugin: &openbaov1alpha1.PluginConfig{
					AutoDownload: &autoDownload,
				},
			}

			infraDetails := InfrastructureDetails{
				HeadlessServiceName: cluster.Name,
				Namespace:           cluster.Namespace,
				APIPort:             8200,
				ClusterPort:         8201,
			}

			_, err := RenderHCL(cluster, infraDetails)
			if err == nil {
				t.Fatalf("RenderHCL() expected error, got nil")
			}
			if !strings.Contains(err.Error(), testMetricsListenerVersionHint) {
				t.Fatalf("RenderHCL() error = %v, want version gate error", err)
			}
		})
	}
}

func TestRenderHCLWithAllConfigurationOptions(t *testing.T) {
	cluster := newMinimalCluster("full-config", "default")
	uiEnabled := true
	cacheSize := int64(134217728) // 128MB
	disableCache := false
	detectDeadlocks := true
	rawStorageEndpoint := false
	introspectionEndpoint := true
	impreciseLeaseRoleTracking := true
	unsafeAllowAPIAuditCreation := false
	allowAuditLogPrefixing := true
	enableResponseHeaderHostname := true
	enableResponseHeaderRaftNodeID := false

	cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{
		UI:                             &uiEnabled,
		LogLevel:                       "warn",
		CacheSize:                      &cacheSize,
		DisableCache:                   &disableCache,
		DetectDeadlocks:                &detectDeadlocks,
		RawStorageEndpoint:             &rawStorageEndpoint,
		IntrospectionEndpoint:          &introspectionEndpoint,
		ImpreciseLeaseRoleTracking:     &impreciseLeaseRoleTracking,
		UnsafeAllowAPIAuditCreation:    &unsafeAllowAPIAuditCreation,
		AllowAuditLogPrefixing:         &allowAuditLogPrefixing,
		EnableResponseHeaderHostname:   &enableResponseHeaderHostname,
		EnableResponseHeaderRaftNodeID: &enableResponseHeaderRaftNodeID,
		DefaultLeaseTTL:                "3600h",
		MaxLeaseTTL:                    "7200h",
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_all_config_options", got)
}

func TestRenderHCLIncludesLeaderTLSServerNameForAutoJoin(t *testing.T) {
	cluster := newMinimalCluster("test-cluster", "default")

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_auto_join", got)
}

func TestRenderHCLWithAuditPluginsTelemetry(t *testing.T) {
	cluster := newMinimalCluster("gitops-contract", "default")
	cluster.Spec.Version = "2.5.5"
	maxCardinality := int32(2000)
	cluster.Spec.Audit = []openbaov1alpha1.AuditDevice{
		{
			Type:        "file",
			Path:        "stdout",
			Description: "File audit device",
			FileOptions: &openbaov1alpha1.FileAuditOptions{
				FilePath: "stdout",
				Mode:     "0600",
			},
		},
		{
			Type:        "socket",
			Path:        "custom-socket",
			Description: "Socket audit device (raw options)",
			Options: &apiextensionsv1.JSON{
				Raw: []byte(`{"address":"127.0.0.1:9000","timeout":42}`),
			},
		},
	}
	cluster.Spec.Plugins = []openbaov1alpha1.Plugin{
		{
			Type:       "secret",
			Name:       "example",
			Image:      "ghcr.io/example/openbao-plugin",
			Version:    "1.2.3",
			BinaryName: "openbao-plugin",
			SHA256Sum:  strings.Repeat("a", 64),
			Args:       []string{"--flag"},
			Env:        []string{"KEY=value"},
		},
	}
	cluster.Spec.Telemetry = &openbaov1alpha1.TelemetryConfig{
		UsageGaugePeriod:        "10m",
		MaximumGaugeCardinality: &maxCardinality,
		DisableHostname:         true,
		EnableHostnameLabel:     true,
		DogStatsdAddress:        "127.0.0.1:8125",
		DogStatsdTags:           []string{"env:test", "cluster:gitops-contract"},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_audit_plugins_telemetry", got)
}

func TestRenderHCLWithHTTPAuditHeaders(t *testing.T) {
	cluster := newMinimalCluster("http-audit", "default")
	cluster.Spec.Audit = []openbaov1alpha1.AuditDevice{
		{
			Type: "http",
			Path: "remote",
			HTTPOptions: &openbaov1alpha1.HTTPAuditOptions{
				URI:     "https://audit.example.test/ingest",
				Headers: &apiextensionsv1.JSON{Raw: []byte(`{"X-Audit":["one","two"],"Authorization":["Bearer token"]}`)},
			},
		},
	}

	got, err := RenderHCL(cluster, InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	})
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_http_audit_headers", got)

	rendered := string(got)
	want := `headers = "{\"X-Audit\":[\"one\",\"two\"],\"Authorization\":[\"Bearer token\"]}"`
	if !strings.Contains(rendered, want) {
		t.Fatalf("RenderHCL() headers not rendered as JSON string %q:\n%s", want, rendered)
	}
	if strings.Contains(rendered, `X-Audit =`) {
		t.Fatalf("RenderHCL() rendered HTTP headers as nested HCL instead of a JSON string:\n%s", rendered)
	}
}

func TestRenderHCLWithRawAuditOptionsCoercesScalarsToStrings(t *testing.T) {
	cluster := newMinimalCluster("raw-audit", "default")
	cluster.Spec.Audit = []openbaov1alpha1.AuditDevice{
		{
			Type: "socket",
			Path: "custom-socket",
			Options: &apiextensionsv1.JSON{
				Raw: []byte(`{"address":"127.0.0.1:9000","log_raw":true,"timeout":42}`),
			},
		},
	}

	got, err := RenderHCL(cluster, InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	})
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	rendered := string(got)
	for _, want := range []string{`log_raw = "true"`, `timeout = "42"`} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("RenderHCL() missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderHCLRejectsInvalidAuditDevices(t *testing.T) {
	tests := []struct {
		name    string
		devices []openbaov1alpha1.AuditDevice
		wantErr string
	}{
		{
			name: "duplicate path",
			devices: []openbaov1alpha1.AuditDevice{
				{
					Type:        "file",
					Path:        "stdout",
					FileOptions: &openbaov1alpha1.FileAuditOptions{FilePath: "stdout"},
				},
				{
					Type: "syslog",
					Path: "/stdout/",
					SyslogOptions: &openbaov1alpha1.SyslogAuditOptions{
						Tag: "openbao",
					},
				},
			},
			wantErr: `duplicate path "stdout"`,
		},
		{
			name: "file missing required options",
			devices: []openbaov1alpha1.AuditDevice{
				{
					Type: "file",
					Path: "stdout",
				},
			},
			wantErr: "fileOptions.filePath or options.file_path is required",
		},
		{
			name: "http headers nested in raw options",
			devices: []openbaov1alpha1.AuditDevice{
				{
					Type: "http",
					Path: "remote",
					Options: &apiextensionsv1.JSON{
						Raw: []byte(`{"uri":"https://audit.example.test/ingest","headers":{"X-Audit":["one"]}}`),
					},
				},
			},
			wantErr: `option "headers" must be a string-compatible scalar`,
		},
		{
			name: "wrong structured option family",
			devices: []openbaov1alpha1.AuditDevice{
				{
					Type:        "http",
					Path:        "remote",
					FileOptions: &openbaov1alpha1.FileAuditOptions{FilePath: "stdout"},
				},
			},
			wantErr: "fileOptions is only supported for file audit devices",
		},
		{
			name: "raw options with trailing JSON",
			devices: []openbaov1alpha1.AuditDevice{
				{
					Type: auditTypeSocket,
					Path: "custom-socket",
					Options: &apiextensionsv1.JSON{
						Raw: []byte(`{"address":"127.0.0.1:9000"}{"address":"127.0.0.1:9001"}`),
					},
				},
			},
			wantErr: "must contain exactly one JSON object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("invalid-audit", "default")
			cluster.Spec.Audit = tt.devices

			_, err := RenderHCL(cluster, InfrastructureDetails{
				HeadlessServiceName: cluster.Name,
				Namespace:           cluster.Namespace,
				APIPort:             8200,
				ClusterPort:         8201,
			})
			if err == nil {
				t.Fatal("RenderHCL() expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("RenderHCL() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestRenderHCLWithObservabilityMetricsTelemetry(t *testing.T) {
	cluster := newMinimalCluster("telemetry-cluster", "default")
	cluster.Spec.Observability = &openbaov1alpha1.ObservabilityConfig{
		Metrics: &openbaov1alpha1.MetricsConfig{
			Enabled: true,
		},
	}
	cluster.Spec.Telemetry = &openbaov1alpha1.TelemetryConfig{
		MetricsPrefix:           "openbao.e2e",
		PrometheusRetentionTime: "45s",
		EnableHostnameLabel:     true,
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	gotText := string(got)
	for _, want := range []string{
		"telemetry {",
		`metrics_prefix            = "openbao.e2e"`,
		`prometheus_retention_time = "45s"`,
		"disable_hostname          = true",
		"enable_hostname_label     = true",
	} {
		if !strings.Contains(gotText, want) {
			t.Fatalf("RenderHCL() output missing %q:\n%s", want, gotText)
		}
	}
}

func TestRenderHCLWithAllNodeMetricsListener(t *testing.T) {
	cluster := newMinimalCluster("metrics-listener", "default")
	cluster.Spec.Version = testOpenBaoVersion250
	cluster.Spec.Image = testOpenBaoImage250
	cluster.Spec.Observability = &openbaov1alpha1.ObservabilityConfig{
		Metrics: &openbaov1alpha1.MetricsConfig{
			Enabled:       true,
			ScrapeProfile: configScrapeProfileAllNodes,
		},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_metrics_only_listener", got)

	gotText := string(got)
	for _, want := range []string{
		`address              = "[::]:8200"`,
		"disallow_metrics = true",
		`address              = "[::]:8202"`,
		"metrics_only                   = true",
		"unauthenticated_metrics_access = true",
		`prometheus_retention_time = "30s"`,
	} {
		if !strings.Contains(gotText, want) {
			t.Fatalf("RenderHCL() output missing %q:\n%s", want, gotText)
		}
	}
}

func TestRenderHCLNativeTLSAutoReload(t *testing.T) {
	tests := []struct {
		name          string
		version       string
		mode          openbaov1alpha1.TLSMode
		metrics       bool
		disableAPITLS bool
		wantListeners int
	}{
		{name: "OpenBao 2.6", version: "2.6.3"},
		{name: "OpenBao 2.7 operator managed", version: "2.7.0", wantListeners: 1},
		{name: "OpenBao 2.7 external", version: "2.7.0", mode: openbaov1alpha1.TLSModeExternal, wantListeners: 1},
		{name: "OpenBao 2.7 API and metrics", version: "2.7.0", metrics: true, wantListeners: 2},
		{name: "OpenBao 2.7 TLS disabled on API listener", version: "2.7.0", disableAPITLS: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("native-tls-reload", "default")
			cluster.Spec.Version = tt.version
			cluster.Spec.Image = "openbao/openbao:" + tt.version
			cluster.Spec.TLS.Mode = tt.mode
			if tt.disableAPITLS {
				cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{Listener: &openbaov1alpha1.ListenerConfig{
					TLSDisable: &tt.disableAPITLS,
				}}
			}
			if tt.metrics {
				cluster.Spec.Observability = &openbaov1alpha1.ObservabilityConfig{Metrics: &openbaov1alpha1.MetricsConfig{
					Enabled: true, ScrapeProfile: configScrapeProfileAllNodes,
				}}
			}

			got, err := RenderHCL(cluster, InfrastructureDetails{
				HeadlessServiceName: cluster.Name, Namespace: cluster.Namespace, APIPort: 8200, ClusterPort: 8201,
			})
			if err != nil {
				t.Fatalf("RenderHCL() error = %v", err)
			}
			if count := strings.Count(string(got), "tls_auto_reload          = true"); count != tt.wantListeners {
				t.Fatalf("tls_auto_reload count = %d, want %d:\n%s", count, tt.wantListeners, got)
			}
			if count := strings.Count(string(got), `tls_auto_reload_interval = "10s"`); count != tt.wantListeners {
				t.Fatalf("tls_auto_reload_interval count = %d, want %d:\n%s", count, tt.wantListeners, got)
			}
			if tt.metrics {
				compareGolden(t, "render_hcl_tls_native_reload_metrics", got)
			}
		})
	}
}

func TestRenderHCLWithMetricsOnlyListenerRejectsUnsupportedVersions(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*openbaov1alpha1.MetricsConfig)
		wantErrSubstr string
	}{
		{
			name: "all nodes",
			configure: func(metrics *openbaov1alpha1.MetricsConfig) {
				metrics.ScrapeProfile = configScrapeProfileAllNodes
			},
			wantErrSubstr: testMetricsListenerVersionHint,
		},
		{
			name: "explicit listener",
			configure: func(metrics *openbaov1alpha1.MetricsConfig) {
				enabled := true
				metrics.MetricsOnlyListener = &openbaov1alpha1.MetricsOnlyListenerConfig{
					Enabled: &enabled,
				}
			},
			wantErrSubstr: testMetricsListenerVersionHint,
		},
		{
			name: "all nodes disabled listener",
			configure: func(metrics *openbaov1alpha1.MetricsConfig) {
				enabled := false
				metrics.ScrapeProfile = configScrapeProfileAllNodes
				metrics.MetricsOnlyListener = &openbaov1alpha1.MetricsOnlyListenerConfig{
					Enabled: &enabled,
				}
			},
			wantErrSubstr: "cannot be false when scrapeProfile is AllNodes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("metrics-version", "default")
			metrics := &openbaov1alpha1.MetricsConfig{Enabled: true}
			tt.configure(metrics)
			cluster.Spec.Observability = &openbaov1alpha1.ObservabilityConfig{Metrics: metrics}

			_, err := RenderHCL(cluster, InfrastructureDetails{
				HeadlessServiceName: cluster.Name,
				Namespace:           cluster.Namespace,
				APIPort:             8200,
				ClusterPort:         8201,
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Fatalf("RenderHCL() error = %v, want containing %q", err, tt.wantErrSubstr)
			}
		})
	}
}

func TestRenderHCLWithMetricsOnlyListenerRejectsACME(t *testing.T) {
	cluster := newMinimalCluster("metrics-acme", "default")
	cluster.Spec.Version = testOpenBaoVersion250
	cluster.Spec.Image = testOpenBaoImage250
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeACME
	cluster.Spec.TLS.ACME = &openbaov1alpha1.ACMEConfig{
		Email:        "platform@example.com",
		DirectoryURL: "https://acme.example.test/directory",
		Domains:      []string{"bao.example.test"},
	}
	cluster.Spec.Observability = &openbaov1alpha1.ObservabilityConfig{
		Metrics: &openbaov1alpha1.MetricsConfig{
			Enabled:       true,
			ScrapeProfile: configScrapeProfileAllNodes,
		},
	}

	_, err := RenderHCL(cluster, InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "metricsOnlyListener") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRenderHCLWithSelfInitRequests(t *testing.T) {
	cluster := newMinimalCluster("selfinit-cluster", "default")
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true,
		Requests: []openbaov1alpha1.SelfInitRequest{
			{
				Name:      "enable-stdout-audit",
				Operation: openbaov1alpha1.SelfInitOperationUpdate,
				Path:      "sys/audit/stdout",
				AuditDevice: &openbaov1alpha1.SelfInitAuditDevice{
					Type: "file",
					FileOptions: &openbaov1alpha1.FileAuditOptions{
						FilePath: "stdout",
					},
				},
			},
		},
	}

	got, err := RenderSelfInitHCL(cluster, nil)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	compareGolden(t, "render_self_init_requests", got)
}

func TestRenderSelfInitHCLWithProfileRequestControls(t *testing.T) {
	cluster := newMinimalCluster("selfinit-profile-controls", "default")
	cluster.Spec.Version = testOpenBaoVersion264
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true,
		Requests: []openbaov1alpha1.SelfInitRequest{
			{
				Name:         "conditional-health",
				AllowFailure: true,
				Operation:    openbaov1alpha1.SelfInitOperationRead,
				Path:         "sys/health",
				Headers: map[string][]string{
					"X-OpenBao-Trace": {"trace-1", "trace-2"},
				},
				When: &apiextensionsv1.JSON{Raw: []byte(`{"eval_source":"cel","eval_type":"bool","expression":"true"}`)},
			},
			{
				Name:      "static-skip",
				Operation: openbaov1alpha1.SelfInitOperationRead,
				Path:      "sys/seal-status",
				When:      &apiextensionsv1.JSON{Raw: []byte(`false`)},
			},
		},
	}

	got, err := RenderSelfInitHCL(cluster, nil)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	compareGolden(t, "render_self_init_profile_controls", got)

	rendered := string(got)
	for _, want := range []string{
		`headers = {`,
		`X-OpenBao-Trace = ["trace-1", "trace-2"]`,
		`when = {`,
		`eval_source = "cel"`,
		`eval_type   = "bool"`,
		`expression  = "true"`,
		`when      = false`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("RenderSelfInitHCL() output missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderSelfInitHCLRejectsInvalidProfileRequestControls(t *testing.T) {
	tests := []struct {
		name    string
		request openbaov1alpha1.SelfInitRequest
		wantErr string
	}{
		{
			name: "empty header name",
			request: openbaov1alpha1.SelfInitRequest{
				Name:      "invalid-headers",
				Operation: openbaov1alpha1.SelfInitOperationRead,
				Path:      "sys/health",
				Headers: map[string][]string{
					" ": {"value"},
				},
			},
			wantErr: "empty header name",
		},
		{
			name: "header name with surrounding whitespace",
			request: openbaov1alpha1.SelfInitRequest{
				Name:      "invalid-headers",
				Operation: openbaov1alpha1.SelfInitOperationRead,
				Path:      "sys/health",
				Headers: map[string][]string{
					" X-Trace ": {"value"},
				},
			},
			wantErr: "must not contain leading or trailing whitespace",
		},
		{
			name: "malformed when JSON",
			request: openbaov1alpha1.SelfInitRequest{
				Name:      "invalid-when",
				Operation: openbaov1alpha1.SelfInitOperationRead,
				Path:      "sys/health",
				When:      &apiextensionsv1.JSON{Raw: []byte(`{"eval_source"`)},
			},
			wantErr: "failed to decode self-init when",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("selfinit-profile-invalid", "default")
			cluster.Spec.Version = testOpenBaoVersion264
			cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
				Enabled:  true,
				Requests: []openbaov1alpha1.SelfInitRequest{tt.request},
			}

			_, err := RenderSelfInitHCL(cluster, nil)
			if err == nil {
				t.Fatal("RenderSelfInitHCL() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("RenderSelfInitHCL() error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestRenderSelfInitHCLWithInitialRecoveryKeys(t *testing.T) {
	cluster := newMinimalCluster("selfinit-recovery", "default")
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true,
	}
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "awskms",
	}
	cluster.Spec.RecoveryKeys = &openbaov1alpha1.RecoveryKeysConfig{
		Initial: &openbaov1alpha1.InitialRecoveryKeysConfig{
			Shares:    3,
			Threshold: 2,
			Recipients: []openbaov1alpha1.RecoveryKeyRecipient{
				{Name: "custodian-01", Fingerprint: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", PGPPublicKey: "pgp-key-one"},
				{Name: "custodian-02", Fingerprint: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", PGPPublicKey: "pgp-key-two"},
				{Name: "custodian-03", Fingerprint: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", PGPPublicKey: "pgp-key-three"},
			},
		},
	}

	got, err := RenderSelfInitHCL(cluster, nil)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	compareGolden(t, "render_self_init_initial_recovery_keys", got)
}

func TestRenderSelfInitHCLRejectsInvalidInitialRecoveryKeys(t *testing.T) {
	tests := []struct {
		name          string
		mutateCluster func(*openbaov1alpha1.OpenBaoCluster)
		wantErr       string
	}{
		{
			name: "requires self init",
			mutateCluster: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.SelfInit = nil
			},
			wantErr: "spec.recoveryKeys.initial requires spec.selfInit.enabled=true",
		},
		{
			name: "requires unseal configuration",
			mutateCluster: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Unseal = nil
			},
			wantErr: "requires a non-static spec.unseal.type",
		},
		{
			name: "rejects static unseal",
			mutateCluster: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{Type: "static"}
			},
			wantErr: "requires a non-static spec.unseal.type",
		},
		{
			name: "threshold cannot exceed shares",
			mutateCluster: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.RecoveryKeys.Initial.Threshold = 4
			},
			wantErr: "threshold must be less than or equal to shares",
		},
		{
			name: "recipient count must match shares",
			mutateCluster: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.RecoveryKeys.Initial.Recipients = cluster.Spec.RecoveryKeys.Initial.Recipients[:2]
			},
			wantErr: "recipients must contain exactly 3 entries",
		},
		{
			name: "rejects raw recovery init conflict",
			mutateCluster: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.SelfInit.Requests = []openbaov1alpha1.SelfInitRequest{
					{
						Name:      "raw-recovery-init",
						Operation: openbaov1alpha1.SelfInitOperationUpdate,
						Path:      "sys/rotate/recovery/init",
					},
				}
			},
			wantErr: "cannot be combined with a raw self-init request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("selfinit-recovery-invalid", "default")
			cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
			}
			cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
				Type: "awskms",
			}
			cluster.Spec.RecoveryKeys = &openbaov1alpha1.RecoveryKeysConfig{
				Initial: &openbaov1alpha1.InitialRecoveryKeysConfig{
					Shares:    3,
					Threshold: 2,
					Recipients: []openbaov1alpha1.RecoveryKeyRecipient{
						{Name: "custodian-01", PGPPublicKey: "pgp-key-one"},
						{Name: "custodian-02", PGPPublicKey: "pgp-key-two"},
						{Name: "custodian-03", PGPPublicKey: "pgp-key-three"},
					},
				},
			}

			tt.mutateCluster(cluster)

			_, err := RenderSelfInitHCL(cluster, nil)
			if err == nil {
				t.Fatal("RenderSelfInitHCL() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("RenderSelfInitHCL() error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestRenderSelfInitHCLWithHTTPAuditHeaders(t *testing.T) {
	cluster := newMinimalCluster("selfinit-http-audit", "default")
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true,
		Requests: []openbaov1alpha1.SelfInitRequest{
			{
				Name:      "enable-http-audit",
				Operation: openbaov1alpha1.SelfInitOperationUpdate,
				Path:      "sys/audit/remote",
				AuditDevice: &openbaov1alpha1.SelfInitAuditDevice{
					Type: "http",
					HTTPOptions: &openbaov1alpha1.HTTPAuditOptions{
						URI:     "https://audit.example.test/ingest",
						Headers: &apiextensionsv1.JSON{Raw: []byte(`{"X-Audit":["one"]}`)},
					},
				},
			},
		},
	}

	got, err := RenderSelfInitHCL(cluster, nil)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	rendered := string(got)
	want := `headers = "{\"X-Audit\":[\"one\"]}"`
	if !strings.Contains(rendered, want) {
		t.Fatalf("RenderSelfInitHCL() headers not rendered as JSON string %q:\n%s", want, rendered)
	}
	if strings.Contains(rendered, `X-Audit =`) {
		t.Fatalf("RenderSelfInitHCL() rendered HTTP headers as nested HCL instead of a JSON string:\n%s", rendered)
	}
}

// goldenFile reads the golden file for the given test name.
// If UPDATE_GOLDEN is set to "true", it writes the provided content to the golden file instead.
func goldenFile(t *testing.T, name string, got []byte) []byte {
	t.Helper()

	goldenPath := filepath.Join("testdata", name+".hcl")

	// If UPDATE_GOLDEN is set, write the current output to the golden file
	if os.Getenv("UPDATE_GOLDEN") == "true" {
		if err := os.WriteFile(goldenPath, got, 0644); err != nil {
			t.Fatalf("failed to write golden file %q: %v", goldenPath, err)
		}
		t.Logf("updated golden file: %s", goldenPath)
		return got
	}

	// Read the expected golden file
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("failed to read golden file %q: %v. Set UPDATE_GOLDEN=true to generate it.", goldenPath, err)
	}

	return want
}

// compareGolden compares the generated HCL with the golden file and reports differences.
func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	want := goldenFile(t, name, got)

	gotStr := string(got)
	wantStr := string(want)

	if gotStr != wantStr {
		t.Errorf("HCL output does not match golden file %q", name)
		t.Errorf("To update the golden file, run: UPDATE_GOLDEN=true go test ./internal/adapter/config -run %s", t.Name())

		// Show a simple line-by-line diff for better readability
		gotLines := strings.Split(gotStr, "\n")
		wantLines := strings.Split(wantStr, "\n")

		maxLines := len(gotLines)
		if len(wantLines) > maxLines {
			maxLines = len(wantLines)
		}

		diffCount := 0
		for i := 0; i < maxLines && diffCount < 10; i++ {
			var gotLine, wantLine string
			if i < len(gotLines) {
				gotLine = gotLines[i]
			}
			if i < len(wantLines) {
				wantLine = wantLines[i]
			}

			if gotLine != wantLine {
				diffCount++
				t.Errorf("Line %d:\n  Got:  %q\n  Want: %q", i+1, gotLine, wantLine)
			}
		}

		if diffCount >= 10 {
			t.Errorf("... (showing first 10 differences, %d total lines differ)", maxLines)
		}

		// Also show full output for small diffs (less than 50 lines)
		if maxLines < 50 {
			t.Errorf("\n--- Full Got output:\n%s\n--- Full Want output:\n%s", gotStr, wantStr)
		}
	}
}

func TestRenderSelfInitHCL_PrefersJWKSURL(t *testing.T) {
	cluster := newMinimalCluster("hardened-cluster", "default")
	config := OperatorBootstrapConfig{
		OIDCIssuerURL: "https://issuer.example",
		OIDCJWKSURL:   "https://issuer.example/keys",
		OIDCJWKSCAPEM: "-----BEGIN CERTIFICATE-----\ntest-ca\n-----END CERTIFICATE-----\n",
		JWTKeysPEM:    []string{"-----BEGIN PUBLIC KEY-----\ntest-public-key\n-----END PUBLIC KEY-----\n"},
		OperatorNS:    "openbao-operator-system",
		OperatorSA:    "openbao-operator-controller",
	}

	got, err := RenderSelfInitHCL(cluster, &config)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	rendered := string(got)
	if !strings.Contains(rendered, `jwks_url`) {
		t.Fatalf("expected rendered bootstrap to contain jwks_url, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `jwks_ca_pem`) {
		t.Fatalf("expected rendered bootstrap to contain jwks_ca_pem, got:\n%s", rendered)
	}
	if strings.Contains(rendered, `jwt_validation_pubkeys`) {
		t.Fatalf("expected rendered bootstrap to prefer jwks_url over static keys, got:\n%s", rendered)
	}
}

func TestRenderSelfInitHCL_PrefersOIDCDiscoveryURL(t *testing.T) {
	cluster := newMinimalCluster("hardened-cluster", "default")
	config := OperatorBootstrapConfig{
		OIDCIssuerURL:    "https://issuer.example",
		OIDCDiscoveryURL: "https://issuer.example",
		JWTKeysPEM:       []string{"-----BEGIN PUBLIC KEY-----\ntest-public-key\n-----END PUBLIC KEY-----\n"},
		OperatorNS:       "openbao-operator-system",
		OperatorSA:       "openbao-operator-controller",
	}

	got, err := RenderSelfInitHCL(cluster, &config)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	rendered := string(got)
	if !strings.Contains(rendered, `oidc_discovery_url`) {
		t.Fatalf("expected rendered bootstrap to contain oidc_discovery_url, got:\n%s", rendered)
	}
	if strings.Contains(rendered, `jwks_url`) {
		t.Fatalf("expected rendered bootstrap to prefer oidc_discovery_url over jwks_url, got:\n%s", rendered)
	}
	if strings.Contains(rendered, `jwt_validation_pubkeys`) {
		t.Fatalf("expected rendered bootstrap to prefer oidc_discovery_url over static keys, got:\n%s", rendered)
	}
}

func TestRenderSelfInitHCL_FallsBackToStaticKeysWhenOIDCDiscoveryRequiresCA(t *testing.T) {
	cluster := newMinimalCluster("hardened-cluster", "default")
	config := OperatorBootstrapConfig{
		OIDCIssuerURL:      "https://kubernetes.default.svc.cluster.local",
		OIDCDiscoveryURL:   "https://kubernetes.default.svc",
		OIDCDiscoveryCAPEM: "-----BEGIN CERTIFICATE-----\ntest-ca\n-----END CERTIFICATE-----\n",
		JWTKeysPEM:         []string{"-----BEGIN PUBLIC KEY-----\ntest-public-key\n-----END PUBLIC KEY-----\n"},
		OperatorNS:         "openbao-operator-system",
		OperatorSA:         "openbao-operator-controller",
	}

	got, err := RenderSelfInitHCL(cluster, &config)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	rendered := string(got)
	if strings.Contains(rendered, `oidc_discovery_url`) {
		t.Fatalf("expected rendered bootstrap to fall back to static keys when oidc_discovery_url requires a CA bundle, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `jwt_validation_pubkeys`) {
		t.Fatalf("expected rendered bootstrap to contain jwt_validation_pubkeys fallback, got:\n%s", rendered)
	}
}

func TestRenderSelfInitHCL_FallsBackToStaticKeysWhenJWKSURLHostDiffersWithoutCA(t *testing.T) {
	cluster := newMinimalCluster("hardened-cluster", "default")
	config := OperatorBootstrapConfig{
		OIDCIssuerURL: "https://kubernetes.default.svc.cluster.local",
		OIDCJWKSURL:   "https://192.168.147.2:6443/openid/v1/jwks",
		JWTKeysPEM:    []string{"-----BEGIN PUBLIC KEY-----\ntest-public-key\n-----END PUBLIC KEY-----\n"},
		OperatorNS:    "openbao-operator-system",
		OperatorSA:    "openbao-operator-controller",
	}

	got, err := RenderSelfInitHCL(cluster, &config)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	rendered := string(got)
	if strings.Contains(rendered, `jwks_url`) {
		t.Fatalf("expected rendered bootstrap to fall back to static keys when jwks_url host differs without CA, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `jwt_validation_pubkeys`) {
		t.Fatalf("expected rendered bootstrap to contain jwt_validation_pubkeys fallback, got:\n%s", rendered)
	}
}

func TestRenderSelfInitHCL_WithBootstrapConfig(t *testing.T) {
	cluster := newMinimalCluster("hardened-cluster", "default")
	cluster.Spec.Profile = openbaov1alpha1.ProfileHardened
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true,
		Requests: []openbaov1alpha1.SelfInitRequest{
			{
				Name:      "enable-stdout-audit",
				Operation: openbaov1alpha1.SelfInitOperationUpdate,
				Path:      "sys/audit/stdout",
				AuditDevice: &openbaov1alpha1.SelfInitAuditDevice{
					Type: "file",
					FileOptions: &openbaov1alpha1.FileAuditOptions{
						FilePath: "stdout",
					},
				},
			},
		},
	}

	bootstrapConfig := &OperatorBootstrapConfig{
		OIDCIssuerURL: "https://kubernetes.default.svc",
		JWTKeysPEM:    []string{"-----BEGIN PUBLIC KEY-----\ntest-public-key\n-----END PUBLIC KEY-----\n"},
		OperatorNS:    "openbao-operator-system",
		OperatorSA:    "openbao-operator-controller",
	}

	got, err := RenderSelfInitHCL(cluster, bootstrapConfig)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	compareGolden(t, "render_self_init_with_bootstrap", got)
}

func TestRenderSelfInitHCL_AutoCreatesBackupAndUpgradePolicies(t *testing.T) {
	cluster := newMinimalCluster("hardened-cluster", "default")
	cluster.Spec.Profile = openbaov1alpha1.ProfileHardened
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true,
		OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
			Enabled: true,
			AdditionalSubjects: &openbaov1alpha1.SelfInitOIDCAdditionalSubjects{
				Backup:  []openbaov1alpha1.KubernetesServiceAccountSubject{"system:serviceaccount:recovery:recovery-backup-serviceaccount"},
				Upgrade: []openbaov1alpha1.KubernetesServiceAccountSubject{"system:serviceaccount:recovery:recovery-upgrade-serviceaccount"},
			},
		},
	}
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule:    "0 3 * * *",
		JWTAuthRole: "backup",
		Target: openbaov1alpha1.BackupTarget{
			Endpoint: "https://s3.amazonaws.com",
			Bucket:   "backups",
		},
	}
	cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{
		JWTAuthRole: "upgrade",
	}

	bootstrapConfig := &OperatorBootstrapConfig{
		OIDCIssuerURL: "https://kubernetes.default.svc",
		JWTKeysPEM:    []string{"-----BEGIN PUBLIC KEY-----\ntest-public-key\n-----END PUBLIC KEY-----\n"},
		OperatorNS:    "openbao-operator-system",
		OperatorSA:    "openbao-operator-controller",
	}

	got, err := RenderSelfInitHCL(cluster, bootstrapConfig)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	compareGolden(t, "render_self_init_backup_upgrade_policies", got)
}

func TestRenderSelfInitHCL_AutoCreatesRestorePolicyAndRole(t *testing.T) {
	cluster := newMinimalCluster("hardened-cluster", "default")
	cluster.Spec.Profile = openbaov1alpha1.ProfileHardened
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true,
		OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
			Enabled: true,
			AdditionalSubjects: &openbaov1alpha1.SelfInitOIDCAdditionalSubjects{
				Restore: []openbaov1alpha1.KubernetesServiceAccountSubject{"system:serviceaccount:recovery:recovery-restore-serviceaccount"},
			},
		},
	}
	cluster.Spec.Restore = &openbaov1alpha1.RestoreConfig{
		JWTAuthRole: "restore",
	}

	bootstrapConfig := &OperatorBootstrapConfig{
		OIDCIssuerURL: "https://kubernetes.default.svc",
		JWTKeysPEM:    []string{"-----BEGIN PUBLIC KEY-----\ntest-public-key\n-----END PUBLIC KEY-----\n"},
		OperatorNS:    "openbao-operator-system",
		OperatorSA:    "openbao-operator-controller",
	}

	got, err := RenderSelfInitHCL(cluster, bootstrapConfig)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	compareGolden(t, "render_self_init_restore_policy", got)
}

func TestRenderSelfInitHCL_AdditionalSubjectsRemainRoleScoped(t *testing.T) {
	cluster := newMinimalCluster("source", "source-ns")
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true,
		OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
			Enabled: true,
			AdditionalSubjects: &openbaov1alpha1.SelfInitOIDCAdditionalSubjects{
				Operator: []openbaov1alpha1.KubernetesServiceAccountSubject{"system:serviceaccount:recovery-system:recovery-controller"},
				Backup:   []openbaov1alpha1.KubernetesServiceAccountSubject{"system:serviceaccount:recovery:target-backup-serviceaccount"},
				Restore:  []openbaov1alpha1.KubernetesServiceAccountSubject{"system:serviceaccount:recovery:target-restore-serviceaccount"},
				Upgrade:  []openbaov1alpha1.KubernetesServiceAccountSubject{"system:serviceaccount:recovery:target-upgrade-serviceaccount"},
			},
		},
	}
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule: "0 3 * * *",
		Target: openbaov1alpha1.BackupTarget{
			Endpoint: "https://s3.amazonaws.com",
			Bucket:   "backups",
		},
	}
	cluster.Spec.Restore = &openbaov1alpha1.RestoreConfig{}
	cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{}

	got, err := RenderSelfInitHCL(cluster, &OperatorBootstrapConfig{
		OIDCIssuerURL: "https://kubernetes.default.svc",
		JWTKeysPEM:    []string{"test-public-key"},
		OperatorNS:    "source-system",
		OperatorSA:    "source-controller",
	})
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	rendered := string(got)
	tests := []struct {
		role       string
		subject    string
		notAllowed []string
	}{
		{
			role:    authRoleNameOperator,
			subject: "system:serviceaccount:recovery-system:recovery-controller",
			notAllowed: []string{
				"system:serviceaccount:recovery:target-backup-serviceaccount",
				"system:serviceaccount:recovery:target-restore-serviceaccount",
				"system:serviceaccount:recovery:target-upgrade-serviceaccount",
			},
		},
		{
			role:    authRoleNameBackup,
			subject: "system:serviceaccount:recovery:target-backup-serviceaccount",
			notAllowed: []string{
				"system:serviceaccount:recovery-system:recovery-controller",
				"system:serviceaccount:recovery:target-restore-serviceaccount",
				"system:serviceaccount:recovery:target-upgrade-serviceaccount",
			},
		},
		{
			role:    authRoleNameRestore,
			subject: "system:serviceaccount:recovery:target-restore-serviceaccount",
			notAllowed: []string{
				"system:serviceaccount:recovery-system:recovery-controller",
				"system:serviceaccount:recovery:target-backup-serviceaccount",
				"system:serviceaccount:recovery:target-upgrade-serviceaccount",
			},
		},
		{
			role:    authRoleNameUpgrade,
			subject: "system:serviceaccount:recovery:target-upgrade-serviceaccount",
			notAllowed: []string{
				"system:serviceaccount:recovery-system:recovery-controller",
				"system:serviceaccount:recovery:target-backup-serviceaccount",
				"system:serviceaccount:recovery:target-restore-serviceaccount",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			block := selfInitRequestBlockForPath(t, rendered, pathAuthJWTRolePrefix+tt.role)
			if !strings.Contains(block, "bound_claims") || !strings.Contains(block, tt.subject) {
				t.Fatalf("role %q does not contain its additional exact subject:\n%s", tt.role, block)
			}
			if strings.Contains(block, "bound_subject") {
				t.Fatalf("role %q contains bound_subject with multiple subjects:\n%s", tt.role, block)
			}
			for _, subject := range tt.notAllowed {
				if strings.Contains(block, subject) {
					t.Fatalf("role %q contains subject for another role %q:\n%s", tt.role, subject, block)
				}
			}
		})
	}
}

func selfInitRequestBlockForPath(t *testing.T, rendered, path string) string {
	t.Helper()
	pathIndex := strings.Index(rendered, `"`+path+`"`)
	if pathIndex < 0 {
		t.Fatalf("rendered HCL does not contain path %q:\n%s", path, rendered)
	}
	start := strings.LastIndex(rendered[:pathIndex], `  request "`)
	if start < 0 {
		t.Fatalf("rendered HCL does not contain request start for path %q:\n%s", path, rendered)
	}
	next := strings.Index(rendered[pathIndex:], "\n  request \"")
	if next < 0 {
		return rendered[start:]
	}
	return rendered[start : pathIndex+next]
}

func TestRenderSelfInitHCL_DoesNotCreateBackupUpgradePoliciesWhenNotConfigured(t *testing.T) {
	cluster := newMinimalCluster("hardened-cluster", "default")
	cluster.Spec.Profile = openbaov1alpha1.ProfileHardened
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true,
	}
	// Backup and upgrade are not configured with jwtAuthRole

	bootstrapConfig := &OperatorBootstrapConfig{
		OIDCIssuerURL: "https://kubernetes.default.svc",
		JWTKeysPEM:    []string{"-----BEGIN PUBLIC KEY-----\ntest-public-key\n-----END PUBLIC KEY-----\n"},
		OperatorNS:    "openbao-operator-system",
		OperatorSA:    "openbao-operator-controller",
	}

	got, err := RenderSelfInitHCL(cluster, bootstrapConfig)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	compareGolden(t, "render_self_init_no_backup_upgrade", got)
}

func TestRenderSelfInitHCL_DoesNotCreateBackupPolicyWhenUsingTokenSecretRef(t *testing.T) {
	cluster := newMinimalCluster("hardened-cluster", "default")
	cluster.Spec.Profile = openbaov1alpha1.ProfileHardened
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true,
	}
	// Backup is configured with TokenSecretRef instead of JWTAuthRole.
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule: "0 3 * * *",
		Target: openbaov1alpha1.BackupTarget{
			Endpoint: "https://s3.amazonaws.com",
			Bucket:   "backups",
		},
		TokenSecretRef: &corev1.LocalObjectReference{
			Name: "backup-token",
		},
		// JWTAuthRole is not set - using TokenSecretRef instead.
	}

	bootstrapConfig := &OperatorBootstrapConfig{
		OIDCIssuerURL: "https://kubernetes.default.svc",
		JWTKeysPEM:    []string{"-----BEGIN PUBLIC KEY-----\ntest-public-key\n-----END PUBLIC KEY-----\n"},
		OperatorNS:    "openbao-operator-system",
		OperatorSA:    "openbao-operator-controller",
	}

	got, err := RenderSelfInitHCL(cluster, bootstrapConfig)
	if err != nil {
		t.Fatalf("RenderSelfInitHCL() error = %v", err)
	}

	compareGolden(t, "render_self_init_token_secret_ref", got)
}

func TestRenderHCL_ACMEMode_RendersACMEConfig(t *testing.T) {
	cluster := newMinimalCluster("acme-cluster", "default")
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeACME
	cluster.Spec.TLS.ACME = &openbaov1alpha1.ACMEConfig{
		DirectoryURL: "https://acme-v02.api.letsencrypt.org/directory",
		Domains:      []string{"example.com"},
		Email:        "admin@example.com",
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_acme_mode", got)
}

func TestRenderHCL_ACMEMode_NoRetryJoinTLSFiles(t *testing.T) {
	cluster := newMinimalCluster("acme-cluster", "default")
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeACME
	cluster.Spec.TLS.ACME = &openbaov1alpha1.ACMEConfig{
		DirectoryURL: "https://acme-v02.api.letsencrypt.org/directory",
		Domains:      []string{"example.com"},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_acme_mode_no_email", got)
}

func TestRenderHCL_ACMEMode_DefaultDomain(t *testing.T) {
	cluster := newMinimalCluster("acme-cluster", "default")
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeACME
	cluster.Spec.TLS.ACME = &openbaov1alpha1.ACMEConfig{
		DirectoryURL: "https://acme-v02.api.letsencrypt.org/directory",
		// Domain(s) omitted - operator should default to an internal Service domain.
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_acme_mode_default_domain", got)
}

func TestRenderHCL_ACMEMode_RequiresACMEConfig(t *testing.T) {
	cluster := newMinimalCluster("acme-cluster", "default")
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeACME
	// ACME config is missing

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	_, err := RenderHCL(cluster, infraDetails)
	if err == nil {
		t.Fatal("RenderHCL() expected error for ACME mode without ACME config")
	}
	if !strings.Contains(err.Error(), "ACME configuration is required when tls.mode is ACME") {
		t.Fatalf("RenderHCL() error = %v, want error containing 'ACME configuration is required when tls.mode is ACME'", err)
	}
}

func TestRenderHCL_StaticSeal_Default(t *testing.T) {
	cluster := newMinimalCluster("static-seal", "default")
	// No Unseal config - should default to static with operator defaults

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_static_seal_default", got)
}

func TestRenderHCL_StaticSeal_Custom(t *testing.T) {
	cluster := newMinimalCluster("static-seal-custom", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "static",
		Static: &openbaov1alpha1.StaticSealConfig{
			CurrentKey:   "file:///custom/path/key",
			CurrentKeyID: "custom-key-v1",
		},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_static_seal_custom", got)
}

func TestRenderHCL_TransitSeal(t *testing.T) {
	cluster := newMinimalCluster("transit-seal", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "transit",
		Transit: &openbaov1alpha1.TransitSealConfig{
			Address:       "https://openbao:8200",
			KeyName:       "transit-key",
			MountPath:     "transit/",
			Namespace:     "ns1/",
			TLSCACert:     "/etc/openbao/ca_cert.pem",
			TLSSkipVerify: boolPtr(false),
		},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_transit_seal", got)
}

func TestRenderHCL_AWSKMSSeal(t *testing.T) {
	cluster := newMinimalCluster("awskms-seal", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "awskms",
		AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
			Region:   "us-east-1",
			KMSKeyID: "alias/my-key",
			Endpoint: "https://kms.us-east-1.amazonaws.com",
		},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_awskms_seal", got)
}

func TestRenderHCL_AzureKeyVaultSeal(t *testing.T) {
	cluster := newMinimalCluster("azure-seal", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "azurekeyvault",
		AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
			VaultName: "my-vault",
			KeyName:   "my-key",
			TenantID:  "tenant-123",
			ClientID:  "client-456",
			Resource:  "managedhsm.azure.net",
		},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_azure_keyvault_seal", got)
}

func TestRenderHCL_AzureKeyVaultSealWorkloadIdentity(t *testing.T) {
	cluster := newMinimalCluster("azure-seal-wi", "default")
	cluster.Spec.Version = "2.6.3"
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "azurekeyvault",
		AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
			VaultName:  "my-vault",
			KeyName:    "my-key",
			TenantID:   "tenant-123",
			ClientID:   "client-456",
			AuthMethod: "workload_identity",
		},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_azure_keyvault_seal_workload_identity", got)
}

func TestRenderHCL_GCPCloudKMSSeal(t *testing.T) {
	cluster := newMinimalCluster("gcp-seal", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "gcpckms",
		GCPCloudKMS: &openbaov1alpha1.GCPCloudKMSSealConfig{
			Project:     "my-project",
			Region:      "us-central1",
			KeyRing:     "my-keyring",
			CryptoKey:   "my-cryptokey",
			Credentials: "/etc/gcp/credentials.json",
		},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_gcp_cloudkms_seal", got)
}

func TestRenderHCL_KMIPSeal(t *testing.T) {
	cluster := newMinimalCluster("kmip-seal", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "kmip",
		KMIP: &openbaov1alpha1.KMIPSealConfig{
			Endpoint:     "kmip.example.com:5696",
			KMSKeyID:     "openbao-kmip-key",
			ClientCert:   "/etc/kmip/client.crt",
			ClientKey:    "/etc/kmip/client.key",
			CACert:       "/etc/kmip/ca.pem",
			ServerName:   "kmip.example.com",
			Timeout:      int32Ptr(30),
			EncryptAlg:   "AES_GCM",
			TLS12Ciphers: "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
		},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_kmip_seal", got)
}

func TestRenderHCL_OCIKMSSeal(t *testing.T) {
	cluster := newMinimalCluster("oci-seal", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "ocikms",
		OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
			KeyID:              "ocid1.key.oc1..example",
			CryptoEndpoint:     "https://kms.us-ashburn-1.oraclecloud.com",
			ManagementEndpoint: "https://kms.us-ashburn-1.oraclecloud.com",
			AuthTypeAPIKey:     boolPtr(true),
		},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_ocikms_seal", got)
}

func TestRenderHCL_PKCS11Seal(t *testing.T) {
	cluster := newMinimalCluster("pkcs11-seal", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "pkcs11",
		PKCS11: &openbaov1alpha1.PKCS11SealConfig{
			Lib:                       "/usr/lib/libpkcs11.so",
			TokenLabel:                "openbao-token",
			KeyLabel:                  "openbao-hsm-key",
			KeyID:                     "01",
			Mechanism:                 "0x0009",
			DisableSoftwareEncryption: boolPtr(true),
			RSAOAEPHash:               "sha256",
		},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_pkcs11_seal", got)
}

func TestRenderHCL_KMSPluginSeal(t *testing.T) {
	cluster := newMinimalCluster("kms-plugin-seal", "default")
	cluster.Spec.Version = testOpenBaoVersion264
	cluster.Spec.Image = "openbao/openbao:2.6.4"
	cluster.Spec.Plugins = []openbaov1alpha1.Plugin{
		{
			Type:       "kms",
			Name:       "corp-kms",
			Command:    "openbao-kms-corp",
			Version:    "0.0.0-dev",
			BinaryName: "openbao-kms-corp",
			SHA256Sum:  strings.Repeat("b", 64),
		},
	}
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "kms",
		KMS: &openbaov1alpha1.KMSPluginSealConfig{
			PluginName: "corp-kms",
			Config: map[string]string{
				"ca_file":    "/etc/bao/seal-creds/ca.pem",
				"cluster_id": "prod-eu1",
				"endpoint":   "https://kms.internal.example",
				"mode":       "envelope",
				"node_id":    "openbao.openbao",
			},
		},
	}

	infraDetails := InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	got, err := RenderHCL(cluster, infraDetails)
	if err != nil {
		t.Fatalf("RenderHCL() error = %v", err)
	}

	compareGolden(t, "render_hcl_kms_plugin_seal", got)

	rendered := string(got)
	for _, want := range []string{
		`plugin_directory = "/openbao/plugins"`,
		`seal "corp-kms" {`,
		`ca_file    = "/etc/bao/seal-creds/ca.pem"`,
		`cluster_id = "prod-eu1"`,
		`endpoint   = "https://kms.internal.example"`,
		`mode       = "envelope"`,
		`node_id    = "openbao.openbao"`,
		`plugin "kms" "corp-kms" {`,
		`command     = "openbao-kms-corp"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("RenderHCL() missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderHCL_KMSPluginSealValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*openbaov1alpha1.OpenBaoCluster)
		wantErr string
	}{
		{
			name: "requires kms config",
			mutate: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Unseal.KMS = nil
			},
			wantErr: "unseal.kms is required",
		},
		{
			name: "requires plugin name",
			mutate: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Unseal.KMS.PluginName = " "
			},
			wantErr: "unseal.kms.pluginName is required",
		},
		{
			name: "requires matching kms plugin",
			mutate: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Plugins[0].Type = "secret"
			},
			wantErr: "must reference a spec.plugins entry with type \"kms\"",
		},
		{
			name: "rejects invalid config key",
			mutate: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Unseal.KMS.Config["bad key"] = "value"
			},
			wantErr: "must be a valid HCL identifier",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalCluster("kms-plugin-seal-invalid", "default")
			cluster.Spec.Version = testOpenBaoVersion264
			cluster.Spec.Plugins = []openbaov1alpha1.Plugin{
				{
					Type:       "kms",
					Name:       "corp-kms",
					Command:    "openbao-kms-corp",
					Version:    "0.0.0-dev",
					BinaryName: "openbao-kms-corp",
					SHA256Sum:  strings.Repeat("b", 64),
				},
			}
			cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
				Type: "kms",
				KMS: &openbaov1alpha1.KMSPluginSealConfig{
					PluginName: "corp-kms",
					Config: map[string]string{
						"mode": "broker",
					},
				},
			}
			tt.mutate(cluster)

			_, err := RenderHCL(cluster, InfrastructureDetails{
				HeadlessServiceName: cluster.Name,
				Namespace:           cluster.Namespace,
				APIPort:             8200,
				ClusterPort:         8201,
			})
			if err == nil {
				t.Fatal("RenderHCL() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("RenderHCL() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool {
	return &b
}

func int32Ptr(v int32) *int32 {
	return &v
}
