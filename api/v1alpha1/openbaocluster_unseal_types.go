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

import corev1 "k8s.io/api/core/v1"

// TransitSealConfig configures the Transit seal type.
// See: https://openbao.org/docs/configuration/seal/transit/
type TransitSealConfig struct {
	// Address is the full HTTPS address to the OpenBao cluster providing the Transit seal.
	// +kubebuilder:validation:MinLength=1
	Address string `json:"address"`

	// Token is the OpenBao token to use for authentication.
	// Note: It is strongly recommended to use CredentialsSecretRef instead of setting this directly.
	// +optional
	Token string `json:"token,omitempty"`

	// KeyName is the transit key to use for encryption and decryption.
	// +kubebuilder:validation:MinLength=1
	KeyName string `json:"keyName"`

	// MountPath is the mount path to the transit secret engine.
	// +kubebuilder:validation:MinLength=1
	MountPath string `json:"mountPath"`

	// Namespace is the namespace path to the transit secret engine.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// DisableRenewal disables automatic token renewal.
	// Set to true if token lifecycle is managed externally (e.g., by OpenBao Agent).
	// +optional
	DisableRenewal *bool `json:"disableRenewal,omitempty"`

	// TLSCACert is the path to the CA certificate file for TLS communication.
	// +optional
	TLSCACert string `json:"tlsCACert,omitempty"`

	// TLSClientCert is the path to the client certificate for TLS communication.
	// +optional
	TLSClientCert string `json:"tlsClientCert,omitempty"`

	// TLSClientKey is the path to the private key for TLS communication.
	// +optional
	TLSClientKey string `json:"tlsClientKey,omitempty"`

	// TLSServerName is the SNI host name to use when connecting via TLS.
	// +optional
	TLSServerName string `json:"tlsServerName,omitempty"`

	// TLSSkipVerify disables verification of TLS certificates.
	// Using this option is highly discouraged and decreases security.
	// +optional
	TLSSkipVerify *bool `json:"tlsSkipVerify,omitempty"`
}

// AWSKMSSealConfig configures the AWS KMS seal type.
// See: https://openbao.org/docs/configuration/seal/awskms/
type AWSKMSSealConfig struct {
	// Region is the AWS region where the encryption key lives.
	// +kubebuilder:validation:MinLength=1
	Region string `json:"region"`

	// KMSKeyID is the AWS KMS key ID or ARN to use for encryption and decryption.
	// An alias in the format "alias/key-alias-name" may also be used.
	// +kubebuilder:validation:MinLength=1
	KMSKeyID string `json:"kmsKeyID"`

	// Endpoint is the KMS API endpoint to be used for AWS KMS requests.
	// Useful when connecting to KMS over a VPC Endpoint.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// AccessKey is the AWS access key ID to use.
	// Note: It is strongly recommended to use CredentialsSecretRef or Workload Identity (IRSA) instead.
	// +optional
	AccessKey string `json:"accessKey,omitempty"`

	// SecretKey is the AWS secret access key to use.
	// Note: It is strongly recommended to use CredentialsSecretRef or Workload Identity (IRSA) instead.
	// +optional
	SecretKey string `json:"secretKey,omitempty"`

	// SessionToken specifies the AWS session token.
	// +optional
	SessionToken string `json:"sessionToken,omitempty"`
}

