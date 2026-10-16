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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeletionPolicy defines what happens to underlying resources when the CR is deleted.
// +kubebuilder:validation:Enum=Retain;DeletePVCs;DeleteAll
type DeletionPolicy string

const (
	// OpenBaoClusterFinalizer is the finalizer used to ensure cleanup logic
	// runs before an OpenBaoCluster is fully deleted.
	OpenBaoClusterFinalizer = "openbao.org/openbaocluster-finalizer"

	// DeletionPolicyRetain removes operator-managed compute and keeps PVCs and external backups.
	DeletionPolicyRetain DeletionPolicy = "Retain"
	// DeletionPolicyDeletePVCs removes operator-managed compute and PVCs, but retains external backups.
	DeletionPolicyDeletePVCs DeletionPolicy = "DeletePVCs"
	// DeletionPolicyDeleteAll removes operator-managed compute and PVCs, but retains external object-store backups.
	DeletionPolicyDeleteAll DeletionPolicy = "DeleteAll"
)

// ClusterPhase is a high-level summary of cluster state.
// +kubebuilder:validation:Enum=Initializing;Running;Upgrading;BackingUp;Failed
type ClusterPhase string

const (
	ClusterPhaseInitializing ClusterPhase = "Initializing"
	ClusterPhaseRunning      ClusterPhase = "Running"
	ClusterPhaseUpgrading    ClusterPhase = "Upgrading"
	ClusterPhaseBackingUp    ClusterPhase = "BackingUp"
	ClusterPhaseFailed       ClusterPhase = "Failed"
)

// ConditionType identifies a specific aspect of cluster health or lifecycle.
// This type is kept as a strong string alias to avoid stringly-typed code.
type ConditionType string

