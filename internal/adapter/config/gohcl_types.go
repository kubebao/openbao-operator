package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

type hclCoreAttributes struct {
	UI              bool    `hcl:"ui"`
	ClusterName     string  `hcl:"cluster_name"`
	APIAddr         string  `hcl:"api_addr"`
	ClusterAddr     string  `hcl:"cluster_addr"`
	PluginDirectory *string `hcl:"plugin_directory"`
}

type hclListenerTCP struct {
	Type                      string    `hcl:"type,label"`
	Address                   string    `hcl:"address"`
	ClusterAddress            *string   `hcl:"cluster_address"`
	TLSDisable                int       `hcl:"tls_disable"`
	MaxRequestDuration        string    `hcl:"max_request_duration"`
	TLSMinVersion             *string   `hcl:"tls_min_version"`
	TLSMaxVersion             *string   `hcl:"tls_max_version"`
	TLSKeyExchangePreferences *[]string `hcl:"tls_key_exchange_preferences"`

	ProxyProtocolBehavior        *string   `hcl:"proxy_protocol_behavior"`
	ProxyProtocolAuthorizedAddrs *[]string `hcl:"proxy_protocol_authorized_addrs"`

	TLSCertFile                 *string   `hcl:"tls_cert_file"`
	TLSKeyFile                  *string   `hcl:"tls_key_file"`
	TLSClientCAFile             *string   `hcl:"tls_client_ca_file"`
	TLSAutoReload               *bool     `hcl:"tls_auto_reload"`
	TLSAutoReloadInterval       *string   `hcl:"tls_auto_reload_interval"`
	TLSACMECADir                *string   `hcl:"tls_acme_ca_directory"`
	TLSACMEDomains              *[]string `hcl:"tls_acme_domains"`
	TLSACMEEmail                *string   `hcl:"tls_acme_email"`
	TLSACMECachePath            *string   `hcl:"tls_acme_cache_path"`
	TLSACMEDisableHTTPChallenge *bool     `hcl:"tls_acme_disable_http_challenge"`
	TLSACMECARoot               *string   `hcl:"tls_acme_ca_root"`
}

type hclListenerTelemetry struct {
	UnauthenticatedMetricsAccess *bool `hcl:"unauthenticated_metrics_access"`
	DisallowMetrics              *bool `hcl:"disallow_metrics"`
	MetricsOnly                  *bool `hcl:"metrics_only"`
}

type hclStorageRaft struct {
	Type                  string `hcl:"type,label"`
	Path                  string `hcl:"path"`
	NodeID                string `hcl:"node_id"`
	PerformanceMultiplier *int32 `hcl:"performance_multiplier"`

	RetryJoinAsNonVoter *bool `hcl:"retry_join_as_non_voter"`
}

type hclRetryJoin struct {
	AutoJoin             string  `hcl:"auto_join"`
	LeaderTLSServerName  string  `hcl:"leader_tls_servername"`
	LeaderCACertFile     *string `hcl:"leader_ca_cert_file"`
	LeaderClientCertFile *string `hcl:"leader_client_cert_file"`
	LeaderClientKeyFile  *string `hcl:"leader_client_key_file"`
}

type hclAuditDevice struct {
	Type string `hcl:"type,label"`
	Path string `hcl:"path,label"`

	Description *string `hcl:"description"`
}

type hclPlugin struct {
	Type string `hcl:"type,label"`
	Name string `hcl:"name,label"`

	Image   *string `hcl:"image"`
	Command *string `hcl:"command"`

	Version    *string `hcl:"version"`
	BinaryName *string `hcl:"binary_name"`
	SHA256Sum  *string `hcl:"sha256sum"`

	Args *[]string `hcl:"args"`
	Env  *[]string `hcl:"env"`
}