// AzureKeyVaultSealConfig configures the Azure Key Vault seal type.
// See: https://openbao.org/docs/configuration/seal/azurekeyvault/
// +kubebuilder:validation:XValidation:rule="!has(self.workloadIdentity) || (has(self.tenantID) && size(self.tenantID) > 0)",message="spec.unseal.azureKeyVault.workloadIdentity requires tenantID"
// +kubebuilder:validation:XValidation:rule="!has(self.workloadIdentity) || ((!has(self.clientID) || size(self.clientID) == 0) && (!has(self.clientSecret) || size(self.clientSecret) == 0))",message="spec.unseal.azureKeyVault.workloadIdentity excludes clientID and clientSecret"
// +kubebuilder:validation:XValidation:rule="!has(self.workloadIdentity) || !has(self.authMethod) || self.authMethod == 'workload_identity'",message="spec.unseal.azureKeyVault.workloadIdentity requires authMethod workload_identity or unset"
type AzureKeyVaultSealConfig struct {
	// VaultName is the name of the Azure Key Vault.
	// +kubebuilder:validation:MinLength=1
	VaultName string `json:"vaultName"`

	// KeyName is the name of the key in the Azure Key Vault.
	// +kubebuilder:validation:MinLength=1
	KeyName string `json:"keyName"`

	// TenantID is the Azure tenant ID.
	// +optional
	TenantID string `json:"tenantID,omitempty"`

	// ClientID is the Azure client ID.
	// +optional
	ClientID string `json:"clientID,omitempty"`

	// ClientSecret is the Azure client secret.
	// Note: It is strongly recommended to use CredentialsSecretRef or Managed Service Identity instead.
	// +optional
	ClientSecret string `json:"clientSecret,omitempty"`

	// Resource is the Azure AD resource endpoint.
	// For Managed HSM, this should usually be "managedhsm.azure.net".
	// +optional
	Resource string `json:"resource,omitempty"`

	// Environment is the Azure environment (e.g., "AzurePublicCloud", "AzureUSGovernmentCloud").
	// +optional
	Environment string `json:"environment,omitempty"`

	// AuthMethod selects how the seal authenticates to Azure, rendered as the
	// seal's auth_method. When empty, OpenBao chooses: a client ID without a
	// client secret selects managed_identity, which uses the instance metadata
	// service. Set workload_identity for Azure Workload Identity, where the
	// workload identity webhook also injects AZURE_CLIENT_ID.
	// +kubebuilder:validation:Enum=workload_identity;managed_identity;client_secret;environment;default
	// +optional
	AuthMethod string `json:"authMethod,omitempty"`

	// WorkloadIdentity has the operator project an Azure workload identity token
	// into the OpenBao Pods, without the Azure Workload Identity webhook. Requires
	// tenantID. The token is exchanged with the Azure public cloud.
	// +optional
	WorkloadIdentity *AzureKeyVaultWorkloadIdentity `json:"workloadIdentity,omitempty"`
}

// AzureKeyVaultWorkloadIdentity configures the federated workload identity the
// OpenBao Pods authenticate to Azure Key Vault with.
type AzureKeyVaultWorkloadIdentity struct {
	// ClientID is the client ID of the identity holding the federated credential.
	// +kubebuilder:validation:MinLength=1
	ClientID string `json:"clientID"`
}

// GCPCloudKMSSealConfig configures the GCP Cloud KMS seal type.
// See: https://openbao.org/docs/configuration/seal/gcpckms/
type GCPCloudKMSSealConfig struct {
	// Project is the GCP project ID.
	// +kubebuilder:validation:MinLength=1
	Project string `json:"project"`

	// Region is the GCP region where the key ring lives.
	// +kubebuilder:validation:MinLength=1
	Region string `json:"region"`

	// KeyRing is the name of the GCP KMS key ring.
	// +kubebuilder:validation:MinLength=1
	KeyRing string `json:"keyRing"`

	// CryptoKey is the name of the GCP KMS crypto key.
	// +kubebuilder:validation:MinLength=1
	CryptoKey string `json:"cryptoKey"`

	// Credentials is the path to the GCP credentials JSON file.
	// Note: It is strongly recommended to use CredentialsSecretRef or Workload Identity instead.
	// +optional
	Credentials string `json:"credentials,omitempty"`
}

// KMIPSealConfig configures the KMIP seal type.
// See: https://openbao.org/docs/configuration/seal/kmip/
type KMIPSealConfig struct {
	// Endpoint is the KMIP server endpoint.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// KMSKeyID is the unique identifier of the KMIP key to use.
	// +kubebuilder:validation:MinLength=1
	KMSKeyID string `json:"kmsKeyID"`

	// ClientCert is the path to the client certificate used for KMIP communication.
	// +kubebuilder:validation:MinLength=1
	ClientCert string `json:"clientCert"`

	// ClientKey is the path to the private key used for KMIP communication.
	// +kubebuilder:validation:MinLength=1
	ClientKey string `json:"clientKey"`

	// CACert is the path to the CA certificate for KMIP communication.
	// +optional
	CACert string `json:"caCert,omitempty"`

	// ServerName is the TLS server name to use when connecting to the KMIP endpoint.
	// +optional
	ServerName string `json:"serverName,omitempty"`

	// Timeout is the timeout in seconds for KMIP requests.
	// +kubebuilder:validation:Minimum=1
	// +optional
	Timeout *int32 `json:"timeout,omitempty"`

	// EncryptAlg is the encryption algorithm used for KMIP requests.
	// +kubebuilder:validation:Enum=AES_GCM;RSA_OAEP_SHA256;RSA_OAEP_SHA384;RSA_OAEP_SHA512
	// +optional
	EncryptAlg string `json:"encryptAlg,omitempty"`

	// TLS12Ciphers configures the TLS 1.2 cipher suites to use when connecting
	// to the KMIP endpoint.
	// +optional
	TLS12Ciphers string `json:"tls12Ciphers,omitempty"`

	// Disabled disables this seal configuration, for example during seal migration.
	// +optional
	Disabled *bool `json:"disabled,omitempty"`
}