const (
	// ConditionAvailable indicates whether the cluster is generally available.
	ConditionAvailable ConditionType = "Available"
	// ConditionTLSReady indicates whether TLS assets have been successfully provisioned.
	ConditionTLSReady ConditionType = "TLSReady"
	// ConditionACMEIntegrationReady indicates whether operator-known ACME integration
	// prerequisites are satisfied, such as Gateway passthrough, private ACME trust,
	// and self-reachability checks for supported topologies.
	ConditionACMEIntegrationReady ConditionType = "ACMEIntegrationReady"
	// ConditionACMECacheReady indicates whether the shared ACME cache PVC is ready for use
	// when the configured topology requires or uses a shared ACME cache.
	ConditionACMECacheReady ConditionType = "ACMECacheReady"
	// ConditionAuditFileStorageReady indicates whether the shared audit file storage PVC
	// is ready for file audit devices and mounted by the workload StatefulSets.
	ConditionAuditFileStorageReady ConditionType = "AuditFileStorageReady"
	// ConditionGatewayIntegrationReady indicates whether the operator can verify
	// the referenced Gateway and GatewayClass integration contract for the chosen
	// Gateway API mode, including attachment of the operator-managed Route.
	ConditionGatewayIntegrationReady ConditionType = "GatewayIntegrationReady"
	// ConditionIngressIntegrationReady indicates whether the operator can verify
	// the managed Ingress integration contract for the chosen ingress mode.
	ConditionIngressIntegrationReady ConditionType = "IngressIntegrationReady"
	// ConditionAPIServerNetworkReady indicates whether the operator can validate
	// Kubernetes API egress configuration, including an opt-in Cilium entity policy.
	// It does not probe connectivity. Unknown identifies a VIP-only contract, a
	// pending managed policy, or administrator-managed restore isolation.
	ConditionAPIServerNetworkReady ConditionType = "APIServerNetworkReady"
	// ConditionBackupConfigurationReady indicates whether the operator can verify
	// the backup Job's operator-known prerequisites such as auth references,
	// storage credential Secret references, and hardened-profile egress rules.
	ConditionBackupConfigurationReady ConditionType = "BackupConfigurationReady"

	// ConditionPolicyReconciliationReady indicates whether the requested built-in
	// policies have been verified and the last reconciliation attempt succeeded.
	ConditionPolicyReconciliationReady ConditionType = "PolicyReconciliationReady"
	// ConditionCloudUnsealIdentityReady indicates whether the operator can
	// determine and validate the cloud KMS unseal authentication path for the
	// main OpenBao Pods when using AWS KMS, GCP Cloud KMS, Azure Key Vault, or OCI KMS.
	ConditionCloudUnsealIdentityReady ConditionType = "CloudUnsealIdentityReady"
	// ConditionProductionReady indicates whether the cluster configuration is considered
	// production-ready by the operator (security posture, unseal, bootstrap flow).
	ConditionProductionReady ConditionType = "ProductionReady"
	// ConditionUserAccessBootstrap indicates whether the operator could recognize
	// a likely user-facing authentication bootstrap path in self-init requests.
	// This is a best-effort heuristic and does not prove that the configured auth
	// methods, roles, or identities are usable.
	ConditionUserAccessBootstrap ConditionType = "UserAccessBootstrap"
	// ConditionUpgrading indicates whether an upgrade is currently in progress.
	ConditionUpgrading ConditionType = "Upgrading"
	// ConditionBackingUp indicates whether a backup is currently in progress.
	ConditionBackingUp ConditionType = "BackingUp"
	// ConditionDegraded indicates the operator has detected a problem requiring attention.
	ConditionDegraded ConditionType = "Degraded"
	// ConditionEtcdEncryptionWarning indicates that etcd encryption may not be enabled,
	// which could expose sensitive data stored in Kubernetes Secrets.
	ConditionEtcdEncryptionWarning ConditionType = "EtcdEncryptionWarning"
	// ConditionSecurityRisk indicates that the cluster is using a relaxed security
	// posture (Development profile) which may not be suitable for production.
	ConditionSecurityRisk ConditionType = "SecurityRisk"
	// ConditionOpenBaoInitialized reflects OpenBao's own initialization state as
	// observed via Kubernetes service registration labels on Pods.
	ConditionOpenBaoInitialized ConditionType = "OpenBaoInitialized"
	// ConditionOpenBaoSealed reflects OpenBao's seal state as observed via
	// Kubernetes service registration labels on Pods.
	ConditionOpenBaoSealed ConditionType = "OpenBaoSealed"
	// ConditionOpenBaoLeader reflects whether a leader could be identified via
	// Kubernetes service registration labels on Pods.
	ConditionOpenBaoLeader ConditionType = "OpenBaoLeader"
	// ConditionNodeSecurityCapabilityMismatch indicates the cluster is configured
	// with workload hardening options that are not supported by the underlying
	// node/Kubernetes environment (for example, AppArmor not available).
	ConditionNodeSecurityCapabilityMismatch ConditionType = "NodeSecurityCapabilityMismatch"
	// ConditionStorageConfigured indicates persistent storage has either been
	// explicitly configured or consistently resolved from observed PVCs.
	ConditionStorageConfigured ConditionType = "StorageConfigured"
	// ConditionReadReplicasReady indicates whether the read-replica pool has the
	// desired number of Ready Pods.
	ConditionReadReplicasReady ConditionType = "ReadReplicasReady"
	// ConditionReadServingAvailable indicates whether the read-replica pool is
	// currently observed in a state that should serve reads for the validated
	// OpenBao version.
	ConditionReadServingAvailable ConditionType = "ReadServingAvailable"
	// ConditionRaftMembershipReady indicates whether observed voter and
	// non-voter membership matches the operator's declared topology.
	ConditionRaftMembershipReady ConditionType = "RaftMembershipReady"
	// ConditionReadReplicasAutopilotHealthy indicates whether the read-replica
	// pool is healthy according to the Raft Autopilot state endpoint.
	ConditionReadReplicasAutopilotHealthy ConditionType = "ReadReplicasAutopilotHealthy"
	// ConditionReadReplicaStorageConfigured indicates whether the read-replica
	// pool storage contract has been explicitly configured or consistently
	// resolved from observed PVCs.
	ConditionReadReplicaStorageConfigured ConditionType = "ReadReplicaStorageConfigured"
)

// TLSMode controls who manages the certificate lifecycle.
// +kubebuilder:validation:Enum=OperatorManaged;External;ACME
type TLSMode string

const (
	// TLSModeOperatorManaged: The operator acts as the CA, generating keys and rotating certs (Current Behavior).
	TLSModeOperatorManaged TLSMode = "OperatorManaged"
	// TLSModeExternal: The operator assumes Secrets are managed by an external entity (cert-manager, user, or CSI driver).
	// The operator will mount them but NOT modify/rotate them.
	TLSModeExternal TLSMode = "External"
	// TLSModeACME: OpenBao uses its native ACME client to fetch certificates.
	// No Secrets are mounted. No sidecar is injected. Best for Zero Trust.
	TLSModeACME TLSMode = "ACME"
)

