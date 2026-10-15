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

import apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

// SelfInitOperation defines valid operations for self-initialization requests.
// +kubebuilder:validation:Enum=create;read;update;delete;list;patch
type SelfInitOperation string

const (
	// SelfInitOperationCreate creates a new resource.
	SelfInitOperationCreate SelfInitOperation = "create"
	// SelfInitOperationRead reads an existing resource.
	SelfInitOperationRead SelfInitOperation = "read"
	// SelfInitOperationUpdate updates an existing resource.
	SelfInitOperationUpdate SelfInitOperation = "update"
	// SelfInitOperationPatch performs a partial update to an existing resource.
	SelfInitOperationPatch SelfInitOperation = "patch"
	// SelfInitOperationDelete deletes an existing resource.
	SelfInitOperationDelete SelfInitOperation = "delete"
	// SelfInitOperationList lists resources.
	SelfInitOperationList SelfInitOperation = "list"
)

// SelfInitConfig enables OpenBao's self-initialization feature.
// When enabled, OpenBao initializes itself on first start using the configured
// requests, and the root token is automatically revoked.
// See: https://openbao.org/docs/configuration/self-init/
type SelfInitConfig struct {
	// Enabled activates OpenBao's self-initialization feature.
	// When true, the Operator injects initialize stanzas into config.hcl
	// and does NOT create a root token Secret (root token is auto-revoked).
	//
	// WARNING: The root token is auto-revoked during initialization. You MUST
	// configure user authentication (e.g., userpass, JWT, Kubernetes auth) via
	// spec.selfInit.requests before enabling this. spec.selfInit.oidc.enabled
	// only provides Operator authentication for lifecycle tasks, NOT user access.
	// Enabling without user authentication results in permanent lockout.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`
	// OIDC configures JWT authentication for the Operator to perform cluster
	// lifecycle operations (backups, upgrades, restores). When enabled, this
	// sets up the jwt-operator auth method, OIDC discovery, and operator roles.
	// This is for Operator authentication only - users must configure their own
	// authentication methods via spec.selfInit.requests.
	// +optional
	OIDC *SelfInitOIDCConfig `json:"oidc,omitempty"`
	// Requests defines the API operations to execute during self-initialization.
	// Each request becomes a named request block inside an initialize stanza.
	// +optional
	Requests []SelfInitRequest `json:"requests,omitempty"`
}