type hclTelemetry struct {
	UsageGaugePeriod        *string `hcl:"usage_gauge_period"`
	MaximumGaugeCardinality *int32  `hcl:"maximum_gauge_cardinality"`
	DisableHostname         *bool   `hcl:"disable_hostname"`
	EnableHostnameLabel     *bool   `hcl:"enable_hostname_label"`
	MetricsPrefix           *string `hcl:"metrics_prefix"`
	LeaseMetricsEpsilon     *string `hcl:"lease_metrics_epsilon"`

	PrometheusRetentionTime *string `hcl:"prometheus_retention_time"`

	StatsiteAddress *string `hcl:"statsite_address"`
	StatsdAddress   *string `hcl:"statsd_address"`

	DogStatsdAddress *string   `hcl:"dogstatsd_addr"`
	DogStatsdTags    *[]string `hcl:"dogstatsd_tags"`

	CirconusAPIKey                     *string `hcl:"circonus_api_token"`
	CirconusAPIApp                     *string `hcl:"circonus_api_app"`
	CirconusAPIURL                     *string `hcl:"circonus_api_url"`
	CirconusSubmissionInterval         *string `hcl:"circonus_submission_interval"`
	CirconusCheckID                    *string `hcl:"circonus_check_id"`
	CirconusCheckForceMetricActivation *string `hcl:"circonus_check_force_metric_activation"`
	CirconusCheckInstanceID            *string `hcl:"circonus_check_instance_id"`
	CirconusCheckSearchTag             *string `hcl:"circonus_check_search_tag"`
	CirconusCheckDisplayName           *string `hcl:"circonus_check_display_name"`
	CirconusCheckTags                  *string `hcl:"circonus_check_tags"`
	CirconusBrokerID                   *string `hcl:"circonus_broker_id"`
	CirconusBrokerSelectTag            *string `hcl:"circonus_broker_select_tag"`

	StackdriverProjectID *string `hcl:"stackdriver_project_id"`
	StackdriverLocation  *string `hcl:"stackdriver_location"`
	StackdriverNamespace *string `hcl:"stackdriver_namespace"`
	StackdriverDebugLogs *bool   `hcl:"stackdriver_debug_logs"`
}

type hclUserConfigurationAttributes struct {
	LogLevel *string `hcl:"log_level"`

	LogFormat                      *string `hcl:"log_format"`
	LogFile                        *string `hcl:"log_file"`
	LogRotateDuration              *string `hcl:"log_rotate_duration"`
	LogRotateBytes                 *int64  `hcl:"log_rotate_bytes"`
	LogRotateMaxFiles              *int32  `hcl:"log_rotate_max_files"`
	PIDFile                        *string `hcl:"pid_file"`
	PluginFileUID                  *int64  `hcl:"plugin_file_uid"`
	PluginFilePerms                *string `hcl:"plugin_file_permissions"`
	PluginAutoDownload             *bool   `hcl:"plugin_auto_download"`
	PluginAutoRegister             *bool   `hcl:"plugin_auto_register"`
	PluginDownloadMode             *string `hcl:"plugin_download_behavior"`
	DefaultLeaseTTL                *string `hcl:"default_lease_ttl"`
	MaxLeaseTTL                    *string `hcl:"max_lease_ttl"`
	CacheSize                      *int64  `hcl:"cache_size"`
	DisableCache                   *bool   `hcl:"disable_cache"`
	DetectDeadlocks                *string `hcl:"detect_deadlocks"`
	RawStorageEndpoint             *bool   `hcl:"raw_storage_endpoint"`
	Introspection                  *bool   `hcl:"introspection_endpoint"`
	ImpreciseLeaseRoleTracking     *bool   `hcl:"imprecise_lease_role_tracking"`
	UnsafeAllowAPIAuditCreation    *bool   `hcl:"unsafe_allow_api_audit_creation"`
	AllowAuditLogPrefixing         *bool   `hcl:"allow_audit_log_prefixing"`
	EnableResponseHeaderHostname   *bool   `hcl:"enable_response_header_hostname"`
	EnableResponseHeaderRaftNodeID *bool   `hcl:"enable_response_header_raft_node_id"`
}

type hclSealStatic struct {
	Type         string `hcl:"type,label"`
	CurrentKey   string `hcl:"current_key"`
	CurrentKeyID string `hcl:"current_key_id"`
}