// Profile defines the security posture for an OpenBaoCluster.
// +kubebuilder:validation:Enum=Hardened;Development
type Profile string

const (
	// ProfileHardened enforces strict security requirements and rejects unsafe escape hatches.
	ProfileHardened Profile = "Hardened"
	// ProfileDevelopment allows relaxed security for development/testing.
	ProfileDevelopment Profile = "Development"
)

// OpenBaoClusterSpec defines the desired state of an OpenBaoCluster.
// The Operator owns certain protected OpenBao configuration stanzas (for example,
// listener "tcp", storage "raft", and seal "static" when using default unseal).
// Users must not override these via spec.configuration.
// +kubebuilder:validation:XValidation:rule="self.tls.mode != 'OperatorManaged' || (has(self.tls.rotationPeriod) && size(self.tls.rotationPeriod) > 0)",message="spec.tls.rotationPeriod is required when spec.tls.mode is OperatorManaged"
// +kubebuilder:validation:XValidation:rule="self.tls.mode == 'ACME' || !has(self.tls.acme) || !has(self.tls.acme.sharedCache)",message="spec.tls.acme.sharedCache is only supported when spec.tls.mode is ACME"
// +kubebuilder:validation:XValidation:rule="self.tls.mode != 'ACME' || ((self.replicas <= 1) && (!has(self.upgrade) || self.upgrade.strategy != 'BlueGreen')) || (has(self.tls.acme) && has(self.tls.acme.sharedCache))",message="HA ACME clusters require spec.tls.acme.sharedCache when more than one Pod can serve the same hostname"
// +kubebuilder:validation:XValidation:rule="!has(self.unseal) || self.unseal.type != 'ocikms' || !has(self.unseal.credentialsSecretRef) || (has(self.unseal.ocikms) && has(self.unseal.ocikms.authTypeAPIKey) && self.unseal.ocikms.authTypeAPIKey == true)",message="spec.unseal.credentialsSecretRef for ocikms requires spec.unseal.ocikms.authTypeAPIKey=true"
// +kubebuilder:validation:XValidation:rule="!has(self.recoveryKeys) || !has(self.recoveryKeys.initial) || (has(self.selfInit) && self.selfInit.enabled)",message="spec.recoveryKeys.initial requires spec.selfInit.enabled=true"
// +kubebuilder:validation:XValidation:rule="!has(self.recoveryKeys) || !has(self.recoveryKeys.initial) || (has(self.unseal) && self.unseal.type != 'static')",message="spec.recoveryKeys.initial requires a non-static spec.unseal.type"
type OpenBaoClusterSpec struct {
	// Version is the semantic OpenBao version, used for upgrade orchestration.
	// The Operator uses static auto-unseal, which requires OpenBao v2.4.0 or later.
	// Versions below 2.4.0 do not support the static seal feature and will fail to start.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`
	Version string `json:"version"`
	// Image is the container image to run; defaults may be derived from Version.
	// +optional
	Image string `json:"image,omitempty"`

	// ServiceAccount configures the Kubernetes ServiceAccount used by the OpenBao Pods.
	// +optional
	ServiceAccount *ServiceAccountConfig `json:"serviceAccount,omitempty"`
	// PodMetadata configures additional labels and annotations for the OpenBao Pod template.
	// This is useful for platform integrations that select Pods via metadata, such as
	// Azure Workload Identity. Operator-managed Pod metadata takes precedence.
	// +optional
	PodMetadata *PodMetadataConfig `json:"podMetadata,omitempty"`

	// ImagePullSecrets is a list of references to secrets in the same namespace
	// to use for pulling any images used by this Cluster (server, init, sidecars).
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Observability configures telemetry and metrics integration.
	// +optional
	Observability *ObservabilityConfig `json:"observability,omitempty"`
	// Replicas is the desired number of quorum-carrying voter Pods.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	Replicas int32 `json:"replicas"`
	// Resources defines resource requests and limits for voter OpenBao containers.
	// Read replicas use spec.readReplicas.template.resources instead.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// ReadReplicas configures the steady-state non-voter read-replica pool.
	// +optional
	ReadReplicas *ReadReplicaConfig `json:"readReplicas,omitempty"`
	// Paused, when true, pauses reconciliation for this OpenBaoCluster (except delete and finalizers).
	// +optional
	Paused bool `json:"paused,omitempty"`
	// Maintenance configures supported maintenance workflows.
	// +optional
	Maintenance *MaintenanceConfig `json:"maintenance,omitempty"`
	// Runtime configures explicit runtime control requests for the OpenBao workload.
	// +optional
	Runtime *RuntimeConfig `json:"runtime,omitempty"`
	// BreakGlassAck is an explicit acknowledgment token used to exit Break Glass / Safe Mode.
	//
	// When the operator enters break glass mode, it writes a nonce to status.breakGlass.nonce.
	// To acknowledge and allow the operator to resume quorum-risk automation, set this field
	// to match that nonce.
	//
	// Example:
	//   kubectl -n <ns> patch openbaocluster <name> --type merge -p '{"spec":{"breakGlassAck":"<nonce>"}}'
	//
	// +optional
	BreakGlassAck string `json:"breakGlassAck,omitempty"`
	// TLS configures TLS for the cluster.
	TLS TLSConfig `json:"tls"`
	// Storage configures persistent storage for the cluster.
	Storage StorageConfig `json:"storage"`
	// Service configures the primary Service used to expose OpenBao inside or outside the cluster.
	// +optional
	Service *ServiceConfig `json:"service,omitempty"`
	// Ingress configures optional HTTP(S) ingress in front of the OpenBao Service.
	// +optional
	Ingress *IngressConfig `json:"ingress,omitempty"`
	// Configuration defines the server configuration.
	// +optional
	Configuration *OpenBaoConfiguration `json:"configuration,omitempty"`
	// Backup configures scheduled backups for the cluster.
	// +optional
	Backup *BackupSchedule `json:"backup,omitempty"`
	// Restore configures optional restore authentication bootstrap for the cluster.
	// +optional
	Restore *RestoreConfig `json:"restore,omitempty"`
	// DeletionPolicy controls what happens to underlying resources when the CR is deleted.
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
	// ReconcilePolicies restores the built-in operational policies after initialization.
	// Administrator-managed JWT authentication and exact-content approval are required.
	// OpenBao must independently approve their exact contents through the
	// openbao-operator-policy-approval policy. Bootstrap installs the initial approval;
	// existing clusters and later permission changes require administrator approval.
	// Auth methods, roles, and the approval policy are not reconciled.
	// +optional
	ReconcilePolicies bool `json:"reconcilePolicies,omitempty"`
	// ControllerJWTMode selects the credential used by the controller for OpenBao.
	// Shared (also the behavior when omitted) preserves the installation-wide
	// projected JWT. Target requests a short-lived JWT whose sole audience is
	// urn:openbao:controller:<metadata.uid>. Prepare the OpenBao controller role
	// before switching an initialized cluster to Target. Target never falls back
	// to the shared JWT. Target requires self-init and self-init OIDC to be enabled.
	// Executor Job authentication is unchanged.
	// Enforced admission prevents removal or downgrade after selecting Target.
	// +optional
	ControllerJWTMode ControllerJWTMode `json:"controllerJWTMode,omitempty"`

	// SelfInit configures OpenBao's native self-initialization feature.
	// When enabled, OpenBao initializes itself on first start using the configured
	// requests, and the root token is automatically revoked.
	// See: https://openbao.org/docs/configuration/self-init/
	// +optional
	SelfInit *SelfInitConfig `json:"selfInit,omitempty"`
	// RecoveryKeys configures Operator-assisted recovery-key bootstrap surfaces.
	// The Operator creates recovery keys only during initial self-initialization;
	// recovery share custody and proof ceremonies remain user-owned processes.
	// +optional
	RecoveryKeys *RecoveryKeysConfig `json:"recoveryKeys,omitempty"`
	// Gateway configures Kubernetes Gateway API access (alternative to Ingress).
	// When enabled, the Operator creates an HTTPRoute that routes traffic through
	// a user-managed Gateway resource.
	// +optional
	Gateway *GatewayConfig `json:"gateway,omitempty"`
	// Network configures network-related settings for the cluster.
	// +optional
	Network *NetworkConfig `json:"network,omitempty"`
	// InitContainer configures the init container used to render OpenBao configuration.
	// The init container renders the final config.hcl from a template using environment
	// variables such as HOSTNAME and POD_IP.
	// +optional
	InitContainer *InitContainerConfig `json:"initContainer,omitempty"`
	// Audit configures declarative audit devices for the OpenBao cluster.
	// See: https://openbao.org/docs/configuration/audit/
	// +optional
	// +listType=map
	// +listMapKey=path
	Audit []AuditDevice `json:"audit,omitempty"`
	// AuditFileStorage configures a shared filesystem integration point for file audit devices.
	// When configured, file audit device paths must be under auditFileStorage.mountPath.
	// +optional
	AuditFileStorage *AuditFileStorageConfig `json:"auditFileStorage,omitempty"`
	// Plugins configures declarative plugins for the OpenBao cluster.
	// See: https://openbao.org/docs/configuration/plugins/
	// +optional
	Plugins []Plugin `json:"plugins,omitempty"`
	// Telemetry configures telemetry reporting for the OpenBao cluster.
	// See: https://openbao.org/docs/configuration/telemetry/
	// +optional
	Telemetry *TelemetryConfig `json:"telemetry,omitempty"`
	// Upgrade configures upgrade operations.
	//
	// Built-in upgrade executor Jobs authenticate with JWT auth using the
	// upgrade ServiceAccount (<cluster-name>-upgrade-serviceaccount). If
	// spec.selfInit.oidc.enabled is true during initial SelfInit bootstrap and
	// spec.upgrade.jwtAuthRole is empty, the operator creates the default
	// "openbao-operator-upgrade" role. Already-initialized clusters must keep
	// that role or configure spec.upgrade.jwtAuthRole explicitly.
	//
	// Pre-upgrade snapshots use spec.backup configuration and backup
	// authentication rather than spec.upgrade credentials.
	// +optional
	Upgrade *UpgradeConfig `json:"upgrade,omitempty"`
	// Unseal defines the auto-unseal configuration.
	// If omitted, defaults to "static" mode managed by the operator.
	// +optional
	Unseal *UnsealConfig `json:"unseal,omitempty"`
	// ImageVerification configures supply chain security checks.
	// +optional
	ImageVerification *ImageVerificationConfig `json:"imageVerification,omitempty"`
	// OperatorImageVerification configures supply chain security checks for operator-managed helper images
	// (init container and backup/upgrade/restore executors) and custom BlueGreen validation-hook images.
	// Helper images are typically signed by the operator project (e.g., kubebao/openbao-operator)
	// rather than the OpenBao upstream project.
	// If omitted, helper image verification does not fall back to ImageVerification.
	// In Development, omitted means disabled. In Hardened, omitted means enabled.
	// +optional
	OperatorImageVerification *ImageVerificationConfig `json:"operatorImageVerification,omitempty"`
	// WorkloadHardening configures opt-in workload hardening features.
	// +optional
	WorkloadHardening *WorkloadHardeningConfig `json:"workloadHardening,omitempty"`
	// SecurityContext allows specifying the PodSecurityContext for the OpenBao Pods.
	// If set, these values override the default security context generated by the operator.
	// This is useful for OpenShift (SCC) compatibility or custom security requirements.
	// +optional
	SecurityContext *corev1.PodSecurityContext `json:"securityContext,omitempty"`
	// Profile defines the security posture for this cluster.
	// When set to "Hardened", the operator enforces strict security requirements:
	// - TLS must use External or ACME trust, with no TLS disablement or skip-verify paths
	// - Unseal must use external KMS (no static unseal)
	// - SelfInit must be enabled (no root token)
	// - Network additions must be explicit and least-privilege
	// - Backup/restore storage identity must be explicit
	// - Dangerous runtime flags and backend HTTP are rejected
	// When set to "Development", relaxed security is allowed but a security warning
	// condition is set.
	// +kubebuilder:validation:Enum=Hardened;Development
	Profile Profile `json:"profile"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=openbaoclusters,scope=Namespaced,shortName=bao;baoc
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".spec.version"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// OpenBaoCluster is the Schema for the openbaoclusters API.
type OpenBaoCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of OpenBaoCluster.
	Spec OpenBaoClusterSpec `json:"spec"`

	// Status defines the observed state of OpenBaoCluster.
	// +optional
	Status OpenBaoClusterStatus `json:"status"`
}

// +kubebuilder:object:root=true

// OpenBaoClusterList contains a list of OpenBaoCluster.
type OpenBaoClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []OpenBaoCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OpenBaoCluster{}, &OpenBaoClusterList{})
}