// SelfInitRequest defines a single API operation to execute during self-initialization.
type SelfInitRequest struct {
	// Name is a unique identifier for this request (used as the block name).
	// Must match regex ^[A-Za-z_][A-Za-z0-9_-]*$
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_-]*$`
	Name string `json:"name"`
	// Operation is the API operation type: create, read, update, delete, or list.
	// +kubebuilder:validation:Enum=create;read;update;delete;list;patch
	Operation SelfInitOperation `json:"operation"`
	// Path is the API path to call (e.g., "sys/audit/stdout", "auth/kubernetes/config").
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`
	// Headers contains additional HTTP headers to send with this self-init request.
	// Requires OpenBao 2.6.0 or newer.
	// Header names must not be empty. Values are rendered into OpenBao's profile-engine
	// `headers` request field as map[string][]string.
	// +optional
	Headers map[string][]string `json:"headers,omitempty"`
	// When controls whether OpenBao executes this request.
	// Requires OpenBao 2.6.0 or newer.
	// Omit it to execute the request. Set it to a JSON boolean for static gating
	// or to an OpenBao profile value object for dynamic evaluation, for example
	// {"eval_source":"cel","eval_type":"bool","expression":"true"}.
	// +optional
	When *apiextensionsv1.JSON `json:"when,omitempty"`
	// AuditDevice configures an audit device when Path starts with "sys/audit/".
	// This provides structured configuration for audit devices instead of raw JSON.
	// Only used when Path matches the pattern "sys/audit/*".
	// +optional
	AuditDevice *SelfInitAuditDevice `json:"auditDevice,omitempty"`
	// AuthMethod configures an auth method when Path starts with "sys/auth/".
	// This provides structured configuration for enabling auth methods.
	// Only used when Path matches the pattern "sys/auth/*".
	// +optional
	AuthMethod *SelfInitAuthMethod `json:"authMethod,omitempty"`
	// SecretEngine configures a secret engine when Path starts with "sys/mounts/".
	// This provides structured configuration for enabling secret engines.
	// Only used when Path matches the pattern "sys/mounts/*".
	// +optional
	SecretEngine *SelfInitSecretEngine `json:"secretEngine,omitempty"`
	// Policy configures a policy when Path starts with "sys/policies/".
	// This provides structured configuration for creating/updating policies.
	// Only used when Path matches the pattern "sys/policies/*".
	// +optional
	Policy *SelfInitPolicy `json:"policy,omitempty"`
	// Data contains the request payload for paths that don't have structured types.
	// This must be a JSON/YAML object whose shape matches the target API endpoint.
	// Nested maps and lists are supported and are rendered into the initialize stanza as HCL objects.
	//
	// **Note:** For common paths, use structured types instead:
	// - `sys/audit/*` → use `auditDevice`
	// - `sys/auth/*` → use `authMethod`
	// - `sys/mounts/*` → use `secretEngine`
	// - `sys/policies/*` → use `policy`
	//
	// This payload is stored in the OpenBaoCluster resource and persisted in etcd;
	// it must not contain sensitive values such as tokens, passwords, or unseal keys.
	// +optional
	Data *apiextensionsv1.JSON `json:"data,omitempty"`
	// DataFrom maps top-level request fields to required keys in same-namespace
	// Secrets. Values are read as strings during first self-initialization.
	// Requires OpenBao 2.6.0 or newer. Use only with raw request payloads, not
	// auditDevice, authMethod, secretEngine, policy, or their reserved API paths.
	// Field names must match ^[A-Za-z_][A-Za-z0-9_-]*$. A field must not occur
	// in both data and dataFrom.
	// All requests together can reference at most two distinct Secrets. Mappings,
	// including their request and field names, are immutable after cluster creation.
	// Self-init must be enabled at creation and until initialization completes.
	// Missing inputs block startup before OpenBao initializes, including when
	// when is false or allowFailure is true. Changes after initialization do not
	// update OpenBao. The operator does not read or copy the Secret values.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	DataFrom map[string]SelfInitDataSource `json:"dataFrom,omitempty"`
	// AllowFailure allows this request to fail without blocking initialization.
	// Defaults to false.
	// +optional
	AllowFailure bool `json:"allowFailure,omitempty"`
}

// SelfInitDataSource supplies one string field of a raw self-init request.
type SelfInitDataSource struct {
	// SecretKeyRef selects a required Secret key in the OpenBaoCluster namespace.
	SecretKeyRef SelfInitSecretKeyReference `json:"secretKeyRef"`
}

// SelfInitSecretKeyReference identifies a bootstrap input without containing its value.
type SelfInitSecretKeyReference struct {
	// Name is the Secret name. The submitting identity must be authorized to get it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	Name string `json:"name"`
	// Key is the Secret data key. Its exact contents are used without trimming.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	Key string `json:"key"`
}

// RecoveryKeysConfig configures OpenBao recovery-key bootstrap surfaces.
//
// The Operator only creates recovery keys during first self-initialization. It
// does not distribute encrypted shares, collect decrypted shares, escrow share
// material, or run generate-root ceremonies.
type RecoveryKeysConfig struct {
	// Initial configures the first recovery-key generation request for a
	// self-initialized cluster using auto-unseal.
	// +optional
	Initial *InitialRecoveryKeysConfig `json:"initial,omitempty"`
}