// OCIKMSSealConfig configures the OCI KMS seal type.
// See: https://openbao.org/docs/configuration/seal/ocikms/
type OCIKMSSealConfig struct {
	// KeyID is the OCID of the master encryption key.
	// +kubebuilder:validation:MinLength=1
	KeyID string `json:"keyID"`

	// CryptoEndpoint is the OCI KMS crypto endpoint.
	// +kubebuilder:validation:MinLength=1
	CryptoEndpoint string `json:"cryptoEndpoint"`

	// ManagementEndpoint is the OCI KMS management endpoint.
	// +kubebuilder:validation:MinLength=1
	ManagementEndpoint string `json:"managementEndpoint"`

	// AuthTypeAPIKey enables OCI API key authentication through an OCI SDK config file.
	// When false or omitted, OpenBao uses the default OCI principal flow for the runtime
	// environment, such as instance principal.
	// +optional
	AuthTypeAPIKey *bool `json:"authTypeAPIKey,omitempty"`

	// Disabled disables this seal configuration, for example during seal migration.
	// +optional
	Disabled *bool `json:"disabled,omitempty"`
}

// PKCS11RuntimeConfig configures local runtime wiring needed by PKCS#11 vendor
// libraries. It is intentionally scoped to environment variables and library
// lookup paths so HSM integrations do not require custom wrapper scripts.
type PKCS11RuntimeConfig struct {
	// LibraryPath sets LD_LIBRARY_PATH for the OpenBao process. Use this when
	// the configured PKCS#11 module depends on sibling vendor libraries that
	// are not in the image's default dynamic linker search path.
	// +optional
	LibraryPath string `json:"libraryPath,omitempty"`

	// Env exposes literal environment variables from keys in
	// spec.unseal.credentialsSecretRef. Use this for vendor runtime settings
	// such as HSM endpoints or authentication key references.
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Env []PKCS11RuntimeEnvVar `json:"env,omitempty"`

	// FileEnv exposes environment variables whose values are paths to files
	// mounted from keys in spec.unseal.credentialsSecretRef. Use this for vendor
	// settings that expect a config file path, for example SOFTHSM2_CONF or
	// vendor-specific PKCS#11 client configuration variables.
	// +kubebuilder:validation:MaxItems=16
	// +optional
	FileEnv []PKCS11RuntimeFileEnvVar `json:"fileEnv,omitempty"`
}

// PKCS11RuntimeEnvVar maps a PKCS#11 runtime environment variable to a key in
// spec.unseal.credentialsSecretRef.
type PKCS11RuntimeEnvVar struct {
	// Name is the environment variable name to expose to the OpenBao process.
	// Names owned by OpenBao's PKCS#11 seal configuration, such as BAO_HSM_PIN,
	// are managed by the operator and must not be configured here.
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`

	// SecretKey is the key in spec.unseal.credentialsSecretRef to source as the
	// environment variable value.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[-._A-Za-z0-9]+$`
	SecretKey string `json:"secretKey"`
}

// PKCS11RuntimeFileEnvVar maps a PKCS#11 runtime environment variable to the
// mounted file path for a key in spec.unseal.credentialsSecretRef.
type PKCS11RuntimeFileEnvVar struct {
	// Name is the environment variable name to expose to the OpenBao process.
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`

	// SecretKey is the key in spec.unseal.credentialsSecretRef whose mounted
	// file path should become the environment variable value.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[-._A-Za-z0-9]+$`
	SecretKey string `json:"secretKey"`
}

