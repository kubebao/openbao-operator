/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// ImageVerificationConfig configures supply chain security checks for container images.
// When enabled, verification applies to all operator-managed images for this cluster (StatefulSets, Deployments, and Jobs).
type ImageVerificationConfig struct {
	// Enabled controls whether image verification is enforced.
	Enabled bool `json:"enabled"`

	// PublicKey is the Cosign public key content used to verify the signature.
	// Required for static key verification. If empty, keyless verification will be used
	// (requires Issuer and Subject to be set).
	// +optional
	PublicKey string `json:"publicKey,omitempty"`

	// Issuer is the OIDC issuer for keyless verification (e.g., https://token.actions.githubusercontent.com).
	// Required for keyless verification when PublicKey is not provided.
	// For GitHub Actions keyless verification, use: https://token.actions.githubusercontent.com
	// +optional
	Issuer string `json:"issuer,omitempty"`

	// Subject is the OIDC subject for keyless verification.
	// Required for keyless verification when PublicKey is not provided.
	// Example (GitHub Actions): https://github.com/kubebao/openbao-operator/.github/workflows/release.yml@refs/tags/<VERSION>
	// The version in the subject MUST match the image tag version.
	// +optional
	Subject string `json:"subject,omitempty"`

	// IssuerRegExp is a regular expression for the OIDC issuer when using keyless verification.
	// Use this to allow a controlled set of issuers instead of a single exact issuer string.
	// Requires SubjectRegExp when PublicKey is not provided.
	// +optional
	IssuerRegExp string `json:"issuerRegExp,omitempty"`

	// SubjectRegExp is a regular expression for the OIDC subject when using keyless verification.
	// Use this to allow a controlled set of workflow identities instead of a single exact subject.
	// Requires IssuerRegExp when PublicKey is not provided.
	// +optional
	SubjectRegExp string `json:"subjectRegExp,omitempty"`

	// FailurePolicy defines behavior on verification failure.
	// "Block" blocks reconciliation of the affected workload when verification fails.
	// "Warn" logs an error and emits a Kubernetes Event but proceeds.
	// +kubebuilder:validation:Enum=Warn;Block
	// +kubebuilder:default=Block
	FailurePolicy string `json:"failurePolicy"`

	// IgnoreTlog controls whether to verify against the Rekor transparency log.
	// When false (default), signatures are verified against Rekor for non-repudiation.
	// When true, only signature verification is performed without transparency log checks.
	// +optional
	// +kubebuilder:default=false
	IgnoreTlog bool `json:"ignoreTlog,omitempty"`

	// ImagePullSecrets is a list of references to secrets in the same namespace
	// to use for pulling images from private registries during verification.
	// These secrets must be of type kubernetes.io/dockerconfigjson or kubernetes.io/dockercfg.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// WorkloadHardeningConfig configures optional workload hardening features.
type WorkloadHardeningConfig struct {
	// AppArmorEnabled controls whether the operator sets AppArmor profiles on
	// generated Pods and Jobs. Some Kubernetes environments do not support AppArmor;
	// this is opt-in to avoid scheduling failures.
	// +optional
	AppArmorEnabled bool `json:"appArmorEnabled,omitempty"`
}

// OpenBaoConfiguration defines the server configuration for OpenBao.
type OpenBaoConfiguration struct {
	// UI enables the built-in web interface.
	// +kubebuilder:default=true
	// +optional
	UI *bool `json:"ui,omitempty"`

	// LogLevel specifies the log level.
	// +kubebuilder:validation:Enum=trace;debug;info;warn;err
	// +kubebuilder:default=info
	// +optional
	LogLevel string `json:"logLevel,omitempty"`

	// Listener allows tuning the TCP listener.
	// Note: Address and ClusterAddress are managed by the operator and cannot be changed.
	// +optional
	Listener *ListenerConfig `json:"listener,omitempty"`

	// Raft allows tuning the Raft storage backend.
	// +optional
	Raft *RaftConfig `json:"raft,omitempty"`

	// ACMECARoot is the path to the ACME CA root certificate file.
	// This is used when TLS mode is ACME to specify a custom CA root for ACME certificate validation.
	// +optional
	ACMECARoot string `json:"acmeCARoot,omitempty"`

	// Logging allows configuring logging behavior.
	// +optional
	Logging *LoggingConfig `json:"logging,omitempty"`

	// Plugin allows configuring plugin behavior.
	// Note: This is separate from spec.plugins which defines plugin instances.
	// +optional
	Plugin *PluginConfig `json:"plugin,omitempty"`

	// DefaultLeaseTTL is the default lease TTL for tokens and secrets (e.g., "720h", "30m").
	// If not specified, OpenBao uses its default.
	// +optional
	DefaultLeaseTTL string `json:"defaultLeaseTTL,omitempty"`

	// MaxLeaseTTL is the maximum lease TTL for tokens and secrets (e.g., "8760h", "1y").
	// This must be greater than or equal to DefaultLeaseTTL.
	// If not specified, OpenBao uses its default.
	// +optional
	MaxLeaseTTL string `json:"maxLeaseTTL,omitempty"`

	// CacheSize is the size of the cache in bytes.
	// If not specified, OpenBao uses its default cache size.
	// +kubebuilder:validation:Minimum=0
	// +optional
	CacheSize *int64 `json:"cacheSize,omitempty"`

	// DisableCache disables the cache entirely.
	// When true, all caching is disabled.
	// +optional
	DisableCache *bool `json:"disableCache,omitempty"`

	// DetectDeadlocks enables deadlock detection in OpenBao.
	// This is an experimental feature for debugging.
	// Hardened clusters reject detectDeadlocks=true.
	// +optional
	DetectDeadlocks *bool `json:"detectDeadlocks,omitempty"`

	// RawStorageEndpoint enables the raw storage endpoint.
	// This is an experimental feature that exposes raw storage operations.
	// Hardened clusters reject rawStorageEndpoint=true.
	// +optional
	RawStorageEndpoint *bool `json:"rawStorageEndpoint,omitempty"`

	// IntrospectionEndpoint enables the introspection endpoint.
	// This is an experimental feature for debugging and introspection.
	// Hardened clusters reject introspectionEndpoint=true.
	// +optional
	IntrospectionEndpoint *bool `json:"introspectionEndpoint,omitempty"`

	// ImpreciseLeaseRoleTracking enables imprecise lease role tracking.
	// This is an experimental feature that may improve performance in some scenarios.
	// +optional
	ImpreciseLeaseRoleTracking *bool `json:"impreciseLeaseRoleTracking,omitempty"`

	// UnsafeAllowAPIAuditCreation allows API-based audit device creation.
	// This bypasses the normal audit device configuration validation.
	// Use with caution.
	// Hardened clusters reject unsafeAllowAPIAuditCreation=true.
	// +optional
	UnsafeAllowAPIAuditCreation *bool `json:"unsafeAllowAPIAuditCreation,omitempty"`

	// AllowAuditLogPrefixing allows audit log prefixing.
	// This enables custom prefixes in audit log entries.
	// +optional
	AllowAuditLogPrefixing *bool `json:"allowAuditLogPrefixing,omitempty"`

	// EnableResponseHeaderHostname enables the hostname in response headers.
	// When true, OpenBao includes the hostname in HTTP response headers.
	// +optional
	EnableResponseHeaderHostname *bool `json:"enableResponseHeaderHostname,omitempty"`

	// EnableResponseHeaderRaftNodeID enables the Raft node ID in response headers.
	// When true, OpenBao includes the Raft node ID in HTTP response headers.
	// +optional
	EnableResponseHeaderRaftNodeID *bool `json:"enableResponseHeaderRaftNodeID,omitempty"`
}

// TLSVersion identifies a supported TLS protocol version.
// +kubebuilder:validation:Enum=tls12;tls13
type TLSVersion string

const (
	// TLSVersion12 selects TLS 1.2.
	TLSVersion12 TLSVersion = "tls12"
	// TLSVersion13 selects TLS 1.3.
	TLSVersion13 TLSVersion = "tls13"
)

// TLSKeyExchangeGroup identifies a classical or hybrid post-quantum TLS group.
// Pure MLKEM1024 is not supported by the operator's default TLS clients.
// +kubebuilder:validation:Enum=CurveP256;CurveP384;CurveP521;X25519;X25519MLKEM768;SecP256r1MLKEM768;SecP384r1MLKEM1024
type TLSKeyExchangeGroup string

const (
	TLSKeyExchangeCurveP256          TLSKeyExchangeGroup = "CurveP256"
	TLSKeyExchangeCurveP384          TLSKeyExchangeGroup = "CurveP384"
	TLSKeyExchangeCurveP521          TLSKeyExchangeGroup = "CurveP521"
	TLSKeyExchangeX25519             TLSKeyExchangeGroup = "X25519"
	TLSKeyExchangeX25519MLKEM768     TLSKeyExchangeGroup = "X25519MLKEM768"
	TLSKeyExchangeSecP256r1MLKEM768  TLSKeyExchangeGroup = "SecP256r1MLKEM768"
	TLSKeyExchangeSecP384r1MLKEM1024 TLSKeyExchangeGroup = "SecP384r1MLKEM1024"
)

// ListenerConfig allows tuning the API and metrics TCP listeners.
type ListenerConfig struct {
	// TLSDisable controls TLS on the listener.
	// Note: This is typically managed by the operator based on spec.tls.enabled.
	// Hardened clusters reject tlsDisable=true.
	// +optional
	TLSDisable *bool `json:"tlsDisable,omitempty"`

	// TLSMinVersion sets the minimum TLS version on the API and metrics listeners.
	// If omitted, OpenBao uses TLS 1.2. TLS must be enabled.
	// +optional
	TLSMinVersion TLSVersion `json:"tlsMinVersion,omitempty"`

	// TLSMaxVersion sets the maximum TLS version on the API and metrics listeners.
	// If omitted, OpenBao uses TLS 1.3. TLS must be enabled.
	// +optional
	TLSMaxVersion TLSVersion `json:"tlsMaxVersion,omitempty"`

	// TLSKeyExchangePreferences selects allowed key exchange groups on the API
	// and metrics listeners. List order does not control preference; Go uses its
	// internal preference order. If omitted, OpenBao uses Go's default groups.
	// Requires OpenBao >= 2.7.0 and TLS enabled. A list containing only hybrid PQ
	// groups requires TLSMinVersion=tls13 and rejects classical-only clients.
	// This setting does not configure cluster traffic on port 8201.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=7
	// +listType=set
	// +optional
	TLSKeyExchangePreferences []TLSKeyExchangeGroup `json:"tlsKeyExchangePreferences,omitempty"`

	// ProxyProtocolBehavior configures PROXY protocol on the API listener.
	// Supported values are use_always, allow_authorized, and deny_unauthorized.
	// The legacy allow_any value remains in the schema for compatibility but is
	// rejected by the operator. Use allow_authorized to trust selected proxies.
	// +kubebuilder:validation:Enum=use_always;allow_any;allow_authorized;deny_unauthorized
	// +optional
	ProxyProtocolBehavior string `json:"proxyProtocolBehavior,omitempty"`

	// ProxyProtocolAuthorizedAddrs lists trusted proxy source IP addresses or CIDRs.
	// Required for allow_authorized and deny_unauthorized. With allow_authorized,
	// connections from other sources use the peer IP address instead of PROXY metadata.
	// This setting applies only to the API listener, not the metrics listener.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:MinLength=1
	// +listType=set
	// +optional
	ProxyProtocolAuthorizedAddrs []string `json:"proxyProtocolAuthorizedAddrs,omitempty"`
}

// RaftConfig allows tuning the Raft storage backend.
type RaftConfig struct {
	// PerformanceMultiplier scales the Raft timing parameters.
	// +kubebuilder:validation:Minimum=0
	// +optional
	PerformanceMultiplier *int32 `json:"performanceMultiplier,omitempty"`

	// Autopilot configures Raft Autopilot settings.
	// By default, dead server cleanup is enabled with a 5-minute threshold.
	// +optional
	Autopilot *RaftAutopilotConfig `json:"autopilot,omitempty"`
}

// RaftAutopilotConfig configures Raft Autopilot behavior for dead server cleanup.
// See: https://openbao.org/docs/concepts/integrated-storage/autopilot/
type RaftAutopilotConfig struct {
	// CleanupDeadServers enables automatic removal of dead Raft peers.
	// When enabled, Autopilot periodically removes servers that have been
	// unhealthy for longer than DeadServerLastContactThreshold.
	// Requires MinQuorum to be set (defaults to replicas/2 + 1).
	// +kubebuilder:default=true
	// +optional
	CleanupDeadServers *bool `json:"cleanupDeadServers,omitempty"`

	// DeadServerLastContactThreshold is the duration after which a server
	// is considered dead if it hasn't contacted the leader.
	// Minimum: "1m". Default: "5m" (operator default, shorter than OpenBao's 24h).
	// +kubebuilder:default="5m"
	// +optional
	DeadServerLastContactThreshold string `json:"deadServerLastContactThreshold,omitempty"`

	// MinQuorum is the minimum number of servers before Autopilot can prune
	// dead servers. This prevents removing so many servers that quorum is lost.
	// If not specified, defaults to max(3, replicas/2 + 1).
	// +kubebuilder:validation:Minimum=3
	// +optional
	MinQuorum *int32 `json:"minQuorum,omitempty"`

	// ServerStabilizationTime is the minimum time a server must be healthy
	// before being promoted to voter. Default: "10s".
	// +optional
	ServerStabilizationTime string `json:"serverStabilizationTime,omitempty"`

	// LastContactThreshold is the limit on the amount of time a server can
	// go without leader contact before being considered unhealthy.
	// Default: "10s".
	// +optional
	LastContactThreshold string `json:"lastContactThreshold,omitempty"`

	// MaxTrailingLogs is the amount of entries in the Raft Log that a server
	// can be behind before being considered unhealthy. Default: 1000.
	// +optional
	MaxTrailingLogs *int32 `json:"maxTrailingLogs,omitempty"`
}

// LoggingConfig allows configuring logging behavior for OpenBao.
type LoggingConfig struct {
	// Format specifies the log format.
	// +kubebuilder:validation:Enum=standard;json
	// +optional
	Format string `json:"format,omitempty"`

	// File is the path to the log file.
	// If not specified, logs are written to stderr.
	// +optional
	File string `json:"file,omitempty"`

	// RotateDuration specifies how often to rotate logs (e.g., "24h", "7d").
	// +optional
	RotateDuration string `json:"rotateDuration,omitempty"`

	// RotateBytes specifies the maximum size in bytes before rotating logs.
	// +kubebuilder:validation:Minimum=0
	// +optional
	RotateBytes *int64 `json:"rotateBytes,omitempty"`

	// RotateMaxFiles is the maximum number of rotated log files to keep.
	// +kubebuilder:validation:Minimum=0
	// +optional
	RotateMaxFiles *int32 `json:"rotateMaxFiles,omitempty"`

	// PIDFile is the path to write the PID file.
	// +optional
	PIDFile string `json:"pidFile,omitempty"`
}

// PluginConfig allows configuring plugin behavior.
type PluginConfig struct {
	// FileUID is the UID to use for plugin files.
	// +optional
	FileUID *int64 `json:"fileUID,omitempty"`

	// FilePermissions are the file permissions for plugin files (e.g., "0755").
	// +optional
	FilePermissions string `json:"filePermissions,omitempty"`

	// AutoDownload controls automatic plugin downloads from OCI registries.
	// +optional
	AutoDownload *bool `json:"autoDownload,omitempty"`

	// AutoRegister controls automatic plugin registration.
	// +optional
	AutoRegister *bool `json:"autoRegister,omitempty"`

	// DownloadBehavior controls whether OpenBao startup fails or continues when
	// declarative OCI plugin downloads fail. Valid values are "fail" and
	// "continue"; OpenBao defaults to "fail" when unset.
	// +kubebuilder:validation:Enum=fail;continue
	// +optional
	DownloadBehavior string `json:"downloadBehavior,omitempty"`
}

// AuditFileStorageMode controls how the operator provides shared filesystem storage for file audit logs.
// +kubebuilder:validation:Enum=ManagedPVC;ExistingPVC
type AuditFileStorageMode string

const (
	// AuditFileStorageModeManagedPVC instructs the operator to create a dedicated RWX PVC.
	AuditFileStorageModeManagedPVC AuditFileStorageMode = "ManagedPVC"
	// AuditFileStorageModeExistingPVC instructs the operator to mount an existing RWX PVC.
	AuditFileStorageModeExistingPVC AuditFileStorageMode = "ExistingPVC"
)

// AuditFileStorageConfig configures the shared filesystem integration point for file audit devices.
//
// The operator mounts the selected PVC into each OpenBao Pod. Each Pod uses a
// pod-specific subPath under the same PVC so all Pods can render the same audit
// file path while collectors can mount the PVC read-only and read per-Pod audit
// files from the backing directories. This storage is intended as a collector
// handoff and replay buffer, not as the authoritative compliance archive.
// +kubebuilder:validation:XValidation:rule="self.mode != 'ManagedPVC' || !has(self.existingClaimName) || size(self.existingClaimName) == 0",message="auditFileStorage.existingClaimName is only supported when mode is ExistingPVC"
// +kubebuilder:validation:XValidation:rule="self.mode != 'ExistingPVC' || size(self.existingClaimName) > 0",message="auditFileStorage.existingClaimName is required when mode is ExistingPVC"
// +kubebuilder:validation:XValidation:rule="self.mode != 'ExistingPVC' || !has(self.size) || size(self.size) == 0",message="auditFileStorage.size is only supported when mode is ManagedPVC"
// +kubebuilder:validation:XValidation:rule="self.mode != 'ExistingPVC' || !has(self.storageClassName) || size(self.storageClassName) == 0",message="auditFileStorage.storageClassName is only supported when mode is ManagedPVC"
// +kubebuilder:validation:XValidation:rule="self.mode != 'ManagedPVC' || size(self.size) > 0",message="auditFileStorage.size is required when mode is ManagedPVC"
// +kubebuilder:validation:XValidation:rule="!has(self.mountPath) || (self.mountPath.startsWith('/') && self.mountPath != '/')",message="auditFileStorage.mountPath must be an absolute path and must not be /"
type AuditFileStorageConfig struct {
	// Mode selects whether the operator creates a dedicated RWX PVC or mounts an existing one.
	Mode AuditFileStorageMode `json:"mode"`
	// ExistingClaimName is the name of a pre-created RWX PVC in the same namespace.
	// Required when Mode is ExistingPVC.
	// +kubebuilder:validation:MinLength=1
	// +optional
	ExistingClaimName string `json:"existingClaimName,omitempty"`
	// Size is the requested capacity for the managed audit file storage PVC.
	// Required when Mode is ManagedPVC.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Size string `json:"size,omitempty"`
	// StorageClassName is an optional StorageClass for the managed audit file storage PVC.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
	// MountPath is where the audit file storage PVC is mounted in OpenBao Pods.
	// File audit device paths must be under this path when auditFileStorage is configured.
	// +kubebuilder:default=/openbao/audit
	// +optional
	MountPath string `json:"mountPath,omitempty"`
}

// AuditDevice defines a declarative audit device configuration.
// See: https://openbao.org/docs/configuration/audit/
// +kubebuilder:validation:XValidation:rule="self.type == 'file' || !has(self.fileOptions)",message="fileOptions is only supported when type is file"
// +kubebuilder:validation:XValidation:rule="self.type == 'http' || !has(self.httpOptions)",message="httpOptions is only supported when type is http"
// +kubebuilder:validation:XValidation:rule="self.type == 'syslog' || !has(self.syslogOptions)",message="syslogOptions is only supported when type is syslog"
// +kubebuilder:validation:XValidation:rule="self.type == 'socket' || !has(self.socketOptions)",message="socketOptions is only supported when type is socket"
type AuditDevice struct {
	// Type is the type of audit device (e.g., "file", "syslog", "socket", "http").
	// +kubebuilder:validation:Enum=file;syslog;socket;http
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`
	// Path is the path of the audit device in the root namespace.
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`
	// Description is an optional description for the audit device.
	// +optional
	Description string `json:"description,omitempty"`
	// FileOptions configures options for file audit devices.
	// Only used when Type is "file".
	// +optional
	FileOptions *FileAuditOptions `json:"fileOptions,omitempty"`
	// HTTPOptions configures options for HTTP audit devices.
	// Only used when Type is "http".
	// +optional
	HTTPOptions *HTTPAuditOptions `json:"httpOptions,omitempty"`
	// SyslogOptions configures options for syslog audit devices.
	// Only used when Type is "syslog".
	// +optional
	SyslogOptions *SyslogAuditOptions `json:"syslogOptions,omitempty"`
	// SocketOptions configures options for socket audit devices.
	// Only used when Type is "socket".
	// +optional
	SocketOptions *SocketAuditOptions `json:"socketOptions,omitempty"`
	// Options contains device-specific configuration options as a string map.
	// This is a fallback for backward compatibility and advanced use cases.
	// If structured options (FileOptions, HTTPOptions, etc.) are provided, they take precedence.
	// OpenBao audit options are string-to-string; scalar JSON values are rendered as strings,
	// while nested objects and arrays are rejected. For HTTP headers, prefer httpOptions.headers.
	// +optional
	Options *apiextensionsv1.JSON `json:"options,omitempty"`
}

// FileAuditOptions configures options for file audit devices.
// See: https://openbao.org/docs/audit/file/
type FileAuditOptions struct {
	// FilePath is the path to where the audit log will be written.
	// Special keywords: "stdout" writes to standard output, "discard" discards output.
	// +kubebuilder:validation:MinLength=1
	FilePath string `json:"filePath"`
	// Mode is a string containing an octal number representing the bit pattern for the file mode.
	// Defaults to "0600" if not specified. Set to "0000" to prevent OpenBao from modifying the file mode.
	// +optional
	Mode string `json:"mode,omitempty"`
}

// HTTPAuditOptions configures options for HTTP audit devices.
// See: https://openbao.org/docs/audit/http/
type HTTPAuditOptions struct {
	// URI is the URI of the remote server where the audit logs will be written.
	// +kubebuilder:validation:MinLength=1
	URI string `json:"uri"`
	// Headers is a JSON object describing headers. Must take the shape map[string][]string,
	// i.e., an object of headers, with each having one or more values.
	// Headers without values will be ignored. The operator renders this object as OpenBao's
	// expected JSON-encoded options.headers string.
	// +optional
	Headers *apiextensionsv1.JSON `json:"headers,omitempty"`
}

// SyslogAuditOptions configures options for syslog audit devices.
// See: https://openbao.org/docs/audit/syslog/
type SyslogAuditOptions struct {
	// Facility is the syslog facility to use.
	// Defaults to "AUTH" if not specified.
	// +optional
	Facility string `json:"facility,omitempty"`
	// Tag is the syslog tag to use.
	// Defaults to "openbao" if not specified.
	// +optional
	Tag string `json:"tag,omitempty"`
}

// SocketAuditOptions configures options for socket audit devices.
// See: https://openbao.org/docs/audit/socket/
type SocketAuditOptions struct {
	// Address is the socket server address to use.
	// Example: "127.0.0.1:9090" or "/tmp/audit.sock".
	// +optional
	Address string `json:"address,omitempty"`
	// SocketType is the socket type to use, any type compatible with net.Dial is acceptable.
	// Defaults to "tcp" if not specified.
	// +optional
	SocketType string `json:"socketType,omitempty"`
	// WriteTimeout is the (deadline) time in seconds to allow writes to be completed over the socket.
	// A zero value means that write attempts will not time out.
	// Defaults to "2s" if not specified.
	// +optional
	WriteTimeout string `json:"writeTimeout,omitempty"`
}

// Plugin defines a declarative plugin configuration.
// Declarative plugins require OpenBao 2.5.0 or newer; KMS plugins require 2.6.0 or newer.
// During upgrades, declarations must also meet the recorded running version's requirements.
// See: https://openbao.org/docs/configuration/plugins/
type Plugin struct {
	// Type is the plugin type (e.g., "secret", "auth").
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`
	// Name is the name of the plugin.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Image is the OCI image URL including registry and repository.
	// Before OpenBao 2.7.0, use an untagged repository and set Version separately.
	// Required if Command is not set. Conflicts with Command.
	// +optional
	Image string `json:"image,omitempty"`
	// Command is the command name of a manually downloaded plugin.
	// Required if Image is not set. Conflicts with Image.
	// +optional
	Command string `json:"command,omitempty"`
	// Version is the plugin version and, when Image has no tag, the image tag.
	// OpenBao 2.7 and later can infer it from the image tag. Command-based KMS
	// plugins on OpenBao 2.7 and later do not require a version.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Version string `json:"version,omitempty"`
	// BinaryName is the name of the plugin binary file within the OCI image.
	// OpenBao 2.7 and later can infer it from the image ENTRYPOINT or CMD.
	// +kubebuilder:validation:MinLength=1
	// +optional
	BinaryName string `json:"binaryName,omitempty"`
	// SHA256Sum is the expected SHA256 checksum of the plugin binary.
	// Must be a 64-character hexadecimal string.
	// OpenBao 2.7 and later allow omission for digest-pinned OCI images and
	// command-based plugins. Earlier versions require this field.
	// +kubebuilder:validation:MinLength=64
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{64}$`
	// +optional
	SHA256Sum string `json:"sha256sum,omitempty"`
	// Args are arguments to pass to the running plugin.
	// Only used if plugin_auto_register=true is set.
	// +optional
	Args []string `json:"args,omitempty"`
	// Env are environment variables to pass to the running plugin.
	// Only used if plugin_auto_register=true is set.
	// +optional
	Env []string `json:"env,omitempty"`
}

// TelemetryConfig defines telemetry reporting configuration.
// See: https://openbao.org/docs/configuration/telemetry/
type TelemetryConfig struct {
	// Common telemetry options
	// UsageGaugePeriod specifies the interval at which high-cardinality usage data is collected.
	// +optional
	UsageGaugePeriod string `json:"usageGaugePeriod,omitempty"`
	// MaximumGaugeCardinality is the maximum cardinality of gauge labels.
	// +optional
	MaximumGaugeCardinality *int32 `json:"maximumGaugeCardinality,omitempty"`
	// DisableHostname specifies if gauge values should be prefixed with the local hostname.
	// +optional
	DisableHostname bool `json:"disableHostname,omitempty"`
	// EnableHostnameLabel specifies if all metric values should contain the host label.
	// +optional
	EnableHostnameLabel bool `json:"enableHostnameLabel,omitempty"`
	// MetricsPrefix specifies the prefix used for metric values.
	// +optional
	MetricsPrefix string `json:"metricsPrefix,omitempty"`
	// LeaseMetricsEpsilon specifies the size of the bucket used to measure future lease expiration.
	// +optional
	LeaseMetricsEpsilon string `json:"leaseMetricsEpsilon,omitempty"`

	// Prometheus-specific options
	// PrometheusRetentionTime specifies how long to retain metrics in Prometheus format.
	// +optional
	PrometheusRetentionTime string `json:"prometheusRetentionTime,omitempty"`

	// Statsite-specific options
	// StatsiteAddress is the address of the statsite server.
	// +optional
	StatsiteAddress string `json:"statsiteAddress,omitempty"`

	// StatsD-specific options
	// StatsdAddress is the address of the StatsD server.
	// +optional
	StatsdAddress string `json:"statsdAddress,omitempty"`

	// DogStatsD-specific options
	// DogStatsdAddress is the address of the DogStatsD server.
	// +optional
	DogStatsdAddress string `json:"dogStatsdAddress,omitempty"`
	// DogStatsdTags are tags to add to all metrics.
	// +optional
	DogStatsdTags []string `json:"dogStatsdTags,omitempty"`

	// Circonus-specific options
	// CirconusAPIKey is the API key for Circonus.
	// +optional
	CirconusAPIKey string `json:"circonusAPIKey,omitempty"`
	// CirconusAPIApp is the API app name for Circonus.
	// +optional
	CirconusAPIApp string `json:"circonusAPIApp,omitempty"`
	// CirconusAPIURL is the API URL for Circonus.
	// +optional
	CirconusAPIURL string `json:"circonusAPIURL,omitempty"`
	// CirconusSubmissionInterval is the submission interval for Circonus.
	// +optional
	CirconusSubmissionInterval string `json:"circonusSubmissionInterval,omitempty"`
	// CirconusCheckID is the check ID for Circonus.
	// +optional
	CirconusCheckID string `json:"circonusCheckID,omitempty"`
	// CirconusCheckForceMetricActivation forces metric activation in Circonus.
	// +optional
	CirconusCheckForceMetricActivation string `json:"circonusCheckForceMetricActivation,omitempty"`
	// CirconusCheckInstanceID is the instance ID for Circonus.
	// +optional
	CirconusCheckInstanceID string `json:"circonusCheckInstanceID,omitempty"`
	// CirconusCheckSearchTag is the search tag for Circonus.
	// +optional
	CirconusCheckSearchTag string `json:"circonusCheckSearchTag,omitempty"`
	// CirconusCheckDisplayName is the display name for Circonus.
	// +optional
	CirconusCheckDisplayName string `json:"circonusCheckDisplayName,omitempty"`
	// CirconusCheckTags is the tags for Circonus.
	// +optional
	CirconusCheckTags string `json:"circonusCheckTags,omitempty"`
	// CirconusBrokerID is the broker ID for Circonus.
	// +optional
	CirconusBrokerID string `json:"circonusBrokerID,omitempty"`
	// CirconusBrokerSelectTag is the broker select tag for Circonus.
	// +optional
	CirconusBrokerSelectTag string `json:"circonusBrokerSelectTag,omitempty"`

	// Stackdriver-specific options
	// StackdriverProjectID is the Google Cloud Project ID.
	// +optional
	StackdriverProjectID string `json:"stackdriverProjectID,omitempty"`
	// StackdriverLocation is the GCP or AWS region.
	// +optional
	StackdriverLocation string `json:"stackdriverLocation,omitempty"`
	// StackdriverNamespace is a namespace identifier for the telemetry data.
	// +optional
	StackdriverNamespace string `json:"stackdriverNamespace,omitempty"`
	// StackdriverDebugLogs specifies if OpenBao writes additional stackdriver debug logs.
	// +optional
	StackdriverDebugLogs bool `json:"stackdriverDebugLogs,omitempty"`
}