// InitialRecoveryKeysConfig declares the first recovery-key set that OpenBao
// should create through the authenticated recovery-key rotation endpoint during
// self-initialization.
//
// The Operator always renders this request with backup=true so encrypted
// recovery shares can be retrieved through OpenBao's recovery backup endpoint
// after bootstrap. Decrypted recovery shares must stay outside Kubernetes and
// outside the Operator.
// +kubebuilder:validation:XValidation:rule="self.threshold <= self.shares",message="recoveryKeys.initial.threshold must be less than or equal to recoveryKeys.initial.shares"
// +kubebuilder:validation:XValidation:rule="size(self.recipients) == self.shares",message="recoveryKeys.initial.recipients must contain exactly recoveryKeys.initial.shares entries"
type InitialRecoveryKeysConfig struct {
	// Shares is the total number of recovery-key shares to create.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=255
	Shares int32 `json:"shares"`
	// Threshold is the number of recovery-key shares required for recovery
	// operations such as generate-root.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=255
	Threshold int32 `json:"threshold"`
	// Recipients lists the public OpenPGP recipients for encrypted recovery
	// shares. Each recipient is passed to OpenBao as one pgp_keys entry; use
	// fingerprints for custody mapping instead of relying on share numbering.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=255
	// +listType=map
	// +listMapKey=name
	Recipients []RecoveryKeyRecipient `json:"recipients"`
}

// RecoveryKeyRecipient describes one public OpenPGP recipient for an encrypted
// recovery-key share. The public key material is not secret, but it must be
// fingerprint-verified before production use.
type RecoveryKeyRecipient struct {
	// Name is a stable ceremony-local recipient identifier used only for review
	// and status/evidence mapping.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9][A-Za-z0-9_.-]*$`
	Name string `json:"name"`
	// Fingerprint is the expected OpenPGP public-key fingerprint for the
	// recipient. It is informational for the Operator and should be verified
	// out of band by the ceremony participants.
	// +kubebuilder:validation:Pattern=`^([0-9A-Fa-f]{40}|[0-9A-Fa-f]{64})$`
	// +optional
	Fingerprint string `json:"fingerprint,omitempty"`
	// PGPPublicKey is the base64-encoded binary OpenPGP public key material
	// expected by OpenBao's sys/rotate/recovery/init pgp_keys field.
	// +kubebuilder:validation:MinLength=1
	PGPPublicKey string `json:"pgpPublicKey"`
}

// SelfInitAuditDevice provides structured configuration for enabling audit devices
// via self-init requests. This replaces the need for raw JSON in the Data field.
// See: https://openbao.org/api-docs/system/audit/
// +kubebuilder:validation:XValidation:rule="self.type == 'file' || !has(self.fileOptions)",message="fileOptions is only supported when type is file"
// +kubebuilder:validation:XValidation:rule="self.type != 'file' || has(self.fileOptions)",message="fileOptions is required when type is file"
// +kubebuilder:validation:XValidation:rule="self.type == 'http' || !has(self.httpOptions)",message="httpOptions is only supported when type is http"
// +kubebuilder:validation:XValidation:rule="self.type != 'http' || has(self.httpOptions)",message="httpOptions is required when type is http"
// +kubebuilder:validation:XValidation:rule="self.type == 'syslog' || !has(self.syslogOptions)",message="syslogOptions is only supported when type is syslog"
// +kubebuilder:validation:XValidation:rule="self.type == 'socket' || !has(self.socketOptions)",message="socketOptions is only supported when type is socket"
type SelfInitAuditDevice struct {
	// Type is the type of audit device (e.g., "file", "syslog", "socket", "http").
	// +kubebuilder:validation:Enum=file;syslog;socket;http
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`
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
}

// SelfInitAuthMethod provides structured configuration for enabling auth methods
// via self-init requests. This replaces the need for raw JSON in the Data field.
// See: https://openbao.org/api-docs/system/auth/
type SelfInitAuthMethod struct {
	// Type is the type of auth method (e.g., "jwt", "kubernetes", "userpass", "ldap").
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`
	// Description is an optional description for the auth method.
	// +optional
	Description string `json:"description,omitempty"`
	// Config contains optional configuration for the auth method mount.
	// Common fields include: default_lease_ttl, max_lease_ttl, listing_visibility, etc.
	// +optional
	Config map[string]string `json:"config,omitempty"`
}