// PKCS11SealConfig configures the PKCS#11 seal type.
// See: https://openbao.org/docs/configuration/seal/pkcs11/
// +kubebuilder:validation:XValidation:rule="(has(self.slot) && size(self.slot) > 0) || (has(self.tokenLabel) && size(self.tokenLabel) > 0)",message="spec.unseal.pkcs11.slot or spec.unseal.pkcs11.tokenLabel is required"
// +kubebuilder:validation:XValidation:rule="!(has(self.slot) && size(self.slot) > 0 && has(self.tokenLabel) && size(self.tokenLabel) > 0)",message="spec.unseal.pkcs11.slot and spec.unseal.pkcs11.tokenLabel are mutually exclusive"
type PKCS11SealConfig struct {
	// Lib is the path to the PKCS#11 library provided by the HSM vendor.
	// +kubebuilder:validation:MinLength=1
	Lib string `json:"lib"`

	// Slot is the slot number where the HSM token is located.
	// +optional
	Slot string `json:"slot,omitempty"`

	// TokenLabel is the token label of the HSM slot to use instead of Slot.
	// +optional
	TokenLabel string `json:"tokenLabel,omitempty"`

	// PIN is the PIN for accessing the HSM token.
	// Note: It is strongly recommended to use CredentialsSecretRef instead of setting this directly.
	// +optional
	PIN string `json:"pin,omitempty"`

	// KeyLabel is the label for the encryption key used by OpenBao.
	// +kubebuilder:validation:MinLength=1
	KeyLabel string `json:"keyLabel"`

	// KeyID is the PKCS#11 key identifier to use instead of KeyLabel.
	// +optional
	KeyID string `json:"keyID,omitempty"`

	// Mechanism overrides the PKCS#11 wrapping or encryption mechanism.
	// +optional
	Mechanism string `json:"mechanism,omitempty"`

	// DisableSoftwareEncryption disables the software encryption fallback.
	// +optional
	DisableSoftwareEncryption *bool `json:"disableSoftwareEncryption,omitempty"`

	// Disabled disables this seal configuration, for example during seal migration.
	// +optional
	Disabled *bool `json:"disabled,omitempty"`

	// RSAOAEPHash specifies the hash algorithm to use for RSA with OAEP padding.
	// Valid values: sha1, sha224, sha256, sha384, sha512.
	// +kubebuilder:validation:Enum=sha1;sha224;sha256;sha384;sha512
	// +optional
	RSAOAEPHash string `json:"rsaOAEPHash,omitempty"`

	// Runtime configures local PKCS#11 vendor runtime wiring such as library
	// lookup paths and environment variables sourced from credentialsSecretRef.
	// +optional
	Runtime *PKCS11RuntimeConfig `json:"runtime,omitempty"`
}

// StaticSealConfig configures the static seal type.
// This is the default seal type managed by the operator.
// See: https://openbao.org/docs/configuration/seal/static-key/
type StaticSealConfig struct {
	// CurrentKey is the path to the static unseal key file.
	// Defaults to "file:///etc/bao/unseal/key" (operator-managed).
	// +optional
	CurrentKey string `json:"currentKey,omitempty"`

	// CurrentKeyID is the identifier for the current unseal key.
	// Defaults to "operator-generated-v1" (operator-managed).
	// +optional
	CurrentKeyID string `json:"currentKeyID,omitempty"`
}

// KMSPluginSealConfig configures a plugin-backed KMS seal.
// The referenced plugin must be declared in spec.plugins with type "kms".
type KMSPluginSealConfig struct {
	// PluginName is the name of the plugin registered through a matching
	// plugin "kms" stanza. OpenBao uses this value as the seal stanza label.
	// +kubebuilder:validation:MinLength=1
	PluginName string `json:"pluginName"`

	// Config contains plugin-specific seal configuration rendered as string
	// attributes inside seal "<pluginName>". Keys must be valid HCL identifiers.
	// Values are stored in the OpenBaoCluster resource; use file paths to
	// credentialsSecretRef-mounted files for sensitive material instead of inline
	// secrets.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	Config map[string]string `json:"config,omitempty"`
}