type hclSealTransit struct {
	Type string `hcl:"type,label"`

	Address        string  `hcl:"address"`
	Token          *string `hcl:"token"`
	KeyName        string  `hcl:"key_name"`
	MountPath      string  `hcl:"mount_path"`
	Namespace      *string `hcl:"namespace"`
	DisableRenewal *string `hcl:"disable_renewal"`
	TLSCACert      *string `hcl:"tls_ca_cert"`
	TLSClientCert  *string `hcl:"tls_client_cert"`
	TLSClientKey   *string `hcl:"tls_client_key"`
	TLSServerName  *string `hcl:"tls_server_name"`
	TLSSkipVerify  *string `hcl:"tls_skip_verify"`
}

type hclSealAWSKMS struct {
	Type string `hcl:"type,label"`

	Region       string  `hcl:"region"`
	KMSKeyID     string  `hcl:"kms_key_id"`
	Endpoint     *string `hcl:"endpoint"`
	AccessKey    *string `hcl:"access_key"`
	SecretKey    *string `hcl:"secret_key"`
	SessionToken *string `hcl:"session_token"`
}

type hclSealAzureKeyVault struct {
	Type string `hcl:"type,label"`

	VaultName    string  `hcl:"vault_name"`
	KeyName      string  `hcl:"key_name"`
	TenantID     *string `hcl:"tenant_id"`
	ClientID     *string `hcl:"client_id"`
	ClientSecret *string `hcl:"client_secret"`
	Resource     *string `hcl:"resource"`
	Environment  *string `hcl:"environment"`
	AuthMethod   *string `hcl:"auth_method"`
}

type hclSealGCPCloudKMS struct {
	Type string `hcl:"type,label"`

	Project     string  `hcl:"project"`
	Region      string  `hcl:"region"`
	KeyRing     string  `hcl:"key_ring"`
	CryptoKey   string  `hcl:"crypto_key"`
	Credentials *string `hcl:"credentials"`
}

type hclSealKMIP struct {
	Type string `hcl:"type,label"`

	Endpoint     string  `hcl:"endpoint"`
	KMSKeyID     string  `hcl:"kms_key_id"`
	ClientCert   *string `hcl:"client_cert"`
	ClientKey    *string `hcl:"client_key"`
	CACert       *string `hcl:"ca_cert"`
	ServerName   *string `hcl:"server_name"`
	Timeout      *int32  `hcl:"timeout"`
	EncryptAlg   *string `hcl:"encrypt_alg"`
	TLS12Ciphers *string `hcl:"tls12_ciphers"`
	Disabled     *string `hcl:"disabled"`
}

type hclSealOCIKMS struct {
	Type string `hcl:"type,label"`

	KeyID              string  `hcl:"key_id"`
	CryptoEndpoint     string  `hcl:"crypto_endpoint"`
	ManagementEndpoint string  `hcl:"management_endpoint"`
	AuthTypeAPIKey     *bool   `hcl:"auth_type_api_key"`
	Disabled           *string `hcl:"disabled"`
}

type hclSealPKCS11 struct {
	Type string `hcl:"type,label"`

	Lib                       string  `hcl:"lib"`
	Slot                      *string `hcl:"slot"`
	TokenLabel                *string `hcl:"token_label"`
	PIN                       *string `hcl:"pin"`
	KeyLabel                  string  `hcl:"key_label"`
	KeyID                     *string `hcl:"key_id"`
	Mechanism                 *string `hcl:"mechanism"`
	DisableSoftwareEncryption *string `hcl:"disable_software_encryption"`
	Disabled                  *string `hcl:"disabled"`
	RSAOAEPHash               *string `hcl:"rsa_oaep_hash"`
}

func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	val := v
	return &val
}

func normalizeTrailingNewline(tokens hclwrite.Tokens) hclwrite.Tokens {
	for len(tokens) > 0 && tokens[len(tokens)-1].Type == hclsyntax.TokenNewline {
		tokens = tokens[:len(tokens)-1]
	}
	if len(tokens) == 0 {
		return tokens
	}
	return append(tokens, &hclwrite.Token{
		Type:  hclsyntax.TokenNewline,
		Bytes: []byte("\n"),
	})
}

func boolPtrString(b *bool) *string {
	if b == nil {
		return nil
	}
	val := fmt.Sprintf("%t", *b)
	return &val
}

func boolPtrTrue(v bool) *bool {
	if !v {
		return nil
	}
	return boolPtrValue(true)
}

func boolPtrValue(v bool) *bool {
	val := v
	return &val
}