// SelfInitSecretEngine provides structured configuration for enabling secret engines
// via self-init requests. This replaces the need for raw JSON in the Data field.
// See: https://openbao.org/api-docs/system/mounts/
type SelfInitSecretEngine struct {
	// Type is the type of secret engine (e.g., "kv", "pki", "transit", "database").
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`
	// Description is an optional description for the secret engine.
	// +optional
	Description string `json:"description,omitempty"`
	// Options contains optional configuration specific to the secret engine type.
	// For KV engines, common options include: version ("1" or "2").
	// For other engines, options vary by type.
	// +optional
	Options map[string]string `json:"options,omitempty"`
}

// SelfInitPolicy provides structured configuration for creating/updating policies
// via self-init requests. This replaces the need for raw JSON in the Data field.
// See: https://openbao.org/api-docs/system/policies-acl/
type SelfInitPolicy struct {
	// Policy is the HCL or JSON policy content.
	// This is the actual policy rules that will be applied.
	// +kubebuilder:validation:MinLength=1
	Policy string `json:"policy"`
}

// SelfInitOIDCConfig configures OIDC identity for the cluster.
// +kubebuilder:validation:XValidation:rule="!has(self.additionalSubjects) || self.enabled",message="spec.selfInit.oidc.additionalSubjects requires spec.selfInit.oidc.enabled=true"
type SelfInitOIDCConfig struct {
	// Enabled triggers the bootstrap logic.
	Enabled bool `json:"enabled"`

	// Audience, if set, must match the operator installation audience used for
	// projected OpenBao auth tokens.
	// This field does not create a per-cluster TokenRequest audience override.
	// +optional
	Audience string `json:"audience,omitempty"`

	// Issuer overrides the auto-discovered K8s issuer URL.
	// Critical for scenarios where OpenBao sees a different K8s URL than the Operator.
	// +optional
	Issuer string `json:"issuer,omitempty"`

	// AdditionalSubjects adds exact Kubernetes ServiceAccount subjects to the
	// generated Operator JWT roles. Use these bindings when a snapshot must
	// remain operable after restore to a target with different ServiceAccount
	// subjects. Configure the source cluster before self-initialization so the
	// bindings are present in each snapshot.
	//
	// These bindings do not configure JWT issuer or signature verification for
	// another Kubernetes control plane. The jwt-operator auth method must also
	// trust the target's projected ServiceAccount tokens.
	// +optional
	AdditionalSubjects *SelfInitOIDCAdditionalSubjects `json:"additionalSubjects,omitempty"`
}

// KubernetesServiceAccountSubject is the exact subject claim in a projected
// Kubernetes ServiceAccount token.
// +kubebuilder:validation:MaxLength=340
// +kubebuilder:validation:Pattern=`^system:serviceaccount:[a-z0-9]([-a-z0-9]*[a-z0-9])?:[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
type KubernetesServiceAccountSubject string

// SelfInitOIDCAdditionalSubjects adds recovery-target identities to the
// generated Operator JWT roles without combining their policies.
type SelfInitOIDCAdditionalSubjects struct {
	// Operator lists additional controller ServiceAccount subjects.
	// +kubebuilder:validation:MaxItems=32
	// +listType=set
	// +optional
	Operator []KubernetesServiceAccountSubject `json:"operator,omitempty"`

	// Backup lists additional backup Job ServiceAccount subjects.
	// +kubebuilder:validation:MaxItems=32
	// +listType=set
	// +optional
	Backup []KubernetesServiceAccountSubject `json:"backup,omitempty"`

	// Restore lists additional restore Job ServiceAccount subjects.
	// +kubebuilder:validation:MaxItems=32
	// +listType=set
	// +optional
	Restore []KubernetesServiceAccountSubject `json:"restore,omitempty"`

	// Upgrade lists additional upgrade Job ServiceAccount subjects.
	// +kubebuilder:validation:MaxItems=32
	// +listType=set
	// +optional
	Upgrade []KubernetesServiceAccountSubject `json:"upgrade,omitempty"`
}
