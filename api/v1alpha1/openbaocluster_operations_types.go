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

// BackupSchedule defines when and where snapshots are stored.
type BackupSchedule struct {
	// RestoreTest periodically restores the latest successful snapshot into a disposable target.
	// +optional
	RestoreTest *RestoreTest `json:"restoreTest,omitempty"`

	// Schedule is a cron-style schedule, for example "0 3 * * *".
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`
	// Target is the object storage configuration for backups.
	Target BackupTarget `json:"target"`
	// JWTAuthRole is the name of the JWT Auth role configured in OpenBao
	// for backup operations. When set, the backup executor will use JWT Auth
	// (projected ServiceAccount token) instead of a static token. This is the preferred authentication
	// method as tokens are automatically rotated by Kubernetes.
	//
	// The role must be configured in OpenBao and must grant the "read" capability on
	// sys/storage/raft/snapshot. The role must bind to the backup ServiceAccount
	// (<cluster-name>-backup-serviceaccount) in the cluster namespace.
	//
	// If OIDC is enabled in SelfInit and this field is empty, a default role
	// named "openbao-operator-backup" will be assumed/created.
	// +optional
	JWTAuthRole string `json:"jwtAuthRole,omitempty"`
	// TokenSecretRef references a Secret containing an OpenBao API token to use
	// for backup operations when JWT Auth is not effective.
	//
	// The Secret must exist in the same namespace as the OpenBaoCluster.
	// Cross-namespace references are not allowed for security reasons.
	//
	// If an effective JWT role exists through JWTAuthRole or the SelfInit OIDC
	// default, this field is ignored in favor of JWT Auth. Otherwise this field
	// is required and must reference a token with permission to read
	// sys/storage/raft/snapshot. The operator does not infer or fall back to a
	// <cluster>-root-token Secret.
	// +optional
	TokenSecretRef *corev1.LocalObjectReference `json:"tokenSecretRef,omitempty"`
	// Retention defines optional backup retention policy.
	// +optional
	Retention *BackupRetention `json:"retention,omitempty"`
	// Image is the container image to use for backup operations.
	// If not specified, OPERATOR_BACKUP_IMAGE can supply a complete image reference, including a digest.
	// Otherwise, defaults to "<repo>:X.Y.Z" where <repo> is derived from OPERATOR_BACKUP_IMAGE_REPOSITORY
	// (default: "ghcr.io/kubebao/openbao-backup") and the tag matches OPERATOR_VERSION.
	// This allows users to override the image for air-gapped environments or custom registries.
	// +optional
	Image string `json:"image,omitempty"`
}

// UpdateStrategyType defines the type of update strategy to use.
// +kubebuilder:validation:Enum=RollingUpdate;BlueGreen
type UpdateStrategyType string

const (
	// UpdateStrategyRollingUpdate uses a rolling update strategy (default).
	UpdateStrategyRollingUpdate UpdateStrategyType = "RollingUpdate"
	// UpdateStrategyBlueGreen uses a blue/green deployment strategy.
	UpdateStrategyBlueGreen UpdateStrategyType = "BlueGreen"
)

// VerificationConfig allows defining custom health checks before promotion.
type VerificationConfig struct {
	// MinSyncDuration ensures the Green cluster stays healthy as a non-voter
	// for at least this duration before promotion (e.g., "5m").
	// +optional
	MinSyncDuration string `json:"minSyncDuration,omitempty"`

	// PrePromotionHook specifies a Job template to run before promoting Green.
	// The job must complete successfully (exit 0) for promotion to proceed.
	// If the job fails, the operator either aborts or rolls back automatically
	// when blueGreen.autoRollback.onValidationFailure is enabled; otherwise it
	// holds for manual resolution.
	// The operator assigns the hook a stable identity for each upgrade attempt.
	// It does not recreate a missing Job after Job creation is committed.
	// +optional
	PrePromotionHook *ValidationHookConfig `json:"prePromotionHook,omitempty"`
}

// ValidationHookConfig defines a user-supplied validation Job.
type ValidationHookConfig struct {
	// Image is the container image for the validation job.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
	// Command is the command to run.
	// +optional
	Command []string `json:"command,omitempty"`
	// Args are arguments passed to the command.
	// +optional
	Args []string `json:"args,omitempty"`
	// TimeoutSeconds is the job timeout (default: 300s).
	// +kubebuilder:default=300
	// +optional
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
}

// AutoRollbackConfig defines conditions that trigger automatic rollback.
type AutoRollbackConfig struct {
	// Enabled controls whether automatic rollback is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
	// OnJobFailure triggers rollback when job failures exceed MaxJobFailures.
	// Only applies during early phases (before demoting Blue).
	// +kubebuilder:default=true
	OnJobFailure bool `json:"onJobFailure,omitempty"`
	// OnValidationFailure triggers automatic abort/rollback if the pre-promotion
	// hook fails.
	// +kubebuilder:default=true
	OnValidationFailure bool `json:"onValidationFailure,omitempty"`
}

// BlueGreenConfig configures the behavior when Type is BlueGreen.
type BlueGreenConfig struct {
	// AutoPromote controls whether newly started blue/green upgrades
	// automatically switch traffic and delete the old cluster after sync.
	// If false when an upgrade starts, that upgrade stays in the Syncing
	// phase waiting for an explicit promotion request via spec.upgrade.requests.promote.
	// Changing this field while an upgrade is already in progress affects only
	// future upgrades.
	// +kubebuilder:default=true
	AutoPromote bool `json:"autoPromote"`

	// VerificationConfig allows defining custom health checks before promotion.
	// +optional
	Verification *VerificationConfig `json:"verification,omitempty"`

	// MaxJobFailures is the maximum consecutive job failures before aborting/rolling back.
	// Defaults to 5 if not specified.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxJobFailures *int32 `json:"maxJobFailures,omitempty"`

	// PreUpgradeSnapshot triggers a backup at the start of an upgrade.
	// Creates a recovery point before any changes are made.
	// Requires spec.backup to be configured.
	// +optional
	PreUpgradeSnapshot bool `json:"preUpgradeSnapshot,omitempty"`

	// AutoRollback configures automatic rollback behavior.
	// +optional
	AutoRollback *AutoRollbackConfig `json:"autoRollback,omitempty"`
}

// UpgradeRequestConfig defines one-shot operator requests for upgrade workflows.
type UpgradeRequestConfig struct {
	// Retry requests a retry of the current failed rolling upgrade when changed
	// to a new non-empty value.
	//
	// The operator compares this value against status.upgradeRequests.lastHandledRetry
	// and acts only when the value changes. Recommended value is an RFC3339
	// timestamp string.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Retry string `json:"retry,omitempty"`
	// Promote requests promotion of a held blue/green upgrade when changed to a
	// new non-empty value while spec.upgrade.blueGreen.autoPromote=false.
	//
	// The operator compares this value against
	// status.upgradeRequests.lastHandledPromote and acts only when the value
	// changes. Recommended value is an RFC3339 timestamp string.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Promote string `json:"promote,omitempty"`
	// Rollback requests a manual abort or rollback of the current blue/green
	// upgrade when changed to a new non-empty value.
	//
	// The operator compares this value against
	// status.upgradeRequests.lastHandledRollback and acts only when the value
	// changes. Recommended value is an RFC3339 timestamp string.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Rollback string `json:"rollback,omitempty"`
}

// UpgradeConfig defines configuration for upgrade operations.
type UpgradeConfig struct {
	// Image is the container image to use for upgrade operations.
	//
	// This image is used by Kubernetes Jobs created during upgrades (for example, blue/green
	// cluster orchestration actions). The executor runs inside the tenant namespace and
	// authenticates to OpenBao using a projected ServiceAccount token (JWT auth).
	//
	// If not specified, OPERATOR_UPGRADE_IMAGE can supply a complete image reference, including a digest.
	// Otherwise, defaults to "<repo>:X.Y.Z" where <repo> is derived from OPERATOR_UPGRADE_IMAGE_REPOSITORY
	// (default: "ghcr.io/kubebao/openbao-upgrade") and the tag matches OPERATOR_VERSION.
	// +optional
	Image string `json:"image,omitempty"`

	// PreUpgradeSnapshot, when true, triggers a backup before any upgrade.
	// When enabled, the upgrade manager will create a backup using the backup
	// configuration (spec.backup.target, spec.backup.image, etc.) and
	// wait for it to complete before proceeding with the upgrade.
	//
	// If the backup fails, the upgrade will be blocked and a Degraded condition
	// will be set with Reason=PreUpgradeBackupFailed.
	//
	// Requires spec.backup to be configured with target, image, and
	// authentication (jwtAuthRole or tokenSecretRef).
	// +optional
	PreUpgradeSnapshot bool `json:"preUpgradeSnapshot,omitempty"`
	// JWTAuthRole is the name of the JWT Auth role configured in OpenBao
	// for upgrade executor Jobs. The executor authenticates with a projected
	// ServiceAccount token from <cluster-name>-upgrade-serviceaccount.
	//
	// The role must be configured in OpenBao and must grant the permissions
	// required by the selected upgrade strategy, including:
	// - "read" capability on sys/health
	// - "sudo" and "update" capability on sys/step-down
	// - "read" capability on sys/storage/raft/autopilot/state
	// - for Blue/Green, raft join/configuration/remove-peer/promote/demote operations
	// The role must bind to the upgrade ServiceAccount (<cluster-name>-upgrade-serviceaccount),
	// which is automatically created by the operator.
	//
	// If OIDC is enabled during initial SelfInit bootstrap and this field is
	// empty, a default role named "openbao-operator-upgrade" will be created.
	// For already-initialized clusters, configure this role explicitly or keep
	// the default role created during initial bootstrap.
	//
	// This is the supported authentication mechanism for built-in upgrade orchestration.
	// +optional
	JWTAuthRole string `json:"jwtAuthRole,omitempty"`
	// Strategy defines the update strategy to use.
	// +kubebuilder:default="RollingUpdate"
	Strategy UpdateStrategyType `json:"strategy,omitempty"`

	// Requests defines explicit one-shot operator requests for the current
	// upgrade workflow. The operator acts only when a request value changes.
	// +optional
	Requests *UpgradeRequestConfig `json:"requests,omitempty"`

	// BlueGreen configures the behavior when Strategy is BlueGreen.
	// +optional
	BlueGreen *BlueGreenConfig `json:"blueGreen,omitempty"`
}

// RestoreConfig defines optional configuration for restore operations.
//
// This is primarily used with self-init JWT bootstrap to pre-create a JWT role
// that can be referenced by OpenBaoRestore resources.
type RestoreConfig struct {
	// JWTAuthRole is the name of the JWT Auth role configured in OpenBao
	// for restore operations. When set, and when spec.selfInit.oidc.enabled is true,
	// the operator bootstraps a restore policy and JWT role bound to the restore ServiceAccount
	// (<cluster-name>-restore-serviceaccount).
	//
	// If OIDC is enabled in SelfInit and this field is empty, a default role
	// named "openbao-operator-restore" will be assumed/created.
	//
	// The role must grant "update" capability on sys/storage/raft/snapshot and
	// sys/storage/raft/snapshot-force. The force endpoint supports explicitly
	// requested break-glass restores.
	//
	// +optional
	JWTAuthRole string `json:"jwtAuthRole,omitempty"`
}

// BackupRetention defines retention policy for backups.
type BackupRetention struct {
	// MaxCount is the maximum number of backups to retain (0 = unlimited).
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxCount int32 `json:"maxCount,omitempty"`
	// MaxAge is the maximum age of backups to retain, e.g., "168h" for 7 days.
	// Backups older than this are deleted after successful new backup upload.
	// +optional
	MaxAge string `json:"maxAge,omitempty"`
}

// BackupTarget describes a generic, cloud-agnostic object storage destination.
// +kubebuilder:validation:XValidation:rule="self.provider != 's3' || (has(self.endpoint) && size(self.endpoint) > 0)",message="backup target endpoint is required when provider is s3"
// +kubebuilder:validation:XValidation:rule="self.provider == 'gcs' || !has(self.gcs)",message="backup target gcs options are only supported when provider is gcs"
// +kubebuilder:validation:XValidation:rule="self.provider == 'azure' || !has(self.azure)",message="backup target azure options are only supported when provider is azure"
// +kubebuilder:validation:XValidation:rule="self.provider == 's3' || !has(self.roleArn) || size(self.roleArn) == 0",message="backup target roleArn is only supported when provider is s3"
type BackupTarget struct {
	// Provider selects the storage backend. Defaults to "s3" for backward compatibility.
	// +optional
	// +kubebuilder:default=s3
	// +kubebuilder:validation:Enum=s3;gcs;azure
	Provider string `json:"provider,omitempty"`
	// Endpoint is the HTTP(S) endpoint for the object storage service.
	// For S3: Required (e.g., "https://s3.amazonaws.com" or MinIO endpoint).
	// For GCS: Optional (defaults to googleapis.com).
	// For Azure: Optional (derived from StorageAccount if not specified).
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// Bucket is the bucket or container name.
	// +kubebuilder:validation:MinLength=1
	Bucket string `json:"bucket"`
	// PathPrefix is an optional prefix within the bucket for this cluster's snapshots.
	// +optional
	PathPrefix string `json:"pathPrefix,omitempty"`
	// CredentialsSecretRef optionally references a Secret containing credentials for the object store.
	// The Secret must exist in the same namespace as the owning OpenBao resource.
	// Cross-namespace references are not allowed for security reasons.
	//
	// For S3: Expected keys are "accessKeyId" and "secretAccessKey" (optional: "sessionToken", "region", "caCert").
	// For GCS: Expected key is "credentials.json" containing a service account JSON key.
	// For Azure: Expected keys are "accountKey" or "connectionString".
	// Hardened clusters require an explicit storage identity path: credentialsSecretRef,
	// workloadIdentity metadata, or roleArn for S3 targets. Omitting those paths relies
	// on ambient/default credentials and is rejected for Hardened clusters.
	// +optional
	CredentialsSecretRef *corev1.LocalObjectReference `json:"credentialsSecretRef,omitempty"`
	// WorkloadIdentity optionally applies provider-specific metadata required by cloud workload identity integrations.
	// Use this for ambient identity setups such as EKS Pod Identity or IRSA, GKE Workload Identity, or Azure Workload Identity.
	// When omitted, backup and restore workloads can still use any credentials exposed through the pod's default provider chain.
	// Hardened clusters reject that ambient/default path unless credentialsSecretRef is set,
	// workloadIdentity metadata is present, or an S3 target uses roleArn.
	// +optional
	WorkloadIdentity *WorkloadIdentityConfig `json:"workloadIdentity,omitempty"`
	// PartSize is the size of each part in multipart uploads (in bytes).
	// Defaults to 10MB (10485760 bytes). Larger values may improve performance for large snapshots
	// on fast networks, while smaller values may be better for slow or unreliable networks.
	// +optional
	// +kubebuilder:default=10485760
	// +kubebuilder:validation:Minimum=5242880
	PartSize int64 `json:"partSize,omitempty"`
	// Concurrency is the number of concurrent parts to upload during multipart uploads.
	// Defaults to 3. Higher values may improve throughput on fast networks but increase
	// memory usage and may overwhelm slower storage backends.
	// +optional
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	Concurrency int32 `json:"concurrency,omitempty"`

	// --- S3-specific configuration (only used when Provider=s3) ---

	// Region is the AWS region to use for S3-compatible clients.
	// For AWS, this should match the bucket region (for example, "eu-west-1").
	// For many S3-compatible stores (MinIO/Ceph), this can be any non-empty value.
	// Only used when Provider is "s3".
	// +optional
	// +kubebuilder:default=us-east-1
	Region string `json:"region,omitempty"`
	// RoleARN is the IAM role ARN (or S3-compatible equivalent) to assume via Web Identity.
	// When set, backup and restore Jobs mount a projected ServiceAccount token and set the
	// AWS Web Identity environment variables explicitly.
	// Only used when Provider is "s3".
	// Outside Hardened S3 targets, leave this empty when relying on ambient workload identity
	// or provider-managed default credentials instead. For Hardened S3 targets, roleArn is
	// one accepted explicit identity path. It does not satisfy Hardened identity requirements
	// for GCS or Azure.
	// +optional
	RoleARN string `json:"roleArn,omitempty"`
	// UsePathStyle controls whether to use path-style addressing (bucket.s3.amazonaws.com/object)
	// or virtual-hosted-style addressing (bucket.s3.amazonaws.com/object).
	// Set to true for MinIO and S3-compatible stores that require path-style.
	// Set to false for AWS S3 (default, as AWS is deprecating path-style).
	// Only used when Provider is "s3".
	// +optional
	// +kubebuilder:default=false
	UsePathStyle bool `json:"usePathStyle,omitempty"`

	// --- GCS-specific configuration (only used when Provider=gcs) ---

	// GCS contains Google Cloud Storage specific configuration.
	// Only used when Provider is "gcs".
	// +optional
	GCS *GCSTargetConfig `json:"gcs,omitempty"`

	// Azure contains Azure Blob Storage specific configuration.
	// Only used when Provider is "azure".
	// +optional
	Azure *AzureTargetConfig `json:"azure,omitempty"`

	// InsecureSkipVerify allows skipping TLS verification (useful for MinIO/LocalStack/Azurite with self-signed certs).
	// This applies to all providers that support TLS.
	// Hardened clusters reject insecureSkipVerify.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// WorkloadIdentityConfig configures cloud workload identity metadata for backup and restore workloads.
type WorkloadIdentityConfig struct {
	// ServiceAccountAnnotations are merged into the generated backup or restore ServiceAccount.
	// An existing restore executor ServiceAccount must already have these annotations;
	// the operator validates them without modifying that account.
	// This is typically used for provider-specific bindings such as GKE Workload Identity
	// or webhook-based AWS/Azure workload identity integrations.
	// +optional
	ServiceAccountAnnotations map[string]string `json:"serviceAccountAnnotations,omitempty"`
	// PodLabels are merged into the generated backup or restore Job pod template.
	// This is typically used for provider-specific selectors such as Azure Workload Identity.
	// Operator-managed labels take precedence if the same key is specified here.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`
}

// GCSTargetConfig holds Google Cloud Storage specific configuration.
type GCSTargetConfig struct {
	// Project is the GCP project ID. Optional if using ADC with default project or
	// if the credentials JSON includes the project.
	// +optional
	Project string `json:"project,omitempty"`
}

// AzureTargetConfig holds Azure Blob Storage specific configuration.
type AzureTargetConfig struct {
	// StorageAccount is the Azure storage account name.
	// Required when using Azure provider.
	// +kubebuilder:validation:MinLength=1
	StorageAccount string `json:"storageAccount,omitempty"`
	// Container is the blob container name. If empty, uses the Bucket field value.
	// +optional
	Container string `json:"container,omitempty"`
}