// UnsealConfig defines the auto-unseal configuration for an OpenBaoCluster.
// If omitted, defaults to "static" mode managed by the operator.
// +kubebuilder:validation:XValidation:rule="self.type == 'static' || (self.type == 'transit' && has(self.transit)) || (self.type == 'awskms' && has(self.awskms)) || (self.type == 'azurekeyvault' && has(self.azureKeyVault)) || (self.type == 'gcpckms' && has(self.gcpCloudKMS)) || (self.type == 'kmip' && has(self.kmip)) || (self.type == 'kms' && has(self.kms)) || (self.type == 'ocikms' && has(self.ocikms)) || (self.type == 'pkcs11' && has(self.pkcs11))",message="the selected spec.unseal.type requires its matching configuration block"
// +kubebuilder:validation:XValidation:rule="self.type == 'static' || !has(self.static)",message="spec.unseal.static is only supported when spec.unseal.type is static"
// +kubebuilder:validation:XValidation:rule="self.type == 'transit' || !has(self.transit)",message="spec.unseal.transit is only supported when spec.unseal.type is transit"
// +kubebuilder:validation:XValidation:rule="self.type == 'awskms' || !has(self.awskms)",message="spec.unseal.awskms is only supported when spec.unseal.type is awskms"
// +kubebuilder:validation:XValidation:rule="self.type == 'azurekeyvault' || !has(self.azureKeyVault)",message="spec.unseal.azureKeyVault is only supported when spec.unseal.type is azurekeyvault"
// +kubebuilder:validation:XValidation:rule="self.type == 'gcpckms' || !has(self.gcpCloudKMS)",message="spec.unseal.gcpCloudKMS is only supported when spec.unseal.type is gcpckms"
// +kubebuilder:validation:XValidation:rule="self.type == 'kmip' || !has(self.kmip)",message="spec.unseal.kmip is only supported when spec.unseal.type is kmip"
// +kubebuilder:validation:XValidation:rule="self.type == 'kms' || !has(self.kms)",message="spec.unseal.kms is only supported when spec.unseal.type is kms"
// +kubebuilder:validation:XValidation:rule="self.type == 'ocikms' || !has(self.ocikms)",message="spec.unseal.ocikms is only supported when spec.unseal.type is ocikms"
// +kubebuilder:validation:XValidation:rule="self.type == 'pkcs11' || !has(self.pkcs11)",message="spec.unseal.pkcs11 is only supported when spec.unseal.type is pkcs11"
type UnsealConfig struct {
	// Type specifies the seal type.
	// Defaults to "static".
	// +kubebuilder:validation:Enum=static;awskms;gcpckms;azurekeyvault;transit;kmip;kms;ocikms;pkcs11
	// +kubebuilder:default=static
	Type string `json:"type,omitempty"`

	// Static configures the static seal type.
	// Optional when Type is "static" (operator provides defaults if omitted).
	// +optional
	Static *StaticSealConfig `json:"static,omitempty"`

	// Transit configures the Transit seal type.
	// Required when Type is "transit".
	// +optional
	Transit *TransitSealConfig `json:"transit,omitempty"`

	// AWSKMS configures the AWS KMS seal type.
	// Required when Type is "awskms".
	// +optional
	AWSKMS *AWSKMSSealConfig `json:"awskms,omitempty"`

	// AzureKeyVault configures the Azure Key Vault seal type.
	// Required when Type is "azurekeyvault".
	// +optional
	AzureKeyVault *AzureKeyVaultSealConfig `json:"azureKeyVault,omitempty"`

	// GCPCloudKMS configures the GCP Cloud KMS seal type.
	// Required when Type is "gcpckms".
	// +optional
	GCPCloudKMS *GCPCloudKMSSealConfig `json:"gcpCloudKMS,omitempty"`

	// KMIP configures the KMIP seal type.
	// Required when Type is "kmip".
	// +optional
	KMIP *KMIPSealConfig `json:"kmip,omitempty"`

	// KMS configures a plugin-backed KMS seal.
	// Required when Type is "kms".
	// +optional
	KMS *KMSPluginSealConfig `json:"kms,omitempty"`

	// OCIKMS configures the OCI KMS seal type.
	// Required when Type is "ocikms".
	// +optional
	OCIKMS *OCIKMSSealConfig `json:"ocikms,omitempty"`

	// PKCS11 configures the PKCS#11 seal type.
	// Required when Type is "pkcs11".
	// +optional
	PKCS11 *PKCS11SealConfig `json:"pkcs11,omitempty"`

	// CredentialsSecretRef references a Secret containing provider credentials
	// (for example AWS access keys, GCP credentials.json, Azure client-secret keys,
	// OCI SDK config for authTypeAPIKey mode, or plugin-backed KMS runtime files).
	// If using Workload Identity (IRSA, GKE WI, Azure MSI), this can be omitted.
	// For static unseal, the Secret supplies the original key instead of an
	// operator-generated key. Its files are mounted at /etc/bao/unseal; the
	// default currentKey reads its "key" entry. The operator does not create,
	// modify, or take ownership of this Secret. A cluster that already owns an
	// operator-generated unseal key keeps that key and ignores this reference.
	// The Secret must exist in the same namespace as the OpenBaoCluster.
	// Cross-namespace references are not allowed for security reasons.
	// +optional
	CredentialsSecretRef *corev1.LocalObjectReference `json:"credentialsSecretRef,omitempty"`
}
