---
title: OpenBaoCluster API
description: Fields, defaults, and validation for the OpenBaoCluster API.
eyebrow: Reference · Generated API
weight: 1
verifiedBy:
  - api/v1alpha1 at HEAD
  - website/generated/api-reference.md at HEAD
---

{{< callout type="note" title="Generated reference" >}}

This page is synchronized from the generated API reference at `HEAD` for the `next` documentation line.
{{< /callout >}}


## Packages
- [openbao.org/v1alpha1](#openbaoorgv1alpha1)


## openbao.org/v1alpha1

Package v1alpha1 contains API Schema definitions for the openbao v1alpha1 API group.

### Resource Types
- [OpenBaoCluster](#openbaocluster)



#### ACMEConfig



ACMEConfig configures ACME certificate management for OpenBao.
See: https://openbao.org/docs/configuration/listener/tcp/#acme-parameters



_Appears in:_
- [TLSConfig](#tlsconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `directoryURL` _string_ | DirectoryURL is the ACME directory URL (e.g., `https://acme-v02.api.letsencrypt.org/directory`). |  | MinLength: 1 <br /> |
| `domains` _string array_ | Domains is the list of domain names for which to obtain the certificate.<br />This maps to OpenBao's listener `tls_acme_domains` field.<br />When empty, the operator will default to an internal Service name suitable for<br />private ACME CAs running inside the cluster (e.g., "&lt;cluster&gt;-acme.&lt;namespace&gt;.svc"). |  | MinItems: 1 <br />Optional: \{\} <br /> |
| `email` _string_ | Email is the email address to use for ACME registration. |  | Optional: \{\} <br /> |
| `sharedCache` _[ACMESharedCacheConfig](#acmesharedcacheconfig)_ | SharedCache configures a filesystem cache shared across OpenBao replicas for ACME account<br />and certificate state. This is required for HA ACME topologies where more than one Pod<br />can serve the same hostname concurrently. |  | Optional: \{\} <br /> |


#### ACMESharedCacheConfig



ACMESharedCacheConfig configures the shared filesystem cache for ACME account and certificate state.
See: https://openbao.org/docs/configuration/listener/tcp/#acme-parameters



_Appears in:_
- [ACMEConfig](#acmeconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _[ACMESharedCacheMode](#acmesharedcachemode)_ | Mode selects whether the operator creates a dedicated RWX PVC or mounts an existing one. |  | Enum: [ManagedPVC ExistingPVC] <br /> |
| `existingClaimName` _string_ | ExistingClaimName is the name of a pre-created RWX PVC in the same namespace.<br />Required when Mode is ExistingPVC. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `size` _string_ | Size is the requested capacity for the managed ACME cache PVC.<br />Required when Mode is ManagedPVC. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `storageClassName` _string_ | StorageClassName is an optional StorageClass for the managed ACME cache PVC. |  | Optional: \{\} <br /> |


#### ACMESharedCacheMode

_Underlying type:_ _string_

ACMESharedCacheMode controls how the operator provides a shared filesystem for OpenBao's ACME cache.

_Validation:_
- Enum: [ManagedPVC ExistingPVC]

_Appears in:_
- [ACMESharedCacheConfig](#acmesharedcacheconfig)

| Field | Description |
| --- | --- |
| `ManagedPVC` | ACMESharedCacheModeManagedPVC instructs the operator to create a dedicated RWX PVC.<br /> |
| `ExistingPVC` | ACMESharedCacheModeExistingPVC instructs the operator to mount an existing RWX PVC.<br /> |


#### AWSKMSSealConfig



AWSKMSSealConfig configures the AWS KMS seal type.
See: https://openbao.org/docs/configuration/seal/awskms/



_Appears in:_
- [UnsealConfig](#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `region` _string_ | Region is the AWS region where the encryption key lives. |  | MinLength: 1 <br /> |
| `kmsKeyID` _string_ | KMSKeyID is the AWS KMS key ID or ARN to use for encryption and decryption.<br />An alias in the format "alias/key-alias-name" may also be used. |  | MinLength: 1 <br /> |
| `endpoint` _string_ | Endpoint is the KMS API endpoint to be used for AWS KMS requests.<br />Useful when connecting to KMS over a VPC Endpoint. |  | Optional: \{\} <br /> |
| `accessKey` _string_ | AccessKey is the AWS access key ID to use.<br />Note: It is strongly recommended to use CredentialsSecretRef or Workload Identity (IRSA) instead. |  | Optional: \{\} <br /> |
| `secretKey` _string_ | SecretKey is the AWS secret access key to use.<br />Note: It is strongly recommended to use CredentialsSecretRef or Workload Identity (IRSA) instead. |  | Optional: \{\} <br /> |
| `sessionToken` _string_ | SessionToken specifies the AWS session token. |  | Optional: \{\} <br /> |


#### AdminOpsControllerStatus



AdminOpsControllerStatus holds status owned by the adminops controller.



_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `lastError` _[ControllerErrorStatus](#controllererrorstatus)_ | LastError is the last adminops-controller error observed for this cluster. |  | Optional: \{\} <br /> |


#### AuditDevice



AuditDevice defines a declarative audit device configuration.
See: https://openbao.org/docs/configuration/audit/



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of audit device (e.g., "file", "syslog", "socket", "http"). |  | Enum: [file syslog socket http] <br />MinLength: 1 <br /> |
| `path` _string_ | Path is the path of the audit device in the root namespace. |  | MinLength: 1 <br /> |
| `description` _string_ | Description is an optional description for the audit device. |  | Optional: \{\} <br /> |
| `fileOptions` _[FileAuditOptions](#fileauditoptions)_ | FileOptions configures options for file audit devices.<br />Only used when Type is "file". |  | Optional: \{\} <br /> |
| `httpOptions` _[HTTPAuditOptions](#httpauditoptions)_ | HTTPOptions configures options for HTTP audit devices.<br />Only used when Type is "http". |  | Optional: \{\} <br /> |
| `syslogOptions` _[SyslogAuditOptions](#syslogauditoptions)_ | SyslogOptions configures options for syslog audit devices.<br />Only used when Type is "syslog". |  | Optional: \{\} <br /> |
| `socketOptions` _[SocketAuditOptions](#socketauditoptions)_ | SocketOptions configures options for socket audit devices.<br />Only used when Type is "socket". |  | Optional: \{\} <br /> |
| `options` _[JSON](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#json-v1-apiextensions-k8s-io)_ | Options contains device-specific configuration options as a string map.<br />This is a fallback for backward compatibility and advanced use cases.<br />If structured options (FileOptions, HTTPOptions, etc.) are provided, they take precedence.<br />OpenBao audit options are string-to-string; scalar JSON values are rendered as strings,<br />while nested objects and arrays are rejected. For HTTP headers, prefer httpOptions.headers. |  | Optional: \{\} <br /> |


#### AuditFileStorageConfig



AuditFileStorageConfig configures the shared filesystem integration point for file audit devices.

The operator mounts the selected PVC into each OpenBao Pod. Each Pod uses a
pod-specific subPath under the same PVC so all Pods can render the same audit
file path while collectors can mount the PVC read-only and read per-Pod audit
files from the backing directories. This storage is intended as a collector
handoff and replay buffer, not as the authoritative compliance archive.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _[AuditFileStorageMode](#auditfilestoragemode)_ | Mode selects whether the operator creates a dedicated RWX PVC or mounts an existing one. |  | Enum: [ManagedPVC ExistingPVC] <br /> |
| `existingClaimName` _string_ | ExistingClaimName is the name of a pre-created RWX PVC in the same namespace.<br />Required when Mode is ExistingPVC. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `size` _string_ | Size is the requested capacity for the managed audit file storage PVC.<br />Required when Mode is ManagedPVC. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `storageClassName` _string_ | StorageClassName is an optional StorageClass for the managed audit file storage PVC. |  | Optional: \{\} <br /> |
| `mountPath` _string_ | MountPath is where the audit file storage PVC is mounted in OpenBao Pods.<br />File audit device paths must be under this path when auditFileStorage is configured. | /openbao/audit | Optional: \{\} <br /> |


#### AuditFileStorageMode

_Underlying type:_ _string_

AuditFileStorageMode controls how the operator provides shared filesystem storage for file audit logs.

_Validation:_
- Enum: [ManagedPVC ExistingPVC]

_Appears in:_
- [AuditFileStorageConfig](#auditfilestorageconfig)

| Field | Description |
| --- | --- |
| `ManagedPVC` | AuditFileStorageModeManagedPVC instructs the operator to create a dedicated RWX PVC.<br /> |
| `ExistingPVC` | AuditFileStorageModeExistingPVC instructs the operator to mount an existing RWX PVC.<br /> |


#### AutoRollbackConfig



AutoRollbackConfig defines conditions that trigger automatic rollback.



_Appears in:_
- [BlueGreenConfig](#bluegreenconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether automatic rollback is active. | true |  |
| `onJobFailure` _boolean_ | OnJobFailure triggers rollback when job failures exceed MaxJobFailures.<br />Only applies during early phases (before demoting Blue). | true |  |
| `onValidationFailure` _boolean_ | OnValidationFailure triggers automatic abort/rollback if the pre-promotion<br />hook fails. | true |  |


#### AzureKeyVaultSealConfig



AzureKeyVaultSealConfig configures the Azure Key Vault seal type.
See: https://openbao.org/docs/configuration/seal/azurekeyvault/



_Appears in:_
- [UnsealConfig](#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `vaultName` _string_ | VaultName is the name of the Azure Key Vault. |  | MinLength: 1 <br /> |
| `keyName` _string_ | KeyName is the name of the key in the Azure Key Vault. |  | MinLength: 1 <br /> |
| `tenantID` _string_ | TenantID is the Azure tenant ID. |  | Optional: \{\} <br /> |
| `clientID` _string_ | ClientID is the Azure client ID. |  | Optional: \{\} <br /> |
| `clientSecret` _string_ | ClientSecret is the Azure client secret.<br />Note: It is strongly recommended to use CredentialsSecretRef or Managed Service Identity instead. |  | Optional: \{\} <br /> |
| `resource` _string_ | Resource is the Azure AD resource endpoint.<br />For Managed HSM, this should usually be "managedhsm.azure.net". |  | Optional: \{\} <br /> |
| `environment` _string_ | Environment is the Azure environment (e.g., "AzurePublicCloud", "AzureUSGovernmentCloud"). |  | Optional: \{\} <br /> |
| `authMethod` _string_ | AuthMethod selects how the seal authenticates to Azure, rendered as the<br />seal's auth_method. When empty, OpenBao chooses: a client ID without a<br />client secret selects managed_identity, which uses the instance metadata<br />service. Set workload_identity for Azure Workload Identity, where the<br />workload identity webhook also injects AZURE_CLIENT_ID. |  | Enum: [workload_identity managed_identity client_secret environment default] <br />Optional: \{\} <br /> |


#### AzureTargetConfig



AzureTargetConfig holds Azure Blob Storage specific configuration.



_Appears in:_
- [BackupTarget](#backuptarget)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `storageAccount` _string_ | StorageAccount is the Azure storage account name.<br />Required when using Azure provider. |  | MinLength: 1 <br /> |
| `container` _string_ | Container is the blob container name. If empty, uses the Bucket field value. |  | Optional: \{\} <br /> |


#### BackendTLSConfig



BackendTLSConfig configures BackendTLSPolicy for Gateway API.



_Appears in:_
- [GatewayConfig](#gatewayconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether the Operator creates a BackendTLSPolicy.<br />When true (default when Gateway is enabled), the Operator creates a BackendTLSPolicy<br />that enables HTTPS and certificate validation for backend connections.<br />When false, no BackendTLSPolicy is created and the Gateway will use HTTP (or rely on<br />external configuration for TLS).<br />Hardened clusters reject backendTLS.enabled=false. | true | Optional: \{\} <br /> |
| `hostname` _string_ | Hostname is the hostname to verify in the backend certificate.<br />If not specified, defaults to the stable TLS server name used by operator-managed<br />clients (for example, openbao-cluster-&lt;cluster-name&gt;.local).<br />This must match a DNS SAN in the backend certificate. |  | Optional: \{\} <br /> |


#### BackupRetention



BackupRetention defines retention policy for backups.



_Appears in:_
- [BackupSchedule](#backupschedule)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `maxCount` _integer_ | MaxCount is the maximum number of backups to retain (0 = unlimited). |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `maxAge` _string_ | MaxAge is the maximum age of backups to retain, e.g., "168h" for 7 days.<br />Backups older than this are deleted after successful new backup upload. |  | Optional: \{\} <br /> |


#### BackupSchedule



BackupSchedule defines when and where snapshots are stored.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `restoreTest` _[RestoreTest](#restoretest)_ | RestoreTest periodically restores the latest successful snapshot into a disposable target. |  | Optional: \{\} <br /> |
| `schedule` _string_ | Schedule is a cron-style schedule, for example "0 3 * * *". |  | MinLength: 1 <br /> |
| `target` _[BackupTarget](#backuptarget)_ | Target is the object storage configuration for backups. |  |  |
| `jwtAuthRole` _string_ | JWTAuthRole is the name of the JWT Auth role configured in OpenBao<br />for backup operations. When set, the backup executor will use JWT Auth<br />(projected ServiceAccount token) instead of a static token. This is the preferred authentication<br />method as tokens are automatically rotated by Kubernetes.<br />The role must be configured in OpenBao and must grant the "read" capability on<br />sys/storage/raft/snapshot. The role must bind to the backup ServiceAccount<br />(&lt;cluster-name&gt;-backup-serviceaccount) in the cluster namespace.<br />If OIDC is enabled in SelfInit and this field is empty, a default role<br />named "openbao-operator-backup" will be assumed/created. |  | Optional: \{\} <br /> |
| `tokenSecretRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | TokenSecretRef references a Secret containing an OpenBao API token to use<br />for backup operations when JWT Auth is not effective.<br />The Secret must exist in the same namespace as the OpenBaoCluster.<br />Cross-namespace references are not allowed for security reasons.<br />If an effective JWT role exists through JWTAuthRole or the SelfInit OIDC<br />default, this field is ignored in favor of JWT Auth. Otherwise this field<br />is required and must reference a token with permission to read<br />sys/storage/raft/snapshot. The operator does not infer or fall back to a<br />&lt;cluster&gt;-root-token Secret. |  | Optional: \{\} <br /> |
| `retention` _[BackupRetention](#backupretention)_ | Retention defines optional backup retention policy. |  | Optional: \{\} <br /> |
| `image` _string_ | Image is the container image to use for backup operations.<br />If not specified, OPERATOR_BACKUP_IMAGE can supply a complete image reference, including a digest.<br />Otherwise, defaults to "&lt;repo&gt;:X.Y.Z" where &lt;repo&gt; is derived from OPERATOR_BACKUP_IMAGE_REPOSITORY<br />(default: "ghcr.io/kubebao/openbao-backup") and the tag matches OPERATOR_VERSION.<br />This allows users to override the image for air-gapped environments or custom registries. |  | Optional: \{\} <br /> |


#### BackupSnapshotSummary



BackupSnapshotSummary is the latest successful executor's bounded observation.
Identity and version are observed before and after streaming the snapshot.



_Appears in:_
- [BackupStatus](#backupstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `digest` _string_ | Digest is sha256: followed by the lowercase digest of the uploaded snapshot. |  | Pattern: `^sha256:[a-f0-9]\{64\}$` <br /> |
| `clusterID` _string_ | ClusterID is the source native cluster ID observed during the backup. |  | MaxLength: 128 <br /> |
| `version` _string_ | Version is the source OpenBao version observed during the backup. |  | MaxLength: 64 <br /> |
| `size` _integer_ | Size is the snapshot size in bytes. |  | Maximum: 8.589934592e+09 <br />Minimum: 1 <br /> |


#### BackupStatus



BackupStatus tracks the state of backups for a cluster.



_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `successfulBackups` _integer_ | SuccessfulBackups counts newly observed successful backup Jobs. |  | Optional: \{\} <br /> |
| `latestSnapshot` _[BackupSnapshotSummary](#backupsnapshotsummary)_ | LatestSnapshot contains the latest executor's source observation. |  | Optional: \{\} <br /> |
| `restoreTest` _[RestoreTestStatus](#restoreteststatus)_ | RestoreTest stores one active run and one last result. |  | Optional: \{\} <br /> |
| `lastBackupTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastBackupTime is the timestamp of the last successful backup. |  | Optional: \{\} <br /> |
| `lastAttemptTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastAttemptTime is the timestamp of the last backup attempt, regardless of outcome.<br />This is used to avoid retry loops when a scheduled backup fails. |  | Optional: \{\} <br /> |
| `lastAttemptScheduledTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastAttemptScheduledTime is the scheduled time of the last backup attempt.<br />It is derived from the cron schedule and used to ensure at-most-once execution<br />per scheduled window. |  | Optional: \{\} <br /> |
| `lastHandledManualTrigger` _string_ | LastHandledManualTrigger is the last observed manual trigger token that<br />has progressed into an actual backup attempt. |  | Optional: \{\} <br /> |
| `lastBackupSize` _integer_ | LastBackupSize is the size in bytes of the last successful backup. |  | Optional: \{\} <br /> |
| `lastBackupDuration` _string_ | LastBackupDuration is how long the last backup took (e.g., "45s"). |  | Optional: \{\} <br /> |
| `lastBackupName` _string_ | LastBackupName is the object key/path of the last successful backup. |  | Optional: \{\} <br /> |
| `nextScheduledBackup` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | NextScheduledBackup is when the next backup is scheduled. |  | Optional: \{\} <br /> |
| `consecutiveFailures` _integer_ | ConsecutiveFailures is the number of consecutive backup failures. |  | Optional: \{\} <br /> |
| `lastFailureReason` _string_ | LastFailureReason is the low-cardinality reason code for the last backup failure (if applicable). |  | Optional: \{\} <br /> |
| `lastFailureMessage` _string_ | LastFailureMessage is the detailed message for the last backup failure (if applicable). |  | Optional: \{\} <br /> |
| `lastFailureTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastFailureTime is when the last backup failure was recorded. |  | Optional: \{\} <br /> |


#### BackupTarget



BackupTarget describes a generic, cloud-agnostic object storage destination.



_Appears in:_
- [BackupSchedule](#backupschedule)
- [RestoreSource](../openbaorestore/#restoresource)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `provider` _string_ | Provider selects the storage backend. Defaults to "s3" for backward compatibility. | s3 | Enum: [s3 gcs azure] <br />Optional: \{\} <br /> |
| `endpoint` _string_ | Endpoint is the HTTP(S) endpoint for the object storage service.<br />For S3: Required (e.g., "https://s3.amazonaws.com" or MinIO endpoint).<br />For GCS: Optional (defaults to googleapis.com).<br />For Azure: Optional (derived from StorageAccount if not specified). |  | Optional: \{\} <br /> |
| `bucket` _string_ | Bucket is the bucket or container name. |  | MinLength: 1 <br /> |
| `pathPrefix` _string_ | PathPrefix is an optional prefix within the bucket for this cluster's snapshots. |  | Optional: \{\} <br /> |
| `credentialsSecretRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | CredentialsSecretRef optionally references a Secret containing credentials for the object store.<br />The Secret must exist in the same namespace as the owning OpenBao resource.<br />Cross-namespace references are not allowed for security reasons.<br />For S3: Expected keys are "accessKeyId" and "secretAccessKey" (optional: "sessionToken", "region", "caCert").<br />For GCS: Expected key is "credentials.json" containing a service account JSON key.<br />For Azure: Expected keys are "accountKey" or "connectionString".<br />Hardened clusters require an explicit storage identity path: credentialsSecretRef,<br />workloadIdentity metadata, or roleArn for S3 targets. Omitting those paths relies<br />on ambient/default credentials and is rejected for Hardened clusters. |  | Optional: \{\} <br /> |
| `workloadIdentity` _[WorkloadIdentityConfig](#workloadidentityconfig)_ | WorkloadIdentity optionally applies provider-specific metadata required by cloud workload identity integrations.<br />Use this for ambient identity setups such as EKS Pod Identity or IRSA, GKE Workload Identity, or Azure Workload Identity.<br />When omitted, backup and restore workloads can still use any credentials exposed through the pod's default provider chain.<br />Hardened clusters reject that ambient/default path unless credentialsSecretRef is set,<br />workloadIdentity metadata is present, or an S3 target uses roleArn. |  | Optional: \{\} <br /> |
| `partSize` _integer_ | PartSize is the size of each part in multipart uploads (in bytes).<br />Defaults to 10MB (10485760 bytes). Larger values may improve performance for large snapshots<br />on fast networks, while smaller values may be better for slow or unreliable networks. | 10485760 | Minimum: 5.24288e+06 <br />Optional: \{\} <br /> |
| `concurrency` _integer_ | Concurrency is the number of concurrent parts to upload during multipart uploads.<br />Defaults to 3. Higher values may improve throughput on fast networks but increase<br />memory usage and may overwhelm slower storage backends. | 3 | Maximum: 10 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `region` _string_ | Region is the AWS region to use for S3-compatible clients.<br />For AWS, this should match the bucket region (for example, "eu-west-1").<br />For many S3-compatible stores (MinIO/Ceph), this can be any non-empty value.<br />Only used when Provider is "s3". | us-east-1 | Optional: \{\} <br /> |
| `roleArn` _string_ | RoleARN is the IAM role ARN (or S3-compatible equivalent) to assume via Web Identity.<br />When set, backup and restore Jobs mount a projected ServiceAccount token and set the<br />AWS Web Identity environment variables explicitly.<br />Only used when Provider is "s3".<br />Outside Hardened S3 targets, leave this empty when relying on ambient workload identity<br />or provider-managed default credentials instead. For Hardened S3 targets, roleArn is<br />one accepted explicit identity path. It does not satisfy Hardened identity requirements<br />for GCS or Azure. |  | Optional: \{\} <br /> |
| `usePathStyle` _boolean_ | UsePathStyle controls whether to use path-style addressing (bucket.s3.amazonaws.com/object)<br />or virtual-hosted-style addressing (bucket.s3.amazonaws.com/object).<br />Set to true for MinIO and S3-compatible stores that require path-style.<br />Set to false for AWS S3 (default, as AWS is deprecating path-style).<br />Only used when Provider is "s3". | false | Optional: \{\} <br /> |
| `gcs` _[GCSTargetConfig](#gcstargetconfig)_ | GCS contains Google Cloud Storage specific configuration.<br />Only used when Provider is "gcs". |  | Optional: \{\} <br /> |
| `azure` _[AzureTargetConfig](#azuretargetconfig)_ | Azure contains Azure Blob Storage specific configuration.<br />Only used when Provider is "azure". |  | Optional: \{\} <br /> |
| `insecureSkipVerify` _boolean_ | InsecureSkipVerify allows skipping TLS verification (useful for MinIO/LocalStack/Azurite with self-signed certs).<br />This applies to all providers that support TLS.<br />Hardened clusters reject insecureSkipVerify. |  | Optional: \{\} <br /> |


#### BlueGreenConfig



BlueGreenConfig configures the behavior when Type is BlueGreen.



_Appears in:_
- [UpgradeConfig](#upgradeconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `autoPromote` _boolean_ | AutoPromote controls whether newly started blue/green upgrades<br />automatically switch traffic and delete the old cluster after sync.<br />If false when an upgrade starts, that upgrade stays in the Syncing<br />phase waiting for an explicit promotion request via spec.upgrade.requests.promote.<br />Changing this field while an upgrade is already in progress affects only<br />future upgrades. | true |  |
| `verification` _[VerificationConfig](#verificationconfig)_ | VerificationConfig allows defining custom health checks before promotion. |  | Optional: \{\} <br /> |
| `maxJobFailures` _integer_ | MaxJobFailures is the maximum consecutive job failures before aborting/rolling back.<br />Defaults to 5 if not specified. | 5 | Minimum: 1 <br />Optional: \{\} <br /> |
| `preUpgradeSnapshot` _boolean_ | PreUpgradeSnapshot triggers a backup at the start of an upgrade.<br />Creates a recovery point before any changes are made.<br />Requires spec.backup to be configured. |  | Optional: \{\} <br /> |
| `autoRollback` _[AutoRollbackConfig](#autorollbackconfig)_ | AutoRollback configures automatic rollback behavior. |  | Optional: \{\} <br /> |


#### BlueGreenPhase

_Underlying type:_ _string_

BlueGreenPhase is a high-level summary of blue/green upgrade state.

_Validation:_
- Enum: [Idle DeployingGreen JoiningMesh Syncing Promoting DemotingBlue Cleanup RestoringReadReplicas RollingBack RollbackCleanup]

_Appears in:_
- [BlueGreenStatus](#bluegreenstatus)

| Field | Description |
| --- | --- |
| `Idle` | PhaseIdle indicates no blue/green upgrade is in progress.<br /> |
| `DeployingGreen` | PhaseDeployingGreen indicates the Green StatefulSet is being created and pods are becoming ready.<br />This phase includes waiting for pods to be unsealed.<br /> |
| `JoiningMesh` | PhaseJoiningMesh indicates Green pods are joining the Raft cluster as non-voters.<br /> |
| `Syncing` | PhaseSyncing indicates waiting for Green nodes to catch up with Blue nodes.<br /> |
| `Promoting` | PhasePromoting indicates Green nodes are being promoted to voters.<br /> |
| `DemotingBlue` | PhaseDemotingBlue indicates Blue nodes are being demoted to non-voters.<br /> |
| `Cleanup` | PhaseCleanup indicates Blue StatefulSet is being deleted.<br /> |
| `RestoringReadReplicas` | PhaseRestoringReadReplicas indicates the steady-state read-replica pool is<br />being restored after cutover cleanup and must converge before the upgrade<br />returns to Idle.<br /> |
| `RollingBack` | PhaseRollingBack indicates the upgrade is being rolled back.<br />Blue nodes are re-promoted and Green nodes are demoted.<br /> |
| `RollbackCleanup` | PhaseRollbackCleanup indicates Green StatefulSet is being deleted after rollback.<br /> |


#### BlueGreenStatus



BlueGreenStatus tracks the lifecycle of the "Green" revision during blue/green upgrades.



_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[BlueGreenPhase](#bluegreenphase)_ | Phase is the current phase of the blue/green upgrade. |  | Enum: [Idle DeployingGreen JoiningMesh Syncing Promoting DemotingBlue Cleanup RestoringReadReplicas RollingBack RollbackCleanup] <br /> |
| `operationID` _string_ | OperationID identifies the current blue/green upgrade attempt. |  | Optional: \{\} <br /> |
| `blueRevision` _string_ | BlueRevision is the hash/name of the currently active cluster. |  |  |
| `blueControllerRevision` _string_ | BlueControllerRevision is the Kubernetes StatefulSet controller revision<br />of Blue. It identifies an unrevisioned rolling workload after switching to<br />BlueGreen without requiring the existing Pods to be restarted or relabeled. |  | Optional: \{\} <br /> |
| `blueImage` _string_ | BlueImage is the container image used by the Blue cluster.<br />This ensures the Blue cluster is not actively upgraded when spec.image changes. |  |  |
| `blueReplicas` _integer_ | BlueReplicas is the voter population captured when the upgrade starts.<br />It remains fixed until Green becomes the stable workload. |  | Minimum: 1 <br />Optional: \{\} <br /> |
| `greenImage` _string_ | GreenImage is the image selected for the in-flight target. |  | Optional: \{\} <br /> |
| `greenVersion` _string_ | GreenVersion is the version selected for the in-flight target. |  | Optional: \{\} <br /> |
| `greenReplicas` _integer_ | GreenReplicas is the replica count selected for the in-flight target. |  | Minimum: 1 <br />Optional: \{\} <br /> |
| `greenRevision` _string_ | GreenRevision is the hash/name of the next cluster (if upgrade in progress). |  |  |
| `manualPromotionRequired` _boolean_ | ManualPromotionRequired snapshots whether the current in-flight blue/green<br />upgrade requires an explicit spec.upgrade.requests.promote request before<br />promotion can proceed. It is derived from spec.upgrade.blueGreen.autoPromote<br />when the upgrade starts. |  | Optional: \{\} <br /> |
| `startTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | StartTime is when the current phase began. |  |  |
| `jobFailureCount` _integer_ | JobFailureCount tracks consecutive job failures in the current phase.<br />Reset to 0 on phase transition or successful job completion. |  | Optional: \{\} <br /> |
| `lastJobFailure` _string_ | LastJobFailure records the name of the last failed job for debugging. |  | Optional: \{\} <br /> |
| `preUpgradeSnapshotJobName` _string_ | PreUpgradeSnapshotJobName is the name of the backup job triggered at upgrade start. |  | Optional: \{\} <br /> |
| `validationHook` _[BlueGreenValidationHookStatus](#bluegreenvalidationhookstatus)_ | ValidationHook records the expected identity and durable execution receipts<br />for the current pre-promotion validation hook. |  | Optional: \{\} <br /> |
| `rollbackReason` _string_ | RollbackReason records why a rollback was triggered (if any). |  | Optional: \{\} <br /> |
| `rollbackStartTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | RollbackStartTime is when the rollback was initiated. |  | Optional: \{\} <br /> |
| `rollbackAttempt` _integer_ | RollbackAttempt increments each time rollback automation is retried.<br />It is used to produce stable, deterministic Job names per attempt. |  | Optional: \{\} <br /> |


#### BlueGreenValidationHookResult

_Underlying type:_ _string_

BlueGreenValidationHookResult is the persisted terminal result of a
pre-promotion validation hook Job.

_Validation:_
- Enum: [Succeeded Failed]

_Appears in:_
- [BlueGreenValidationHookStatus](#bluegreenvalidationhookstatus)

| Field | Description |
| --- | --- |
| `Succeeded` | BlueGreenValidationHookResultSucceeded indicates that the validation hook<br />Job succeeded.<br /> |
| `Failed` | BlueGreenValidationHookResultFailed indicates that the validation hook Job<br />failed.<br /> |


#### BlueGreenValidationHookStage

_Underlying type:_ _string_

BlueGreenValidationHookStage identifies the durable execution boundary
reached by a pre-promotion validation hook.

_Validation:_
- Enum: [Prepared Committed Created TerminalObserved Unknown]

_Appears in:_
- [BlueGreenValidationHookStatus](#bluegreenvalidationhookstatus)

| Field | Description |
| --- | --- |
| `Prepared` | BlueGreenValidationHookStagePrepared indicates that the expected Job<br />identity is durable, but Job creation has not been committed.<br /> |
| `Committed` | BlueGreenValidationHookStageCommitted indicates that the controller<br />durably committed to one Job creation attempt. A missing Job after this<br />point is ambiguous and is not recreated automatically.<br /> |
| `Created` | BlueGreenValidationHookStageCreated indicates that the controller<br />persisted the created Job identity.<br /> |
| `TerminalObserved` | BlueGreenValidationHookStageTerminalObserved indicates that the controller<br />persisted the terminal Job result.<br /> |
| `Unknown` | BlueGreenValidationHookStageUnknown indicates that the controller cannot<br />prove whether the committed validation hook ran.<br /> |


#### BlueGreenValidationHookStatus



BlueGreenValidationHookStatus records the expected identity and durable
receipts for one pre-promotion validation hook execution.



_Appears in:_
- [BlueGreenStatus](#bluegreenstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `operationID` _string_ | OperationID identifies the blue/green upgrade attempt that owns this hook. |  |  |
| `greenRevision` _string_ | GreenRevision identifies the Green revision validated by this hook. |  |  |
| `specHash` _string_ | SpecHash identifies the normalized validation hook specification. |  |  |
| `stage` _[BlueGreenValidationHookStage](#bluegreenvalidationhookstage)_ | Stage is the latest durable execution boundary observed by the controller. |  | Enum: [Prepared Committed Created TerminalObserved Unknown] <br /> |
| `jobName` _string_ | JobName is the expected validation hook Job name. |  |  |
| `jobUID` _[UID](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#uid-types-pkg)_ | JobUID is the UID returned for the created validation hook Job. |  | Optional: \{\} <br /> |
| `preparedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | PreparedAt is when the expected hook identity became durable. |  | Optional: \{\} <br /> |
| `committedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | CommittedAt is when the controller committed to one Job creation attempt. |  | Optional: \{\} <br /> |
| `createdAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | CreatedAt is when the controller persisted the created Job receipt. |  | Optional: \{\} <br /> |
| `terminalResult` _[BlueGreenValidationHookResult](#bluegreenvalidationhookresult)_ | TerminalResult is the persisted terminal Job result. |  | Enum: [Succeeded Failed] <br />Optional: \{\} <br /> |
| `terminalObservedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | TerminalObservedAt is when the controller persisted the terminal Job result. |  | Optional: \{\} <br /> |


#### BreakGlassReason

_Underlying type:_ _string_

BreakGlassReason describes why the operator required manual intervention.

_Validation:_
- Enum: [RollbackConsensusRepairFailed RollbackCleanupPeerRemovalFailed]

_Appears in:_
- [BreakGlassStatus](#breakglassstatus)

| Field | Description |
| --- | --- |
| `RollbackConsensusRepairFailed` |  |
| `RollbackCleanupPeerRemovalFailed` |  |


#### BreakGlassStatus



BreakGlassStatus captures safe-mode / break-glass state and recovery guidance.



_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `active` _boolean_ | Active indicates whether break glass mode is currently active. |  | Optional: \{\} <br /> |
| `reason` _[BreakGlassReason](#breakglassreason)_ | Reason is a stable, typed reason for entering break glass mode. |  | Enum: [RollbackConsensusRepairFailed RollbackCleanupPeerRemovalFailed] <br />Optional: \{\} <br /> |
| `message` _string_ | Message provides a short summary of the detected unsafe state. |  | Optional: \{\} <br /> |
| `nonce` _string_ | Nonce is the acknowledgment token required to resume automation. |  | Optional: \{\} <br /> |
| `enteredAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | EnteredAt is when break glass mode became active. |  | Optional: \{\} <br /> |
| `steps` _string array_ | Steps provides deterministic recovery guidance. |  | Optional: \{\} <br /> |
| `acknowledgedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | AcknowledgedAt records when break glass was acknowledged. |  | Optional: \{\} <br /> |


#### ClusterOperation

_Underlying type:_ _string_

ClusterOperation identifies a mutually-exclusive operator operation.

_Validation:_
- Enum: [Upgrade Backup Restore]

_Appears in:_
- [OperationLockStatus](#operationlockstatus)

| Field | Description |
| --- | --- |
| `Upgrade` |  |
| `Backup` |  |
| `Restore` |  |


#### ClusterPhase

_Underlying type:_ _string_

ClusterPhase is a high-level summary of cluster state.

_Validation:_
- Enum: [Initializing Running Upgrading BackingUp Failed]

_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)

| Field | Description |
| --- | --- |
| `Initializing` |  |
| `Running` |  |
| `Upgrading` |  |
| `BackingUp` |  |
| `Failed` |  |


#### ClusterRestoreStatus



ClusterRestoreStatus retains restart observations from earlier operator releases.
New restores report status, including managed restarts, on OpenBaoRestore.



_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name identifies the OpenBaoRestore recorded by an earlier release. |  | Optional: \{\} <br /> |
| `uid` _string_ | UID identifies the OpenBaoRestore recorded by an earlier release. |  | Optional: \{\} <br /> |
| `restartCompletedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | RestartCompletedAt is the legacy timestamp for a controller-observed voter<br />restart. Administrator-led recovery does not update this field. |  | Optional: \{\} <br /> |




#### ControllerErrorStatus



ControllerErrorStatus captures a controller-scoped error signal that the status controller
can translate into high-level conditions.



_Appears in:_
- [AdminOpsControllerStatus](#adminopscontrollerstatus)
- [PolicyReconciliationStatus](#policyreconciliationstatus)
- [UpgradeProgress](#upgradeprogress)
- [WorkloadControllerStatus](#workloadcontrollerstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `reason` _string_ | Reason is a low-cardinality identifier for the error. |  | Optional: \{\} <br /> |
| `message` _string_ | Message is a human-readable error message (best-effort). |  | Optional: \{\} <br /> |
| `at` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | At is when the error was observed (best-effort). |  | Optional: \{\} <br /> |


#### ControllerJWTMode

_Underlying type:_ _string_

ControllerJWTMode selects shared or target-specific controller credentials.

_Validation:_
- Enum: [Shared Target]

_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description |
| --- | --- |
| `Shared` | ControllerJWTModeShared uses the installation's projected ServiceAccount JWT.<br /> |
| `Target` | ControllerJWTModeTarget requests a JWT for this OpenBaoCluster's UID.<br /> |


#### DeletionPolicy

_Underlying type:_ _string_

DeletionPolicy defines what happens to underlying resources when the CR is deleted.

_Validation:_
- Enum: [Retain DeletePVCs DeleteAll]

_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description |
| --- | --- |
| `Retain` | DeletionPolicyRetain removes operator-managed compute and keeps PVCs and external backups.<br /> |
| `DeletePVCs` | DeletionPolicyDeletePVCs removes operator-managed compute and PVCs, but retains external backups.<br /> |
| `DeleteAll` | DeletionPolicyDeleteAll removes operator-managed compute and PVCs, but retains external object-store backups.<br /> |


#### FileAuditOptions



FileAuditOptions configures options for file audit devices.
See: https://openbao.org/docs/audit/file/



_Appears in:_
- [AuditDevice](#auditdevice)
- [SelfInitAuditDevice](#selfinitauditdevice)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `filePath` _string_ | FilePath is the path to where the audit log will be written.<br />Special keywords: "stdout" writes to standard output, "discard" discards output. |  | MinLength: 1 <br /> |
| `mode` _string_ | Mode is a string containing an octal number representing the bit pattern for the file mode.<br />Defaults to "0600" if not specified. Set to "0000" to prevent OpenBao from modifying the file mode. |  | Optional: \{\} <br /> |


#### GCPCloudKMSSealConfig



GCPCloudKMSSealConfig configures the GCP Cloud KMS seal type.
See: https://openbao.org/docs/configuration/seal/gcpckms/



_Appears in:_
- [UnsealConfig](#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `project` _string_ | Project is the GCP project ID. |  | MinLength: 1 <br /> |
| `region` _string_ | Region is the GCP region where the key ring lives. |  | MinLength: 1 <br /> |
| `keyRing` _string_ | KeyRing is the name of the GCP KMS key ring. |  | MinLength: 1 <br /> |
| `cryptoKey` _string_ | CryptoKey is the name of the GCP KMS crypto key. |  | MinLength: 1 <br /> |
| `credentials` _string_ | Credentials is the path to the GCP credentials JSON file.<br />Note: It is strongly recommended to use CredentialsSecretRef or Workload Identity instead. |  | Optional: \{\} <br /> |


#### GCSTargetConfig



GCSTargetConfig holds Google Cloud Storage specific configuration.



_Appears in:_
- [BackupTarget](#backuptarget)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `project` _string_ | Project is the GCP project ID. Optional if using ADC with default project or<br />if the credentials JSON includes the project. |  | Optional: \{\} <br /> |


#### GatewayConfig



GatewayConfig configures Kubernetes Gateway API access for the OpenBao cluster.
This is an alternative to Ingress for external access, using the more modern
and expressive Gateway API.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled activates Gateway API support for this cluster.<br />When true, the Operator creates an HTTPRoute for the cluster. |  |  |
| `listenerName` _string_ | ListenerName optionally targets a specific listener (sectionName) on the referenced Gateway.<br />When set, the generated Route (HTTPRoute or TLSRoute) attaches only to that listener.<br />This is useful when a Gateway exposes multiple listeners for the same hostname (e.g. Traefik<br />"web" and "websecure") and you want deterministic attachment. |  | Optional: \{\} <br /> |
| `gatewayRef` _[GatewayReference](#gatewayreference)_ | GatewayRef references an existing Gateway resource that will handle<br />traffic for this OpenBao cluster. The Gateway must already exist. |  |  |
| `hostname` _string_ | Hostname for routing traffic to this OpenBao cluster.<br />This hostname will be automatically added to the TLS SANs. |  | MinLength: 1 <br /> |
| `path` _string_ | Path prefix for the HTTPRoute (defaults to "/"). |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations to apply to the HTTPRoute resource. |  | Optional: \{\} <br /> |
| `backendTLS` _[BackendTLSConfig](#backendtlsconfig)_ | BackendTLS configures BackendTLSPolicy for end-to-end TLS between the Gateway and OpenBao.<br />When enabled, the Operator creates a BackendTLSPolicy that configures the Gateway to use<br />HTTPS when communicating with the OpenBao backend service and validates the backend<br />certificate using the cluster's CA certificate. |  | Optional: \{\} <br /> |
| `tlsPassthrough` _boolean_ | TLSPassthrough enables TLS passthrough mode using TLSRoute instead of HTTPRoute.<br />When true, the Operator creates a TLSRoute that routes encrypted TLS traffic based on SNI<br />without terminating TLS at the Gateway. OpenBao terminates TLS directly.<br />When false (default), the Operator creates an HTTPRoute with TLS termination at the Gateway.<br />Note: TLSRoute and HTTPRoute are mutually exclusive - only one can be used per cluster.<br />BackendTLSPolicy is not needed when TLSPassthrough is enabled since the Gateway does not<br />decrypt traffic. The Gateway listener must be configured with protocol: TLS and mode: Passthrough. |  | Optional: \{\} <br /> |


#### GatewayReference



GatewayReference identifies a Gateway resource.



_Appears in:_
- [GatewayConfig](#gatewayconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Gateway resource. |  | MinLength: 1 <br /> |
| `namespace` _string_ | Namespace of the Gateway resource. If empty, uses the OpenBaoCluster namespace. |  | Optional: \{\} <br /> |


#### HTTPAuditOptions



HTTPAuditOptions configures options for HTTP audit devices.
See: https://openbao.org/docs/audit/http/



_Appears in:_
- [AuditDevice](#auditdevice)
- [SelfInitAuditDevice](#selfinitauditdevice)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `uri` _string_ | URI is the URI of the remote server where the audit logs will be written. |  | MinLength: 1 <br /> |
| `headers` _[JSON](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#json-v1-apiextensions-k8s-io)_ | Headers is a JSON object describing headers. Must take the shape map[string][]string,<br />i.e., an object of headers, with each having one or more values.<br />Headers without values will be ignored. The operator renders this object as OpenBao's<br />expected JSON-encoded options.headers string. |  | Optional: \{\} <br /> |


#### ImageVerificationConfig



ImageVerificationConfig configures supply chain security checks for container images.
When enabled, verification applies to all operator-managed images for this cluster (StatefulSets, Deployments, and Jobs).



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether image verification is enforced. |  |  |
| `publicKey` _string_ | PublicKey is the Cosign public key content used to verify the signature.<br />Required for static key verification. If empty, keyless verification will be used<br />(requires Issuer and Subject to be set). |  | Optional: \{\} <br /> |
| `issuer` _string_ | Issuer is the OIDC issuer for keyless verification (e.g., https://token.actions.githubusercontent.com).<br />Required for keyless verification when PublicKey is not provided.<br />For GitHub Actions keyless verification, use: https://token.actions.githubusercontent.com |  | Optional: \{\} <br /> |
| `subject` _string_ | Subject is the OIDC subject for keyless verification.<br />Required for keyless verification when PublicKey is not provided.<br />Example (GitHub Actions): `https://github.com/kubebao/openbao-operator/.github/workflows/release.yml@refs/tags/<VERSION>`<br />The version in the subject MUST match the image tag version. |  | Optional: \{\} <br /> |
| `issuerRegExp` _string_ | IssuerRegExp is a regular expression for the OIDC issuer when using keyless verification.<br />Use this to allow a controlled set of issuers instead of a single exact issuer string.<br />Requires SubjectRegExp when PublicKey is not provided. |  | Optional: \{\} <br /> |
| `subjectRegExp` _string_ | SubjectRegExp is a regular expression for the OIDC subject when using keyless verification.<br />Use this to allow a controlled set of workflow identities instead of a single exact subject.<br />Requires IssuerRegExp when PublicKey is not provided. |  | Optional: \{\} <br /> |
| `failurePolicy` _string_ | FailurePolicy defines behavior on verification failure.<br />"Block" blocks reconciliation of the affected workload when verification fails.<br />"Warn" logs an error and emits a Kubernetes Event but proceeds. | Block | Enum: [Warn Block] <br /> |
| `ignoreTlog` _boolean_ | IgnoreTlog controls whether to verify against the Rekor transparency log.<br />When false (default), signatures are verified against Rekor for non-repudiation.<br />When true, only signature verification is performed without transparency log checks. | false | Optional: \{\} <br /> |
| `imagePullSecrets` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core) array_ | ImagePullSecrets is a list of references to secrets in the same namespace<br />to use for pulling images from private registries during verification.<br />These secrets must be of type kubernetes.io/dockerconfigjson or kubernetes.io/dockercfg. |  | Optional: \{\} <br /> |


#### IngressConfig



IngressConfig controls optional HTTP(S) ingress in front of the OpenBao Service.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether the Operator manages an Ingress for external access. |  | Optional: \{\} <br /> |
| `className` _string_ | ClassName is an optional IngressClassName (for example, "nginx", "traefik"). |  | Optional: \{\} <br /> |
| `host` _string_ | Host is the primary host for external access, for example "bao.example.com". |  | MinLength: 1 <br /> |
| `path` _string_ | Path is the HTTP path to route to OpenBao, defaulting to "/". |  | Optional: \{\} <br /> |
| `pathType` _[IngressPathType](#ingresspathtype)_ | PathType identifies how the ingress controller should interpret Path. | Prefix | Enum: [Prefix Exact ImplementationSpecific] <br />Optional: \{\} <br /> |
| `tlsSecretName` _string_ | TLSSecretName is an optional TLS Secret name; when empty the cluster TLS Secret is used. |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations are additional annotations to apply to the Ingress. |  | Optional: \{\} <br /> |
| `readinessMode` _[IngressReadinessMode](#ingressreadinessmode)_ | ReadinessMode identifies when the operator should consider ingress<br />integration ready for endpoint publication. | LoadBalancerPublished | Enum: [Created LoadBalancerPublished] <br />Optional: \{\} <br /> |


#### IngressPathType

_Underlying type:_ _string_

IngressPathType identifies how a Kubernetes Ingress path should match requests.

_Validation:_
- Enum: [Prefix Exact ImplementationSpecific]

_Appears in:_
- [IngressConfig](#ingressconfig)

| Field | Description |
| --- | --- |
| `Prefix` | IngressPathTypePrefix uses prefix path matching.<br /> |
| `Exact` | IngressPathTypeExact uses exact path matching.<br /> |
| `ImplementationSpecific` | IngressPathTypeImplementationSpecific defers path matching to the controller.<br /> |


#### IngressReadinessMode

_Underlying type:_ _string_

IngressReadinessMode identifies how the operator decides whether ingress
integration is ready for endpoint publication.

_Validation:_
- Enum: [Created LoadBalancerPublished]

_Appears in:_
- [IngressConfig](#ingressconfig)

| Field | Description |
| --- | --- |
| `Created` | IngressReadinessModeCreated considers ingress integration ready once the<br />managed Ingress object exists.<br /> |
| `LoadBalancerPublished` | IngressReadinessModeLoadBalancerPublished considers ingress integration<br />ready only after the managed Ingress reports a published load balancer<br />address in status.<br /> |


#### InitContainerConfig



InitContainerConfig configures the init container used to render OpenBao configuration.
The init container is responsible for rendering the final config.hcl from a template
using environment variables such as HOSTNAME and POD_IP.

The operator relies on this init container to render config.hcl at runtime. Disabling
the init container is not supported and will be rejected by validation.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether the init container is used to render the configuration.<br />The operator requires the init container; disabling it is not supported. | true | Optional: \{\} <br /> |
| `image` _string_ | Image is the container image to use for the init container.<br />If not specified, OPERATOR_INIT_IMAGE can supply a complete image reference, including a digest.<br />Otherwise, defaults to "&lt;repo&gt;:X.Y.Z" where &lt;repo&gt; is derived from OPERATOR_INIT_IMAGE_REPOSITORY<br />(default: "ghcr.io/kubebao/openbao-init") and the tag matches OPERATOR_VERSION. |  | Optional: \{\} <br /> |


#### InitialRecoveryKeysConfig



InitialRecoveryKeysConfig declares the first recovery-key set that OpenBao
should create through the authenticated recovery-key rotation endpoint during
self-initialization.

The Operator always renders this request with backup=true so encrypted
recovery shares can be retrieved through OpenBao's recovery backup endpoint
after bootstrap. Decrypted recovery shares must stay outside Kubernetes and
outside the Operator.



_Appears in:_
- [RecoveryKeysConfig](#recoverykeysconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `shares` _integer_ | Shares is the total number of recovery-key shares to create. |  | Maximum: 255 <br />Minimum: 1 <br /> |
| `threshold` _integer_ | Threshold is the number of recovery-key shares required for recovery<br />operations such as generate-root. |  | Maximum: 255 <br />Minimum: 1 <br /> |
| `recipients` _[RecoveryKeyRecipient](#recoverykeyrecipient) array_ | Recipients lists the public OpenPGP recipients for encrypted recovery<br />shares. Each recipient is passed to OpenBao as one pgp_keys entry; use<br />fingerprints for custody mapping instead of relying on share numbering. |  | MaxItems: 255 <br />MinItems: 1 <br /> |


#### KMIPSealConfig



KMIPSealConfig configures the KMIP seal type.
See: https://openbao.org/docs/configuration/seal/kmip/



_Appears in:_
- [UnsealConfig](#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `endpoint` _string_ | Endpoint is the KMIP server endpoint. |  | MinLength: 1 <br /> |
| `kmsKeyID` _string_ | KMSKeyID is the unique identifier of the KMIP key to use. |  | MinLength: 1 <br /> |
| `clientCert` _string_ | ClientCert is the path to the client certificate used for KMIP communication. |  | MinLength: 1 <br /> |
| `clientKey` _string_ | ClientKey is the path to the private key used for KMIP communication. |  | MinLength: 1 <br /> |
| `caCert` _string_ | CACert is the path to the CA certificate for KMIP communication. |  | Optional: \{\} <br /> |
| `serverName` _string_ | ServerName is the TLS server name to use when connecting to the KMIP endpoint. |  | Optional: \{\} <br /> |
| `timeout` _integer_ | Timeout is the timeout in seconds for KMIP requests. |  | Minimum: 1 <br />Optional: \{\} <br /> |
| `encryptAlg` _string_ | EncryptAlg is the encryption algorithm used for KMIP requests. |  | Enum: [AES_GCM RSA_OAEP_SHA256 RSA_OAEP_SHA384 RSA_OAEP_SHA512] <br />Optional: \{\} <br /> |
| `tls12Ciphers` _string_ | TLS12Ciphers configures the TLS 1.2 cipher suites to use when connecting<br />to the KMIP endpoint. |  | Optional: \{\} <br /> |
| `disabled` _boolean_ | Disabled disables this seal configuration, for example during seal migration. |  | Optional: \{\} <br /> |


#### KMSPluginSealConfig



KMSPluginSealConfig configures a plugin-backed KMS seal.
The referenced plugin must be declared in spec.plugins with type "kms".



_Appears in:_
- [UnsealConfig](#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `pluginName` _string_ | PluginName is the name of the plugin registered through a matching<br />plugin "kms" stanza. OpenBao uses this value as the seal stanza label. |  | MinLength: 1 <br /> |
| `config` _object (keys:string, values:string)_ | Config contains plugin-specific seal configuration rendered as string<br />attributes inside seal "&lt;pluginName&gt;". Keys must be valid HCL identifiers.<br />Values are stored in the OpenBaoCluster resource; use file paths to<br />credentialsSecretRef-mounted files for sensitive material instead of inline<br />secrets. |  | MaxProperties: 64 <br />Optional: \{\} <br /> |


#### KubernetesServiceAccountSubject

_Underlying type:_ _string_

KubernetesServiceAccountSubject is the exact subject claim in a projected
Kubernetes ServiceAccount token.

_Validation:_
- MaxLength: 340
- Pattern: `^system:serviceaccount:[a-z0-9]([-a-z0-9]*[a-z0-9])?:[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`

_Appears in:_
- [SelfInitOIDCAdditionalSubjects](#selfinitoidcadditionalsubjects)



#### ListenerConfig



ListenerConfig allows tuning the API and metrics TCP listeners.



_Appears in:_
- [OpenBaoConfiguration](#openbaoconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `tlsDisable` _boolean_ | TLSDisable controls TLS on the listener.<br />Note: This is typically managed by the operator based on spec.tls.enabled.<br />Hardened clusters reject tlsDisable=true. |  | Optional: \{\} <br /> |
| `tlsMinVersion` _[TLSVersion](#tlsversion)_ | TLSMinVersion sets the minimum TLS version on the API and metrics listeners.<br />If omitted, OpenBao uses TLS 1.2. TLS must be enabled. |  | Enum: [tls12 tls13] <br />Optional: \{\} <br /> |
| `tlsMaxVersion` _[TLSVersion](#tlsversion)_ | TLSMaxVersion sets the maximum TLS version on the API and metrics listeners.<br />If omitted, OpenBao uses TLS 1.3. TLS must be enabled. |  | Enum: [tls12 tls13] <br />Optional: \{\} <br /> |
| `tlsKeyExchangePreferences` _[TLSKeyExchangeGroup](#tlskeyexchangegroup) array_ | TLSKeyExchangePreferences selects allowed key exchange groups on the API<br />and metrics listeners. List order does not control preference; Go uses its<br />internal preference order. If omitted, OpenBao uses Go's default groups.<br />Requires OpenBao >= 2.7.0 and TLS enabled. A list containing only hybrid PQ<br />groups requires TLSMinVersion=tls13 and rejects classical-only clients.<br />This setting does not configure cluster traffic on port 8201. |  | Enum: [CurveP256 CurveP384 CurveP521 X25519 X25519MLKEM768 SecP256r1MLKEM768 SecP384r1MLKEM1024] <br />MaxItems: 7 <br />MinItems: 1 <br />Optional: \{\} <br /> |
| `proxyProtocolBehavior` _string_ | ProxyProtocolBehavior configures PROXY protocol on the API listener.<br />Supported values are use_always, allow_authorized, and deny_unauthorized.<br />The legacy allow_any value remains in the schema for compatibility but is<br />rejected by the operator. Use allow_authorized to trust selected proxies. |  | Enum: [use_always allow_any allow_authorized deny_unauthorized] <br />Optional: \{\} <br /> |
| `proxyProtocolAuthorizedAddrs` _string array_ | ProxyProtocolAuthorizedAddrs lists trusted proxy source IP addresses or CIDRs.<br />Required for allow_authorized and deny_unauthorized. With allow_authorized,<br />connections from other sources use the peer IP address instead of PROXY metadata.<br />This setting applies only to the API listener, not the metrics listener. |  | MinItems: 1 <br />items:MinLength: 1 <br />Optional: \{\} <br /> |


#### LoggingConfig



LoggingConfig allows configuring logging behavior for OpenBao.



_Appears in:_
- [OpenBaoConfiguration](#openbaoconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `format` _string_ | Format specifies the log format. |  | Enum: [standard json] <br />Optional: \{\} <br /> |
| `file` _string_ | File is the path to the log file.<br />If not specified, logs are written to stderr. |  | Optional: \{\} <br /> |
| `rotateDuration` _string_ | RotateDuration specifies how often to rotate logs (e.g., "24h", "7d"). |  | Optional: \{\} <br /> |
| `rotateBytes` _integer_ | RotateBytes specifies the maximum size in bytes before rotating logs. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `rotateMaxFiles` _integer_ | RotateMaxFiles is the maximum number of rotated log files to keep. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `pidFile` _string_ | PIDFile is the path to write the PID file. |  | Optional: \{\} <br /> |


#### MaintenanceConfig



MaintenanceConfig defines supported maintenance operations.
This is intended to provide a first-class workflow for day-2 operations in
clusters that enforce managed-resource mutation locks via admission policy.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled enables maintenance mode for this cluster.<br />When true, the operator annotates managed resources (Pods/StatefulSet) with<br />`openbao.org/maintenance=true` to allow controlled restarts/deletes where<br />admission policies require an explicit maintenance signal. |  | Optional: \{\} <br /> |


#### MetricsConfig



MetricsConfig configures metrics collection.



_Appears in:_
- [ObservabilityConfig](#observabilityconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled configures the OpenBao telemetry stanza and creates a ServiceMonitor. | false |  |
| `scrapeProfile` _string_ | ScrapeProfile selects which OpenBao pods are targeted by generated scrape resources.<br />Active targets only the active OpenBao pod. AllNodes targets every OpenBao pod and<br />requires a dedicated metrics-only listener. | Active | Enum: [Active AllNodes] <br />Optional: \{\} <br /> |
| `metricsOnlyListener` _[MetricsOnlyListenerConfig](#metricsonlylistenerconfig)_ | MetricsOnlyListener configures a dedicated listener for metrics scraping.<br />It is enabled automatically when scrapeProfile is AllNodes. |  | Optional: \{\} <br /> |
| `serviceMonitor` _[ServiceMonitorConfig](#servicemonitorconfig)_ | ServiceMonitor controls whether to create a Prometheus Operator ServiceMonitor. |  | Optional: \{\} <br /> |


#### MetricsOnlyListenerConfig



MetricsOnlyListenerConfig configures a dedicated metrics-only TCP listener.



_Appears in:_
- [MetricsConfig](#metricsconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether to render the dedicated metrics-only listener.<br />When omitted, the listener is enabled automatically for the AllNodes scrape profile. |  | Optional: \{\} <br /> |
| `port` _integer_ | Port is the dedicated metrics listener port. | 8202 | Maximum: 65535 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `unauthenticatedMetricsAccess` _boolean_ | UnauthenticatedMetricsAccess allows unauthenticated access to /v1/sys/metrics<br />on the metrics-only listener. AllNodes scraping needs this so standby nodes can<br />expose metrics. Restrict this listener with NetworkPolicy. |  | Optional: \{\} <br /> |


#### NetworkConfig



NetworkConfig configures network-related settings for the OpenBaoCluster.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiServerCIDR` _string_ | APIServerCIDR is an optional CIDR block for the Kubernetes API server.<br />When specified, this value is used instead of auto-detection for NetworkPolicy egress rules.<br />This is useful when you want an explicit allow-list (or when the in-cluster service VIP<br />injected into pods is unavailable/unusable in your environment).<br />Example: "10.43.0.0/16" for service network or "192.168.1.0/24" for control plane nodes. |  | Optional: \{\} <br /> |
| `apiServerEndpointIPs` _string array_ | APIServerEndpointIPs is an optional list of Kubernetes API server endpoint IPs.<br />When set, the operator adds least-privilege NetworkPolicy egress rules for these IPs on port 6443.<br />This is required on some CNI implementations where egress enforcement happens on the post-NAT<br />destination (the API server endpoint) rather than the kubernetes Service IP (10.43.0.1:443).<br />The operator does not auto-detect these endpoint IPs because doing so reliably requires broader<br />cluster permissions (list/watch). Configure this field explicitly when needed.<br />Example (k3d): ["192.168.166.2"] |  | Optional: \{\} <br /> |
| `dnsNamespace` _string_ | DNSNamespace specifies the namespace where the cluster DNS service resides.<br />Defaults to "kube-system" if not specified. | kube-system | Optional: \{\} <br /> |
| `dnsEndpointIPs` _string array_ | DNSEndpointIPs is an optional list of DNS resolver endpoint IPs that should be<br />allow-listed directly in the operator-managed NetworkPolicy on TCP/UDP port 53.<br />Use this for clusters that resolve DNS through node-local or host-networked caches<br />instead of pod-backed DNS Services in a namespace. These IP-based rules are additive<br />to the namespace-based allow-list controlled by DNSNamespace.<br />The operator does not auto-detect these endpoint IPs because doing so reliably would<br />require environment-specific node or DNS discovery logic outside the current trust model.<br />Example: ["169.254.20.10"] |  | Optional: \{\} <br /> |
| `egressRules` _[NetworkPolicyEgressRule](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#networkpolicyegressrule-v1-networking) array_ | EgressRules allows users to specify additional egress rules that will be merged into<br />the operator-managed NetworkPolicy. This is useful for allowing access to external<br />services such as transit seal backends, object storage endpoints, or other dependencies.<br />The operator's default egress rules (DNS, API server, cluster pods) are always included<br />and cannot be overridden. User-provided rules are appended to the operator-managed rules.<br />Hardened clusters require every user-provided egress rule to be port-scoped and to target<br />explicit non-wildcard peers.<br />Example: Allow egress to a transit seal backend in another namespace:<br />  egressRules:<br />  - to:<br />    - namespaceSelector:<br />        matchLabels:<br />          kubernetes.io/metadata.name: transit-namespace<br />    ports:<br />    - protocol: TCP<br />      port: 8200 |  | Optional: \{\} <br /> |
| `ingressRules` _[NetworkPolicyIngressRule](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#networkpolicyingressrule-v1-networking) array_ | IngressRules allows users to specify additional ingress rules that will be merged into<br />the operator-managed NetworkPolicy. This is useful for allowing access from external<br />services, monitoring tools, or other components that need to reach OpenBao pods.<br />The operator's default ingress rules (cluster pods and operator-managed jobs)<br />are always included and cannot be overridden. User-provided rules are appended to<br />the operator-managed rules. Gateway or ingress-controller data-plane reachability<br />should be modeled with trustedIngressPeers.<br />Hardened clusters reject raw ingress rules; use trustedIngressPeers or managed<br />Gateway/Ingress integration for application access.<br />Example: Allow ingress from a monitoring namespace:<br />  ingressRules:<br />  - from:<br />    - namespaceSelector:<br />        matchLabels:<br />          kubernetes.io/metadata.name: monitoring<br />    ports:<br />    - protocol: TCP<br />      port: 8200 |  | Optional: \{\} <br /> |
| `trustedIngressPeers` _[NetworkPolicyPeer](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#networkpolicypeer-v1-networking) array_ | TrustedIngressPeers allows users to declare ingress-controller or passthrough-proxy peers<br />that should be allowed to reach OpenBao on the API port without writing full raw<br />NetworkPolicy ingress rules.<br />This is useful for user-managed TCP passthrough or external ingress components that the<br />operator does not manage directly. The operator adds least-privilege ingress rules for<br />port 8200 using these peers.<br />Hardened clusters require trusted ingress peers to select explicit non-wildcard sources.<br />Example: Allow a Traefik namespace to reach OpenBao on port 8200:<br />  trustedIngressPeers:<br />  - namespaceSelector:<br />      matchLabels:<br />        kubernetes.io/metadata.name: traefik<br />Example: Allow only specific ingress-controller pods in another namespace:<br />  trustedIngressPeers:<br />  - namespaceSelector:<br />      matchLabels:<br />        kubernetes.io/metadata.name: ingress-system<br />    podSelector:<br />      matchLabels:<br />        app.kubernetes.io/name: traefik |  | Optional: \{\} <br /> |


#### OCIKMSSealConfig



OCIKMSSealConfig configures the OCI KMS seal type.
See: https://openbao.org/docs/configuration/seal/ocikms/



_Appears in:_
- [UnsealConfig](#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `keyID` _string_ | KeyID is the OCID of the master encryption key. |  | MinLength: 1 <br /> |
| `cryptoEndpoint` _string_ | CryptoEndpoint is the OCI KMS crypto endpoint. |  | MinLength: 1 <br /> |
| `managementEndpoint` _string_ | ManagementEndpoint is the OCI KMS management endpoint. |  | MinLength: 1 <br /> |
| `authTypeAPIKey` _boolean_ | AuthTypeAPIKey enables OCI API key authentication through an OCI SDK config file.<br />When false or omitted, OpenBao uses the default OCI principal flow for the runtime<br />environment, such as instance principal. |  | Optional: \{\} <br /> |
| `disabled` _boolean_ | Disabled disables this seal configuration, for example during seal migration. |  | Optional: \{\} <br /> |


#### ObservabilityConfig



ObservabilityConfig configures observability features.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `metrics` _[MetricsConfig](#metricsconfig)_ | Metrics configures integration with Prometheus/OpenMetrics. |  | Optional: \{\} <br /> |


#### OpenBaoCluster



OpenBaoCluster is the Schema for the openbaoclusters API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `openbao.org/v1alpha1` | | |
| `kind` _string_ | `OpenBaoCluster` | | |
| `spec` _[OpenBaoClusterSpec](#openbaoclusterspec)_ | Spec defines the desired state of OpenBaoCluster. |  |  |
| `status` _[OpenBaoClusterStatus](#openbaoclusterstatus)_ | Status defines the observed state of OpenBaoCluster. |  | Optional: \{\} <br /> |


#### OpenBaoClusterSpec



OpenBaoClusterSpec defines the desired state of an OpenBaoCluster.
The Operator owns certain protected OpenBao configuration stanzas (for example,
listener "tcp", storage "raft", and seal "static" when using default unseal).
Users must not override these via spec.configuration.



_Appears in:_
- [OpenBaoCluster](#openbaocluster)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version is the semantic OpenBao version, used for upgrade orchestration.<br />The Operator uses static auto-unseal, which requires OpenBao v2.4.0 or later.<br />Versions below 2.4.0 do not support the static seal feature and will fail to start. |  | MinLength: 1 <br />Pattern: `^v?(0\|[1-9][0-9]*)\.(0\|[1-9][0-9]*)\.(0\|[1-9][0-9]*)(-(0\|[1-9][0-9]*\|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0\|[1-9][0-9]*\|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$` <br /> |
| `image` _string_ | Image is the container image to run; defaults may be derived from Version. |  | Optional: \{\} <br /> |
| `serviceAccount` _[ServiceAccountConfig](#serviceaccountconfig)_ | ServiceAccount configures the Kubernetes ServiceAccount used by the OpenBao Pods. |  | Optional: \{\} <br /> |
| `podMetadata` _[PodMetadataConfig](#podmetadataconfig)_ | PodMetadata configures additional labels and annotations for the OpenBao Pod template.<br />This is useful for platform integrations that select Pods via metadata, such as<br />Azure Workload Identity. Operator-managed Pod metadata takes precedence. |  | Optional: \{\} <br /> |
| `imagePullSecrets` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core) array_ | ImagePullSecrets is a list of references to secrets in the same namespace<br />to use for pulling any images used by this Cluster (server, init, sidecars). |  | Optional: \{\} <br /> |
| `observability` _[ObservabilityConfig](#observabilityconfig)_ | Observability configures telemetry and metrics integration. |  | Optional: \{\} <br /> |
| `replicas` _integer_ | Replicas is the desired number of quorum-carrying voter Pods. | 3 | Minimum: 1 <br /> |
| `resources` _[ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#resourcerequirements-v1-core)_ | Resources defines resource requests and limits for voter OpenBao containers.<br />Read replicas use spec.readReplicas.template.resources instead. |  | Optional: \{\} <br /> |
| `readReplicas` _[ReadReplicaConfig](#readreplicaconfig)_ | ReadReplicas configures the steady-state non-voter read-replica pool. |  | Optional: \{\} <br /> |
| `paused` _boolean_ | Paused, when true, pauses reconciliation for this OpenBaoCluster (except delete and finalizers). |  | Optional: \{\} <br /> |
| `maintenance` _[MaintenanceConfig](#maintenanceconfig)_ | Maintenance configures supported maintenance workflows. |  | Optional: \{\} <br /> |
| `runtime` _[RuntimeConfig](#runtimeconfig)_ | Runtime configures explicit runtime control requests for the OpenBao workload. |  | Optional: \{\} <br /> |
| `breakGlassAck` _string_ | BreakGlassAck is an explicit acknowledgment token used to exit Break Glass / Safe Mode.<br />When the operator enters break glass mode, it writes a nonce to status.breakGlass.nonce.<br />To acknowledge and allow the operator to resume quorum-risk automation, set this field<br />to match that nonce.<br />Example:<br />  kubectl -n &lt;ns&gt; patch openbaocluster &lt;name&gt; --type merge -p '\{"spec":\{"breakGlassAck":"&lt;nonce&gt;"\}\}' |  | Optional: \{\} <br /> |
| `tls` _[TLSConfig](#tlsconfig)_ | TLS configures TLS for the cluster. |  |  |
| `storage` _[StorageConfig](#storageconfig)_ | Storage configures persistent storage for the cluster. |  |  |
| `service` _[ServiceConfig](#serviceconfig)_ | Service configures the primary Service used to expose OpenBao inside or outside the cluster. |  | Optional: \{\} <br /> |
| `ingress` _[IngressConfig](#ingressconfig)_ | Ingress configures optional HTTP(S) ingress in front of the OpenBao Service. |  | Optional: \{\} <br /> |
| `configuration` _[OpenBaoConfiguration](#openbaoconfiguration)_ | Configuration defines the server configuration. |  | Optional: \{\} <br /> |
| `backup` _[BackupSchedule](#backupschedule)_ | Backup configures scheduled backups for the cluster. |  | Optional: \{\} <br /> |
| `restore` _[RestoreConfig](#restoreconfig)_ | Restore configures optional restore authentication bootstrap for the cluster. |  | Optional: \{\} <br /> |
| `deletionPolicy` _[DeletionPolicy](#deletionpolicy)_ | DeletionPolicy controls what happens to underlying resources when the CR is deleted. |  | Enum: [Retain DeletePVCs DeleteAll] <br />Optional: \{\} <br /> |
| `reconcilePolicies` _boolean_ | ReconcilePolicies restores the built-in operational policies after initialization.<br />Administrator-managed JWT authentication and exact-content approval are required.<br />OpenBao must independently approve their exact contents through the<br />openbao-operator-policy-approval policy. Bootstrap installs the initial approval;<br />existing clusters and later permission changes require administrator approval.<br />Auth methods, roles, and the approval policy are not reconciled. |  | Optional: \{\} <br /> |
| `controllerJWTMode` _[ControllerJWTMode](#controllerjwtmode)_ | ControllerJWTMode selects the credential used by the controller for OpenBao.<br />Shared (also the behavior when omitted) preserves the installation-wide<br />projected JWT. Target requests a short-lived JWT whose sole audience is<br />urn:openbao:controller:&lt;metadata.uid&gt;. Prepare the OpenBao controller role<br />before switching an initialized cluster to Target. Target never falls back<br />to the shared JWT. Target requires self-init and self-init OIDC to be enabled.<br />Executor Job authentication is unchanged.<br />Enforced admission prevents removal or downgrade after selecting Target. |  | Enum: [Shared Target] <br />Optional: \{\} <br /> |
| `selfInit` _[SelfInitConfig](#selfinitconfig)_ | SelfInit configures OpenBao's native self-initialization feature.<br />When enabled, OpenBao initializes itself on first start using the configured<br />requests, and the root token is automatically revoked.<br />See: https://openbao.org/docs/configuration/self-init/ |  | Optional: \{\} <br /> |
| `recoveryKeys` _[RecoveryKeysConfig](#recoverykeysconfig)_ | RecoveryKeys configures Operator-assisted recovery-key bootstrap surfaces.<br />The Operator creates recovery keys only during initial self-initialization;<br />recovery share custody and proof ceremonies remain user-owned processes. |  | Optional: \{\} <br /> |
| `gateway` _[GatewayConfig](#gatewayconfig)_ | Gateway configures Kubernetes Gateway API access (alternative to Ingress).<br />When enabled, the Operator creates an HTTPRoute that routes traffic through<br />a user-managed Gateway resource. |  | Optional: \{\} <br /> |
| `network` _[NetworkConfig](#networkconfig)_ | Network configures network-related settings for the cluster. |  | Optional: \{\} <br /> |
| `initContainer` _[InitContainerConfig](#initcontainerconfig)_ | InitContainer configures the init container used to render OpenBao configuration.<br />The init container renders the final config.hcl from a template using environment<br />variables such as HOSTNAME and POD_IP. |  | Optional: \{\} <br /> |
| `audit` _[AuditDevice](#auditdevice) array_ | Audit configures declarative audit devices for the OpenBao cluster.<br />See: https://openbao.org/docs/configuration/audit/ |  | Optional: \{\} <br /> |
| `auditFileStorage` _[AuditFileStorageConfig](#auditfilestorageconfig)_ | AuditFileStorage configures a shared filesystem integration point for file audit devices.<br />When configured, file audit device paths must be under auditFileStorage.mountPath. |  | Optional: \{\} <br /> |
| `plugins` _[Plugin](#plugin) array_ | Plugins configures declarative plugins for the OpenBao cluster.<br />See: https://openbao.org/docs/configuration/plugins/ |  | Optional: \{\} <br /> |
| `telemetry` _[TelemetryConfig](#telemetryconfig)_ | Telemetry configures telemetry reporting for the OpenBao cluster.<br />See: https://openbao.org/docs/configuration/telemetry/ |  | Optional: \{\} <br /> |
| `upgrade` _[UpgradeConfig](#upgradeconfig)_ | Upgrade configures upgrade operations.<br />Built-in upgrade executor Jobs authenticate with JWT auth using the<br />upgrade ServiceAccount (&lt;cluster-name&gt;-upgrade-serviceaccount). If<br />spec.selfInit.oidc.enabled is true during initial SelfInit bootstrap and<br />spec.upgrade.jwtAuthRole is empty, the operator creates the default<br />"openbao-operator-upgrade" role. Already-initialized clusters must keep<br />that role or configure spec.upgrade.jwtAuthRole explicitly.<br />Pre-upgrade snapshots use spec.backup configuration and backup<br />authentication rather than spec.upgrade credentials. |  | Optional: \{\} <br /> |
| `unseal` _[UnsealConfig](#unsealconfig)_ | Unseal defines the auto-unseal configuration.<br />If omitted, defaults to "static" mode managed by the operator. |  | Optional: \{\} <br /> |
| `imageVerification` _[ImageVerificationConfig](#imageverificationconfig)_ | ImageVerification configures supply chain security checks. |  | Optional: \{\} <br /> |
| `operatorImageVerification` _[ImageVerificationConfig](#imageverificationconfig)_ | OperatorImageVerification configures supply chain security checks for operator-managed helper images<br />(init container and backup/upgrade/restore executors) and custom BlueGreen validation-hook images.<br />Helper images are typically signed by the operator project (e.g., kubebao/openbao-operator)<br />rather than the OpenBao upstream project.<br />If omitted, helper image verification does not fall back to ImageVerification.<br />In Development, omitted means disabled. In Hardened, omitted means enabled. |  | Optional: \{\} <br /> |
| `workloadHardening` _[WorkloadHardeningConfig](#workloadhardeningconfig)_ | WorkloadHardening configures opt-in workload hardening features. |  | Optional: \{\} <br /> |
| `securityContext` _[PodSecurityContext](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#podsecuritycontext-v1-core)_ | SecurityContext allows specifying the PodSecurityContext for the OpenBao Pods.<br />If set, these values override the default security context generated by the operator.<br />This is useful for OpenShift (SCC) compatibility or custom security requirements. |  | Optional: \{\} <br /> |
| `profile` _[Profile](#profile)_ | Profile defines the security posture for this cluster.<br />When set to "Hardened", the operator enforces strict security requirements:<br />- TLS must use External or ACME trust, with no TLS disablement or skip-verify paths<br />- Unseal must use external KMS (no static unseal)<br />- SelfInit must be enabled (no root token)<br />- Network additions must be explicit and least-privilege<br />- Backup/restore storage identity must be explicit<br />- Dangerous runtime flags and backend HTTP are rejected<br />When set to "Development", relaxed security is allowed but a security warning<br />condition is set. |  | Enum: [Hardened Development] <br /> |


#### OpenBaoClusterStatus



DriftStatus tracks drift detection and correction events for a cluster.
OpenBaoClusterStatus defines the observed state of an OpenBaoCluster.



_Appears in:_
- [OpenBaoCluster](#openbaocluster)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent metadata.generation that has been<br />reconciled into this status. |  | Optional: \{\} <br /> |
| `phase` _[ClusterPhase](#clusterphase)_ | Phase is a high-level summary of the cluster state. |  | Enum: [Initializing Running Upgrading BackingUp Failed] <br />Optional: \{\} <br /> |
| `activeLeader` _string_ | ActiveLeader is the current Raft leader pod name, for example "prod-cluster-0". |  | Optional: \{\} <br /> |
| `readyReplicas` _integer_ | ReadyReplicas is the number of replicas that are currently Ready. |  | Optional: \{\} <br /> |
| `readReplicas` _[ReadReplicaStatus](#readreplicastatus)_ | ReadReplicas captures observed state for the read-replica pool. |  | Optional: \{\} <br /> |
| `currentVersion` _string_ | CurrentVersion is the OpenBao version currently running on the cluster. |  | Optional: \{\} <br /> |
| `acceptedUpgradeStrategy` _[UpdateStrategyType](#updatestrategytype)_ | AcceptedUpgradeStrategy is the upgrade strategy the operator has accepted<br />after applying idle-state transition guards. While a requested strategy<br />change is blocked, controllers continue using this strategy so an existing<br />operation can finish safely. |  | Enum: [RollingUpdate BlueGreen] <br />Optional: \{\} <br /> |
| `initialized` _boolean_ | Initialized indicates whether the OpenBao cluster has been initialized.<br />This is set to true after the first pod is initialized using bao operator init<br />or after self-initialization completes. |  | Optional: \{\} <br /> |
| `selfInitialized` _boolean_ | SelfInitialized indicates whether the cluster was initialized using<br />OpenBao's self-initialization feature. When true, no root token Secret<br />exists for this cluster (the root token was auto-revoked). |  | Optional: \{\} <br /> |
| `upgrade` _[UpgradeProgress](#upgradeprogress)_ | Upgrade tracks the state of an in-progress upgrade (if any).<br />When non-nil, an upgrade is in progress and the UpgradeManager is orchestrating<br />the pod-by-pod rolling update with leader step-down. |  | Optional: \{\} <br /> |
| `upgradeRequests` _[UpgradeRequestStatus](#upgraderequeststatus)_ | UpgradeRequests tracks which explicit upgrade request values have already<br />been handled so one-shot requests are edge-triggered instead of level-triggered. |  | Optional: \{\} <br /> |
| `backup` _[BackupStatus](#backupstatus)_ | Backup tracks the state of backups for this cluster. |  | Optional: \{\} <br /> |
| `restore` _[ClusterRestoreStatus](#clusterrestorestatus)_ | Restore retains post-restore restart status written by earlier releases.<br />It is deprecated: new restores report status on OpenBaoRestore, and the<br />operator reads this field only to preserve legacy Pod templates. |  | Optional: \{\} <br /> |
| `blueGreen` _[BlueGreenStatus](#bluegreenstatus)_ | BlueGreen tracks the state of blue/green upgrades (if enabled). |  | Optional: \{\} <br /> |
| `operationLock` _[OperationLockStatus](#operationlockstatus)_ | OperationLock prevents concurrent long-running operations (upgrade/backup/restore)<br />from acting on the same cluster at the same time. |  | Optional: \{\} <br /> |
| `breakGlass` _[BreakGlassStatus](#breakglassstatus)_ | BreakGlass records when the operator has halted quorum-risk automation and requires<br />explicit operator acknowledgment to continue. |  | Optional: \{\} <br /> |
| `workload` _[WorkloadControllerStatus](#workloadcontrollerstatus)_ | Workload holds signals owned by the workload controller (infrastructure reconciliation). |  | Optional: \{\} <br /> |
| `adminOps` _[AdminOpsControllerStatus](#adminopscontrollerstatus)_ | AdminOps holds signals owned by the adminops controller (upgrade + backup). |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions represent the current state of the OpenBaoCluster resource. |  | Optional: \{\} <br /> |


#### OpenBaoConfiguration



OpenBaoConfiguration defines the server configuration for OpenBao.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ui` _boolean_ | UI enables the built-in web interface. | true | Optional: \{\} <br /> |
| `logLevel` _string_ | LogLevel specifies the log level. | info | Enum: [trace debug info warn err] <br />Optional: \{\} <br /> |
| `listener` _[ListenerConfig](#listenerconfig)_ | Listener allows tuning the TCP listener.<br />Note: Address and ClusterAddress are managed by the operator and cannot be changed. |  | Optional: \{\} <br /> |
| `raft` _[RaftConfig](#raftconfig)_ | Raft allows tuning the Raft storage backend. |  | Optional: \{\} <br /> |
| `acmeCARoot` _string_ | ACMECARoot is the path to the ACME CA root certificate file.<br />This is used when TLS mode is ACME to specify a custom CA root for ACME certificate validation. |  | Optional: \{\} <br /> |
| `logging` _[LoggingConfig](#loggingconfig)_ | Logging allows configuring logging behavior. |  | Optional: \{\} <br /> |
| `plugin` _[PluginConfig](#pluginconfig)_ | Plugin allows configuring plugin behavior.<br />Note: This is separate from spec.plugins which defines plugin instances. |  | Optional: \{\} <br /> |
| `defaultLeaseTTL` _string_ | DefaultLeaseTTL is the default lease TTL for tokens and secrets (e.g., "720h", "30m").<br />If not specified, OpenBao uses its default. |  | Optional: \{\} <br /> |
| `maxLeaseTTL` _string_ | MaxLeaseTTL is the maximum lease TTL for tokens and secrets (e.g., "8760h", "1y").<br />This must be greater than or equal to DefaultLeaseTTL.<br />If not specified, OpenBao uses its default. |  | Optional: \{\} <br /> |
| `cacheSize` _integer_ | CacheSize is the size of the cache in bytes.<br />If not specified, OpenBao uses its default cache size. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `disableCache` _boolean_ | DisableCache disables the cache entirely.<br />When true, all caching is disabled. |  | Optional: \{\} <br /> |
| `detectDeadlocks` _boolean_ | DetectDeadlocks enables deadlock detection in OpenBao.<br />This is an experimental feature for debugging.<br />Hardened clusters reject detectDeadlocks=true. |  | Optional: \{\} <br /> |
| `rawStorageEndpoint` _boolean_ | RawStorageEndpoint enables the raw storage endpoint.<br />This is an experimental feature that exposes raw storage operations.<br />Hardened clusters reject rawStorageEndpoint=true. |  | Optional: \{\} <br /> |
| `introspectionEndpoint` _boolean_ | IntrospectionEndpoint enables the introspection endpoint.<br />This is an experimental feature for debugging and introspection.<br />Hardened clusters reject introspectionEndpoint=true. |  | Optional: \{\} <br /> |
| `impreciseLeaseRoleTracking` _boolean_ | ImpreciseLeaseRoleTracking enables imprecise lease role tracking.<br />This is an experimental feature that may improve performance in some scenarios. |  | Optional: \{\} <br /> |
| `unsafeAllowAPIAuditCreation` _boolean_ | UnsafeAllowAPIAuditCreation allows API-based audit device creation.<br />This bypasses the normal audit device configuration validation.<br />Use with caution.<br />Hardened clusters reject unsafeAllowAPIAuditCreation=true. |  | Optional: \{\} <br /> |
| `allowAuditLogPrefixing` _boolean_ | AllowAuditLogPrefixing allows audit log prefixing.<br />This enables custom prefixes in audit log entries. |  | Optional: \{\} <br /> |
| `enableResponseHeaderHostname` _boolean_ | EnableResponseHeaderHostname enables the hostname in response headers.<br />When true, OpenBao includes the hostname in HTTP response headers. |  | Optional: \{\} <br /> |
| `enableResponseHeaderRaftNodeID` _boolean_ | EnableResponseHeaderRaftNodeID enables the Raft node ID in response headers.<br />When true, OpenBao includes the Raft node ID in HTTP response headers. |  | Optional: \{\} <br /> |


#### OperationLockStatus



OperationLockStatus represents a status-based lock held by the operator.



_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `operation` _[ClusterOperation](#clusteroperation)_ | Operation is the operation currently holding the lock. |  | Enum: [Upgrade Backup Restore] <br />Optional: \{\} <br /> |
| `holder` _string_ | Holder is a stable identifier for the lock holder (controller/component). |  | Optional: \{\} <br /> |
| `message` _string_ | Message provides human-readable context for why the lock is held. |  | Optional: \{\} <br /> |
| `acquiredAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | AcquiredAt is when the lock was first acquired. |  | Optional: \{\} <br /> |
| `renewedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | RenewedAt is updated when the holder reasserts the lock during reconciliation. |  | Optional: \{\} <br /> |


#### PKCS11RuntimeConfig



PKCS11RuntimeConfig configures local runtime wiring needed by PKCS#11 vendor
libraries. It is intentionally scoped to environment variables and library
lookup paths so HSM integrations do not require custom wrapper scripts.



_Appears in:_
- [PKCS11SealConfig](#pkcs11sealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `libraryPath` _string_ | LibraryPath sets LD_LIBRARY_PATH for the OpenBao process. Use this when<br />the configured PKCS#11 module depends on sibling vendor libraries that<br />are not in the image's default dynamic linker search path. |  | Optional: \{\} <br /> |
| `env` _[PKCS11RuntimeEnvVar](#pkcs11runtimeenvvar) array_ | Env exposes literal environment variables from keys in<br />spec.unseal.credentialsSecretRef. Use this for vendor runtime settings<br />such as HSM endpoints or authentication key references. |  | MaxItems: 16 <br />Optional: \{\} <br /> |
| `fileEnv` _[PKCS11RuntimeFileEnvVar](#pkcs11runtimefileenvvar) array_ | FileEnv exposes environment variables whose values are paths to files<br />mounted from keys in spec.unseal.credentialsSecretRef. Use this for vendor<br />settings that expect a config file path, for example SOFTHSM2_CONF or<br />vendor-specific PKCS#11 client configuration variables. |  | MaxItems: 16 <br />Optional: \{\} <br /> |


#### PKCS11RuntimeEnvVar

_Underlying type:_ _`struct{Name string "json:\"name\""; SecretKey string "json:\"secretKey\""}`_

PKCS11RuntimeEnvVar maps a PKCS#11 runtime environment variable to a key in
spec.unseal.credentialsSecretRef.



_Appears in:_
- [PKCS11RuntimeConfig](#pkcs11runtimeconfig)



#### PKCS11RuntimeFileEnvVar

_Underlying type:_ _`struct{Name string "json:\"name\""; SecretKey string "json:\"secretKey\""}`_

PKCS11RuntimeFileEnvVar maps a PKCS#11 runtime environment variable to the
mounted file path for a key in spec.unseal.credentialsSecretRef.



_Appears in:_
- [PKCS11RuntimeConfig](#pkcs11runtimeconfig)



#### PKCS11SealConfig



PKCS11SealConfig configures the PKCS#11 seal type.
See: https://openbao.org/docs/configuration/seal/pkcs11/



_Appears in:_
- [UnsealConfig](#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `lib` _string_ | Lib is the path to the PKCS#11 library provided by the HSM vendor. |  | MinLength: 1 <br /> |
| `slot` _string_ | Slot is the slot number where the HSM token is located. |  | Optional: \{\} <br /> |
| `tokenLabel` _string_ | TokenLabel is the token label of the HSM slot to use instead of Slot. |  | Optional: \{\} <br /> |
| `pin` _string_ | PIN is the PIN for accessing the HSM token.<br />Note: It is strongly recommended to use CredentialsSecretRef instead of setting this directly. |  | Optional: \{\} <br /> |
| `keyLabel` _string_ | KeyLabel is the label for the encryption key used by OpenBao. |  | MinLength: 1 <br /> |
| `keyID` _string_ | KeyID is the PKCS#11 key identifier to use instead of KeyLabel. |  | Optional: \{\} <br /> |
| `mechanism` _string_ | Mechanism overrides the PKCS#11 wrapping or encryption mechanism. |  | Optional: \{\} <br /> |
| `disableSoftwareEncryption` _boolean_ | DisableSoftwareEncryption disables the software encryption fallback. |  | Optional: \{\} <br /> |
| `disabled` _boolean_ | Disabled disables this seal configuration, for example during seal migration. |  | Optional: \{\} <br /> |
| `rsaOAEPHash` _string_ | RSAOAEPHash specifies the hash algorithm to use for RSA with OAEP padding.<br />Valid values: sha1, sha224, sha256, sha384, sha512. |  | Enum: [sha1 sha224 sha256 sha384 sha512] <br />Optional: \{\} <br /> |
| `runtime` _[PKCS11RuntimeConfig](#pkcs11runtimeconfig)_ | Runtime configures local PKCS#11 vendor runtime wiring such as library<br />lookup paths and environment variables sourced from credentialsSecretRef. |  | Optional: \{\} <br /> |


#### Plugin



Plugin defines a declarative plugin configuration.
See: https://openbao.org/docs/configuration/plugins/



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the plugin type (e.g., "secret", "auth"). |  | MinLength: 1 <br /> |
| `name` _string_ | Name is the name of the plugin. |  | MinLength: 1 <br /> |
| `image` _string_ | Image is the OCI image URL including registry and repository.<br />Required if Command is not set. Conflicts with Command. |  | Optional: \{\} <br /> |
| `command` _string_ | Command is the command name of a manually downloaded plugin.<br />Required if Image is not set. Conflicts with Image. |  | Optional: \{\} <br /> |
| `version` _string_ | Version is the plugin version and, when Image has no tag, the image tag.<br />OpenBao 2.7 and later can infer it from the image tag. Command-based KMS<br />plugins on OpenBao 2.7 and later do not require a version. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `binaryName` _string_ | BinaryName is the name of the plugin binary file within the OCI image.<br />OpenBao 2.7 and later can infer it from the image ENTRYPOINT or CMD. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `sha256sum` _string_ | SHA256Sum is the expected SHA256 checksum of the plugin binary.<br />Must be a 64-character hexadecimal string.<br />OpenBao 2.7 and later allow omission for digest-pinned OCI images and<br />command-based plugins. Earlier versions require this field. |  | MaxLength: 64 <br />MinLength: 64 <br />Pattern: `^[0-9a-fA-F]\{64\}$` <br />Optional: \{\} <br /> |
| `args` _string array_ | Args are arguments to pass to the running plugin.<br />Only used if plugin_auto_register=true is set. |  | Optional: \{\} <br /> |
| `env` _string array_ | Env are environment variables to pass to the running plugin.<br />Only used if plugin_auto_register=true is set. |  | Optional: \{\} <br /> |


#### PluginConfig



PluginConfig allows configuring plugin behavior.



_Appears in:_
- [OpenBaoConfiguration](#openbaoconfiguration)
- [RestoreTargetConfiguration](#restoretargetconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `fileUID` _integer_ | FileUID is the UID to use for plugin files. |  | Optional: \{\} <br /> |
| `filePermissions` _string_ | FilePermissions are the file permissions for plugin files (e.g., "0755"). |  | Optional: \{\} <br /> |
| `autoDownload` _boolean_ | AutoDownload controls automatic plugin downloads from OCI registries. |  | Optional: \{\} <br /> |
| `autoRegister` _boolean_ | AutoRegister controls automatic plugin registration. |  | Optional: \{\} <br /> |
| `downloadBehavior` _string_ | DownloadBehavior controls whether OpenBao startup fails or continues when<br />declarative OCI plugin downloads fail. Valid values are "fail" and<br />"continue"; OpenBao defaults to "fail" when unset. |  | Enum: [fail continue] <br />Optional: \{\} <br /> |


#### PodMetadataConfig



PodMetadataConfig configures additional metadata for the OpenBao Pod template.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)
- [ReadReplicaTemplateConfig](#readreplicatemplateconfig)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `labels` _object (keys:string, values:string)_ | Labels are merged into the generated OpenBao Pod template labels.<br />Operator-managed labels take precedence if the same key is specified here. |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations are merged into the generated OpenBao Pod template annotations.<br />Operator-managed annotations take precedence if the same key is specified here. |  | Optional: \{\} <br /> |


#### PolicyReconciliationStatus



PolicyReconciliationStatus records progress without granting OpenBao permissions.



_Appears in:_
- [WorkloadControllerStatus](#workloadcontrollerstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `revisions` _object (keys:string, values:string)_ | Revisions contains the last verified content digest for each observed policy.<br />An empty digest means the policy was observed missing or different and awaits repair.<br />An absent entry means the policy has not been observed; it does not block operations.<br />Failed reads retain the last observation; OpenBao still authorizes every operation. |  | Optional: \{\} <br /> |
| `attemptedRevision` _string_ | AttemptedRevision identifies the bundle used for the last attempt.<br />A desired bundle change bypasses the previous retry delay. |  | Optional: \{\} <br /> |
| `lastVerified` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastVerified is when the complete desired bundle was last verified or repaired.<br />Unchanged successful observations are refreshed at most once every five minutes. |  | Optional: \{\} <br /> |
| `retryAfter` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | RetryAfter delays policy requests after a failure, including during unrelated reconciles. |  | Optional: \{\} <br /> |
| `lastError` _[ControllerErrorStatus](#controllererrorstatus)_ | LastError reports policy failures independently of infrastructure and Autopilot errors. |  | Optional: \{\} <br /> |


#### Profile

_Underlying type:_ _string_

Profile defines the security posture for an OpenBaoCluster.

_Validation:_
- Enum: [Hardened Development]

_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description |
| --- | --- |
| `Hardened` | ProfileHardened enforces strict security requirements and rejects unsafe escape hatches.<br /> |
| `Development` | ProfileDevelopment allows relaxed security for development/testing.<br /> |


#### RaftAutopilotConfig



RaftAutopilotConfig configures Raft Autopilot behavior for dead server cleanup.
See: https://openbao.org/docs/concepts/integrated-storage/autopilot/



_Appears in:_
- [RaftConfig](#raftconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `cleanupDeadServers` _boolean_ | CleanupDeadServers enables automatic removal of dead Raft peers.<br />When enabled, Autopilot periodically removes servers that have been<br />unhealthy for longer than DeadServerLastContactThreshold.<br />Requires MinQuorum to be set (defaults to replicas/2 + 1). | true | Optional: \{\} <br /> |
| `deadServerLastContactThreshold` _string_ | DeadServerLastContactThreshold is the duration after which a server<br />is considered dead if it hasn't contacted the leader.<br />Minimum: "1m". Default: "5m" (operator default, shorter than OpenBao's 24h). | 5m | Optional: \{\} <br /> |
| `minQuorum` _integer_ | MinQuorum is the minimum number of servers before Autopilot can prune<br />dead servers. This prevents removing so many servers that quorum is lost.<br />If not specified, defaults to max(3, replicas/2 + 1). |  | Minimum: 3 <br />Optional: \{\} <br /> |
| `serverStabilizationTime` _string_ | ServerStabilizationTime is the minimum time a server must be healthy<br />before being promoted to voter. Default: "10s". |  | Optional: \{\} <br /> |
| `lastContactThreshold` _string_ | LastContactThreshold is the limit on the amount of time a server can<br />go without leader contact before being considered unhealthy.<br />Default: "10s". |  | Optional: \{\} <br /> |
| `maxTrailingLogs` _integer_ | MaxTrailingLogs is the amount of entries in the Raft Log that a server<br />can be behind before being considered unhealthy. Default: 1000. |  | Optional: \{\} <br /> |


#### RaftConfig



RaftConfig allows tuning the Raft storage backend.



_Appears in:_
- [OpenBaoConfiguration](#openbaoconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `performanceMultiplier` _integer_ | PerformanceMultiplier scales the Raft timing parameters. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `autopilot` _[RaftAutopilotConfig](#raftautopilotconfig)_ | Autopilot configures Raft Autopilot settings.<br />By default, dead server cleanup is enabled with a 5-minute threshold. |  | Optional: \{\} <br /> |


#### ReadReplicaConfig



ReadReplicaConfig defines the steady-state read-replica pool.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `replicas` _integer_ | Replicas is the desired number of permanent non-voters. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `service` _[ReadReplicaServiceConfig](#readreplicaserviceconfig)_ | Service configures an optional dedicated Service for read traffic. |  | Optional: \{\} <br /> |
| `template` _[ReadReplicaTemplateConfig](#readreplicatemplateconfig)_ | Template configures read-replica-specific Pod template overrides. |  | Optional: \{\} <br /> |
| `storage` _[ReadReplicaStorageConfig](#readreplicastorageconfig)_ | Storage configures read-replica-specific storage overrides. |  | Optional: \{\} <br /> |


#### ReadReplicaSchedulingConfig



ReadReplicaSchedulingConfig defines scheduling overrides for read replicas.



_Appears in:_
- [ReadReplicaTemplateConfig](#readreplicatemplateconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `nodeSelector` _object (keys:string, values:string)_ | NodeSelector defines node-selection constraints for read-replica Pods. |  | Optional: \{\} <br /> |
| `tolerations` _[Toleration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#toleration-v1-core) array_ | Tolerations defines Pod tolerations for read-replica Pods. |  | Optional: \{\} <br /> |
| `affinity` _[Affinity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#affinity-v1-core)_ | Affinity defines Pod affinity / anti-affinity rules for read-replica Pods. |  | Optional: \{\} <br /> |
| `topologySpreadConstraints` _[TopologySpreadConstraint](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#topologyspreadconstraint-v1-core) array_ | TopologySpreadConstraints defines topology spread constraints for<br />read-replica Pods. |  | Optional: \{\} <br /> |


#### ReadReplicaServiceConfig



ReadReplicaServiceConfig controls the optional read-only Service for the
read-replica pool.



_Appears in:_
- [ReadReplicaConfig](#readreplicaconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether the operator creates a dedicated Service for the<br />read-replica pool. |  | Optional: \{\} <br /> |
| `type` _[ServiceType](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#servicetype-v1-core)_ | Type is the Kubernetes Service type, for example "ClusterIP" or<br />"LoadBalancer". |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations are additional annotations to apply to the read Service. |  | Optional: \{\} <br /> |


#### ReadReplicaStatus



ReadReplicaStatus captures observed state for the read-replica pool.



_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `desiredReplicas` _integer_ | DesiredReplicas is the desired number of read replicas. |  | Optional: \{\} <br /> |
| `readyReplicas` _integer_ | ReadyReplicas is the number of Ready read-replica Pods observed. |  | Optional: \{\} <br /> |
| `registeredReplicas` _integer_ | RegisteredReplicas is the number of observed non-voter peers registered in<br />Raft membership. |  | Optional: \{\} <br /> |
| `healthyReplicas` _integer_ | HealthyReplicas is the number of read-replica peers that are currently<br />healthy according to the Raft Autopilot state endpoint. |  | Optional: \{\} <br /> |
| `storage` _[ReadReplicaStorageStatus](#readreplicastoragestatus)_ | Storage captures read-replica-specific storage observation state. |  | Optional: \{\} <br /> |


#### ReadReplicaStorageConfig



ReadReplicaStorageConfig defines storage overrides for the read-replica
StatefulSet.



_Appears in:_
- [ReadReplicaConfig](#readreplicaconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `size` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#quantity-resource-api)_ | Size is the requested persistent volume size for read replicas. |  | Optional: \{\} <br /> |
| `storageClassName` _string_ | StorageClassName is an optional StorageClass for read-replica PVCs. |  | Optional: \{\} <br /> |


#### ReadReplicaStorageStatus



ReadReplicaStorageStatus captures observed storage state for the read-replica
pool.



_Appears in:_
- [ReadReplicaStatus](#readreplicastatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `desiredPVCs` _integer_ | DesiredPVCs is the number of data PVCs expected for the read-replica pool. |  | Optional: \{\} <br /> |
| `boundPVCs` _integer_ | BoundPVCs is the number of observed data PVCs for the read-replica pool. |  | Optional: \{\} <br /> |
| `storageClassName` _string_ | StorageClassName is the effective StorageClass observed for the<br />read-replica PVCs when it is consistent. |  | Optional: \{\} <br /> |


#### ReadReplicaTemplateConfig



ReadReplicaTemplateConfig defines Pod-template overrides for read replicas.



_Appears in:_
- [ReadReplicaConfig](#readreplicaconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `metadata` _[PodMetadataConfig](#podmetadataconfig)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `resources` _[ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#resourcerequirements-v1-core)_ | Resources defines container resource requests and limits for read replicas. |  | Optional: \{\} <br /> |
| `scheduling` _[ReadReplicaSchedulingConfig](#readreplicaschedulingconfig)_ | Scheduling defines node-placement and topology overrides for read replicas. |  | Optional: \{\} <br /> |


#### RecoveryKeyRecipient



RecoveryKeyRecipient describes one public OpenPGP recipient for an encrypted
recovery-key share. The public key material is not secret, but it must be
fingerprint-verified before production use.



_Appears in:_
- [InitialRecoveryKeysConfig](#initialrecoverykeysconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is a stable ceremony-local recipient identifier used only for review<br />and status/evidence mapping. |  | MaxLength: 64 <br />MinLength: 1 <br />Pattern: `^[A-Za-z0-9][A-Za-z0-9_.-]*$` <br /> |
| `fingerprint` _string_ | Fingerprint is the expected OpenPGP public-key fingerprint for the<br />recipient. It is informational for the Operator and should be verified<br />out of band by the ceremony participants. |  | Pattern: `^([0-9A-Fa-f]\{40\}\|[0-9A-Fa-f]\{64\})$` <br />Optional: \{\} <br /> |
| `pgpPublicKey` _string_ | PGPPublicKey is the base64-encoded binary OpenPGP public key material<br />expected by OpenBao's sys/rotate/recovery/init pgp_keys field. |  | MinLength: 1 <br /> |


#### RecoveryKeysConfig



RecoveryKeysConfig configures OpenBao recovery-key bootstrap surfaces.

The Operator only creates recovery keys during first self-initialization. It
does not distribute encrypted shares, collect decrypted shares, escrow share
material, or run generate-root ceremonies.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `initial` _[InitialRecoveryKeysConfig](#initialrecoverykeysconfig)_ | Initial configures the first recovery-key generation request for a<br />self-initialized cluster using auto-unseal. |  | Optional: \{\} <br /> |


#### RestoreClusterTemplate



RestoreClusterTemplate is the supported fresh recovery target profile.
Administrators must prepare the destination namespace network boundary.



_Appears in:_
- [OpenBaoRestoreSpec](../openbaorestore/#openbaorestorespec)
- [RestoreTest](#restoretest)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version must match the administrator-observed snapshot source version. |  | Enum: [2.7.0 2.7.1] <br /> |
| `image` _string_ | Image defaults to the version-derived image. |  | Optional: \{\} <br /> |
| `storage` _[StorageConfig](#storageconfig)_ |  |  |  |
| `tls` _[TLSConfig](#tlsconfig)_ |  |  |  |
| `unseal` _[UnsealConfig](#unsealconfig)_ | Unseal uses the same provider configuration as OpenBaoCluster. The target<br />must be able to decrypt the snapshot with the original seal key material. |  |  |
| `serviceAccount` _[ServiceAccountConfig](#serviceaccountconfig)_ | ServiceAccount configures credentials supplied through workload identity. |  | Optional: \{\} <br /> |
| `podMetadata` _[PodMetadataConfig](#podmetadataconfig)_ | PodMetadata supplies provider-specific workload identity metadata. |  | Optional: \{\} <br /> |
| `plugins` _[Plugin](#plugin) array_ | Plugins declares KMS seal plugins required by Unseal. OpenBao 2.7 requires<br />plugins for AWS, Azure, GCP, OCI, and PKCS#11 seals. |  | Optional: \{\} <br /> |
| `configuration` _[RestoreTargetConfiguration](#restoretargetconfiguration)_ | Configuration configures KMS plugin behavior for the target server. |  | Optional: \{\} <br /> |
| `resources` _[ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#resourcerequirements-v1-core)_ |  |  | Optional: \{\} <br /> |
| `initContainer` _[InitContainerConfig](#initcontainerconfig)_ |  |  | Optional: \{\} <br /> |
| `imagePullSecrets` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core) array_ |  |  | MaxItems: 2 <br />Optional: \{\} <br /> |


#### RestoreConfig



RestoreConfig defines optional configuration for restore operations.

This is primarily used with self-init JWT bootstrap to pre-create a JWT role
that can be referenced by OpenBaoRestore resources.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `jwtAuthRole` _string_ | JWTAuthRole is the name of the JWT Auth role configured in OpenBao<br />for restore operations. When set, and when spec.selfInit.oidc.enabled is true,<br />the operator bootstraps a restore policy and JWT role bound to the restore ServiceAccount<br />(&lt;cluster-name&gt;-restore-serviceaccount).<br />If OIDC is enabled in SelfInit and this field is empty, a default role<br />named "openbao-operator-restore" will be assumed/created.<br />The role must grant "update" capability on sys/storage/raft/snapshot and<br />sys/storage/raft/snapshot-force. The force endpoint supports explicitly<br />requested break-glass restores. |  | Optional: \{\} <br /> |


#### RestoreTargetConfiguration



RestoreTargetConfiguration exposes server settings needed by KMS seal plugins.



_Appears in:_
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `plugin` _[PluginConfig](#pluginconfig)_ | Plugin controls downloads, registration, and file ownership for Plugins.<br />Set autoDownload to true to download declared OCI plugins on startup. |  | Optional: \{\} <br /> |


#### RestoreTest



RestoreTest schedules one disposable restore at a time.
The destination must be an administrator-prepared operator-managed namespace.



_Appears in:_
- [BackupSchedule](#backupschedule)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `schedule` _string_ | Schedule uses five-field cron syntax and UTC. |  | MaxLength: 128 <br />MinLength: 1 <br />Optional: \{\} <br /> |
| `everySuccessfulBackups` _integer_ |  |  | Maximum: 10000 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `namespace` _string_ |  |  | MaxLength: 63 <br />MinLength: 1 <br /> |
| `clusterTemplate` _[RestoreClusterTemplate](#restoreclustertemplate)_ |  |  |  |
| `credentialsSecretRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | CredentialsSecretRef is the existing destination storage credential Secret.<br />Storage connection settings are inherited from the source backup target. |  | Optional: \{\} <br /> |
| `serviceAccountName` _string_ | ServiceAccountName selects an existing executor ServiceAccount in Namespace.<br />The operator does not create, modify, or delete this account. It must have<br />its own storage-reader identity; backup writer identities are not inherited. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$` <br />Optional: \{\} <br /> |
| `roleArn` _string_ | RoleARN selects the S3 reader role independently of the backup writer role. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `workloadIdentity` _[WorkloadIdentityConfig](#workloadidentityconfig)_ | WorkloadIdentity supplies destination reader metadata. ServiceAccount<br />annotations must already exist on ServiceAccountName; PodLabels apply to<br />the executor Pod. The operator does not update the existing account. |  | Optional: \{\} <br /> |
| `cleanupAfterSeconds` _integer_ | CleanupAfterSeconds delays deletion after snapshot application to permit inspection. |  | Maximum: 86400 <br />Minimum: 0 <br />Optional: \{\} <br /> |


#### RestoreTestOutcome

_Underlying type:_ _string_

RestoreTestOutcome reports whether a disposable restore test passed or failed.

_Validation:_
- Enum: [Passed Failed]

_Appears in:_
- [RestoreTestResult](#restoretestresult)

| Field | Description |
| --- | --- |
| `Passed` |  |
| `Failed` |  |


#### RestoreTestResult



RestoreTestResult contains no secret data or authentication tokens.



_Appears in:_
- [RestoreTestStatus](#restoreteststatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the child OpenBaoRestore that produced this result. |  |  |
| `finishedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | FinishedAt records when the result was recorded. |  |  |
| `namespace` _string_ | Namespace identifies the destination namespace. |  | Optional: \{\} <br /> |
| `key` _string_ | Key identifies the tested snapshot object in backup storage. |  | Optional: \{\} <br /> |
| `digest` _string_ | Digest is the pinned snapshot digest, when available. |  | Optional: \{\} <br /> |
| `reason` _string_ | Reason and Message retain a bounded summary after the child is deleted.<br />They contain operator-defined diagnostics, never provider responses or logs. |  | MaxLength: 64 <br />Optional: \{\} <br /> |
| `message` _string_ | Message contains the operator-defined diagnostic for this result. |  | MaxLength: 512 <br />Optional: \{\} <br /> |
| `outcome` _[RestoreTestOutcome](#restoretestoutcome)_ | Outcome is Passed or Failed. |  | Enum: [Passed Failed] <br /> |


#### RestoreTestRun



RestoreTestRun reserves one child request and protects its source key.



_Appears in:_
- [RestoreTestStatus](#restoreteststatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `namespace` _string_ | Namespace is the destination namespace of the child OpenBaoRestore. |  |  |
| `name` _string_ | Name is the generated name of the child OpenBaoRestore. |  |  |
| `key` _string_ | Key is the snapshot object under test; retention protects it until the run ends. |  |  |
| `startedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | StartedAt records when the run was reserved. |  |  |
| `uid` _[UID](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#uid-types-pkg)_ | UID identifies the child OpenBaoRestore once its creation is observed. |  | Optional: \{\} <br /> |


#### RestoreTestStatus



RestoreTestStatus stores at most one active request and one last result.



_Appears in:_
- [BackupStatus](#backupstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `active` _[RestoreTestRun](#restoretestrun)_ |  |  | Optional: \{\} <br /> |
| `last` _[RestoreTestResult](#restoretestresult)_ |  |  | Optional: \{\} <br /> |
| `lastSuccessTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastSuccessTime survives later failures and controller restarts. |  | Optional: \{\} <br /> |
| `lastScheduledAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastScheduledAt records the last time a restore test was started. |  | Optional: \{\} <br /> |
| `lastBackupCount` _integer_ | LastBackupCount is the successful-backup count at the last started test. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions reports scheduling and restore test outcomes without secret data.<br />The Passed condition summarizes the current state. |  | Optional: \{\} <br /> |


#### RuntimeConfig



RuntimeConfig defines explicit runtime control requests for the OpenBao
workload.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `restartAt` _string_ | RestartAt triggers a rolling restart when changed.<br />The operator propagates this value as a Pod template annotation; any change<br />results in a new StatefulSet revision and a controlled restart.<br />Recommended value is an RFC3339 timestamp string. |  | MinLength: 1 <br />Optional: \{\} <br /> |


#### SelfInitAuditDevice



SelfInitAuditDevice provides structured configuration for enabling audit devices
via self-init requests. This replaces the need for raw JSON in the Data field.
See: https://openbao.org/api-docs/system/audit/



_Appears in:_
- [SelfInitRequest](#selfinitrequest)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of audit device (e.g., "file", "syslog", "socket", "http"). |  | Enum: [file syslog socket http] <br />MinLength: 1 <br /> |
| `description` _string_ | Description is an optional description for the audit device. |  | Optional: \{\} <br /> |
| `fileOptions` _[FileAuditOptions](#fileauditoptions)_ | FileOptions configures options for file audit devices.<br />Only used when Type is "file". |  | Optional: \{\} <br /> |
| `httpOptions` _[HTTPAuditOptions](#httpauditoptions)_ | HTTPOptions configures options for HTTP audit devices.<br />Only used when Type is "http". |  | Optional: \{\} <br /> |
| `syslogOptions` _[SyslogAuditOptions](#syslogauditoptions)_ | SyslogOptions configures options for syslog audit devices.<br />Only used when Type is "syslog". |  | Optional: \{\} <br /> |
| `socketOptions` _[SocketAuditOptions](#socketauditoptions)_ | SocketOptions configures options for socket audit devices.<br />Only used when Type is "socket". |  | Optional: \{\} <br /> |


#### SelfInitAuthMethod



SelfInitAuthMethod provides structured configuration for enabling auth methods
via self-init requests. This replaces the need for raw JSON in the Data field.
See: https://openbao.org/api-docs/system/auth/



_Appears in:_
- [SelfInitRequest](#selfinitrequest)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of auth method (e.g., "jwt", "kubernetes", "userpass", "ldap"). |  | MinLength: 1 <br /> |
| `description` _string_ | Description is an optional description for the auth method. |  | Optional: \{\} <br /> |
| `config` _object (keys:string, values:string)_ | Config contains optional configuration for the auth method mount.<br />Common fields include: default_lease_ttl, max_lease_ttl, listing_visibility, etc. |  | Optional: \{\} <br /> |


#### SelfInitConfig



SelfInitConfig enables OpenBao's self-initialization feature.
When enabled, OpenBao initializes itself on first start using the configured
requests, and the root token is automatically revoked.
See: https://openbao.org/docs/configuration/self-init/



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled activates OpenBao's self-initialization feature.<br />When true, the Operator injects initialize stanzas into config.hcl<br />and does NOT create a root token Secret (root token is auto-revoked).<br />WARNING: The root token is auto-revoked during initialization. You MUST<br />configure user authentication (e.g., userpass, JWT, Kubernetes auth) via<br />spec.selfInit.requests before enabling this. spec.selfInit.oidc.enabled<br />only provides Operator authentication for lifecycle tasks, NOT user access.<br />Enabling without user authentication results in permanent lockout. | false |  |
| `oidc` _[SelfInitOIDCConfig](#selfinitoidcconfig)_ | OIDC configures JWT authentication for the Operator to perform cluster<br />lifecycle operations (backups, upgrades, restores). When enabled, this<br />sets up the jwt-operator auth method, OIDC discovery, and operator roles.<br />This is for Operator authentication only - users must configure their own<br />authentication methods via spec.selfInit.requests. |  | Optional: \{\} <br /> |
| `requests` _[SelfInitRequest](#selfinitrequest) array_ | Requests defines the API operations to execute during self-initialization.<br />Each request becomes a named request block inside an initialize stanza. |  | Optional: \{\} <br /> |


#### SelfInitOIDCAdditionalSubjects



SelfInitOIDCAdditionalSubjects adds recovery-target identities to the
generated Operator JWT roles without combining their policies.



_Appears in:_
- [SelfInitOIDCConfig](#selfinitoidcconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `operator` _[KubernetesServiceAccountSubject](#kubernetesserviceaccountsubject) array_ | Operator lists additional controller ServiceAccount subjects. |  | MaxItems: 32 <br />MaxLength: 340 <br />Pattern: `^system:serviceaccount:[a-z0-9]([-a-z0-9]*[a-z0-9])?:[a-z0-9]([-a-z0-9.]*[a-z0-9])?$` <br />Optional: \{\} <br /> |
| `backup` _[KubernetesServiceAccountSubject](#kubernetesserviceaccountsubject) array_ | Backup lists additional backup Job ServiceAccount subjects. |  | MaxItems: 32 <br />MaxLength: 340 <br />Pattern: `^system:serviceaccount:[a-z0-9]([-a-z0-9]*[a-z0-9])?:[a-z0-9]([-a-z0-9.]*[a-z0-9])?$` <br />Optional: \{\} <br /> |
| `restore` _[KubernetesServiceAccountSubject](#kubernetesserviceaccountsubject) array_ | Restore lists additional restore Job ServiceAccount subjects. |  | MaxItems: 32 <br />MaxLength: 340 <br />Pattern: `^system:serviceaccount:[a-z0-9]([-a-z0-9]*[a-z0-9])?:[a-z0-9]([-a-z0-9.]*[a-z0-9])?$` <br />Optional: \{\} <br /> |
| `upgrade` _[KubernetesServiceAccountSubject](#kubernetesserviceaccountsubject) array_ | Upgrade lists additional upgrade Job ServiceAccount subjects. |  | MaxItems: 32 <br />MaxLength: 340 <br />Pattern: `^system:serviceaccount:[a-z0-9]([-a-z0-9]*[a-z0-9])?:[a-z0-9]([-a-z0-9.]*[a-z0-9])?$` <br />Optional: \{\} <br /> |


#### SelfInitOIDCConfig



SelfInitOIDCConfig configures OIDC identity for the cluster.



_Appears in:_
- [SelfInitConfig](#selfinitconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled triggers the bootstrap logic. |  |  |
| `audience` _string_ | Audience, if set, must match the operator installation audience used for<br />projected OpenBao auth tokens.<br />This field does not create a per-cluster TokenRequest audience override. |  | Optional: \{\} <br /> |
| `issuer` _string_ | Issuer overrides the auto-discovered K8s issuer URL.<br />Critical for scenarios where OpenBao sees a different K8s URL than the Operator. |  | Optional: \{\} <br /> |
| `additionalSubjects` _[SelfInitOIDCAdditionalSubjects](#selfinitoidcadditionalsubjects)_ | AdditionalSubjects adds exact Kubernetes ServiceAccount subjects to the<br />generated Operator JWT roles. Use these bindings when a snapshot must<br />remain operable after restore to a target with different ServiceAccount<br />subjects. Configure the source cluster before self-initialization so the<br />bindings are present in each snapshot.<br />These bindings do not configure JWT issuer or signature verification for<br />another Kubernetes control plane. The jwt-operator auth method must also<br />trust the target's projected ServiceAccount tokens. |  | Optional: \{\} <br /> |


#### SelfInitOperation

_Underlying type:_ _string_

SelfInitOperation defines valid operations for self-initialization requests.

_Validation:_
- Enum: [create read update delete list patch]

_Appears in:_
- [SelfInitRequest](#selfinitrequest)

| Field | Description |
| --- | --- |
| `create` | SelfInitOperationCreate creates a new resource.<br /> |
| `read` | SelfInitOperationRead reads an existing resource.<br /> |
| `update` | SelfInitOperationUpdate updates an existing resource.<br /> |
| `patch` | SelfInitOperationPatch performs a partial update to an existing resource.<br /> |
| `delete` | SelfInitOperationDelete deletes an existing resource.<br /> |
| `list` | SelfInitOperationList lists resources.<br /> |


#### SelfInitPolicy



SelfInitPolicy provides structured configuration for creating/updating policies
via self-init requests. This replaces the need for raw JSON in the Data field.
See: https://openbao.org/api-docs/system/policy/



_Appears in:_
- [SelfInitRequest](#selfinitrequest)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `policy` _string_ | Policy is the HCL or JSON policy content.<br />This is the actual policy rules that will be applied. |  | MinLength: 1 <br /> |


#### SelfInitRequest



SelfInitRequest defines a single API operation to execute during self-initialization.



_Appears in:_
- [SelfInitConfig](#selfinitconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is a unique identifier for this request (used as the block name).<br />Must match regex ^[A-Za-z_][A-Za-z0-9_-]*$ |  | MaxLength: 64 <br />MinLength: 1 <br />Pattern: `^[A-Za-z_][A-Za-z0-9_-]*$` <br /> |
| `operation` _[SelfInitOperation](#selfinitoperation)_ | Operation is the API operation type: create, read, update, delete, or list. |  | Enum: [create read update delete list patch] <br /> |
| `path` _string_ | Path is the API path to call (e.g., "sys/audit/stdout", "auth/kubernetes/config"). |  | MinLength: 1 <br /> |
| `headers` _object (keys:string, values:string array)_ | Headers contains additional HTTP headers to send with this self-init request.<br />Header names must not be empty. Values are rendered into OpenBao's profile-engine<br />`headers` request field as map[string][]string. |  | Optional: \{\} <br /> |
| `when` _[JSON](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#json-v1-apiextensions-k8s-io)_ | When controls whether OpenBao executes this request.<br />Omit it to execute the request. Set it to a JSON boolean for static gating<br />or to an OpenBao profile value object for dynamic evaluation, for example<br />\{"eval_source":"cel","eval_type":"bool","expression":"true"\}. |  | Optional: \{\} <br /> |
| `auditDevice` _[SelfInitAuditDevice](#selfinitauditdevice)_ | AuditDevice configures an audit device when Path starts with "sys/audit/".<br />This provides structured configuration for audit devices instead of raw JSON.<br />Only used when Path matches the pattern "sys/audit/*". |  | Optional: \{\} <br /> |
| `authMethod` _[SelfInitAuthMethod](#selfinitauthmethod)_ | AuthMethod configures an auth method when Path starts with "sys/auth/".<br />This provides structured configuration for enabling auth methods.<br />Only used when Path matches the pattern "sys/auth/*". |  | Optional: \{\} <br /> |
| `secretEngine` _[SelfInitSecretEngine](#selfinitsecretengine)_ | SecretEngine configures a secret engine when Path starts with "sys/mounts/".<br />This provides structured configuration for enabling secret engines.<br />Only used when Path matches the pattern "sys/mounts/*". |  | Optional: \{\} <br /> |
| `policy` _[SelfInitPolicy](#selfinitpolicy)_ | Policy configures a policy when Path starts with "sys/policies/".<br />This provides structured configuration for creating/updating policies.<br />Only used when Path matches the pattern "sys/policies/*". |  | Optional: \{\} <br /> |
| `data` _[JSON](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#json-v1-apiextensions-k8s-io)_ | Data contains the request payload for paths that don't have structured types.<br />This must be a JSON/YAML object whose shape matches the target API endpoint.<br />Nested maps and lists are supported and are rendered into the initialize stanza as HCL objects.<br />**Note:** For common paths, use structured types instead:<br />- `sys/audit/*` → use `auditDevice`<br />- `sys/auth/*` → use `authMethod`<br />- `sys/mounts/*` → use `secretEngine`<br />- `sys/policies/*` → use `policy`<br />This payload is stored in the OpenBaoCluster resource and persisted in etcd;<br />it must not contain sensitive values such as tokens, passwords, or unseal keys. |  | Optional: \{\} <br /> |
| `allowFailure` _boolean_ | AllowFailure allows this request to fail without blocking initialization.<br />Defaults to false. |  | Optional: \{\} <br /> |


#### SelfInitSecretEngine



SelfInitSecretEngine provides structured configuration for enabling secret engines
via self-init requests. This replaces the need for raw JSON in the Data field.
See: https://openbao.org/api-docs/system/mounts/



_Appears in:_
- [SelfInitRequest](#selfinitrequest)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of secret engine (e.g., "kv", "pki", "transit", "database"). |  | MinLength: 1 <br /> |
| `description` _string_ | Description is an optional description for the secret engine. |  | Optional: \{\} <br /> |
| `options` _object (keys:string, values:string)_ | Options contains optional configuration specific to the secret engine type.<br />For KV engines, common options include: version ("1" or "2").<br />For other engines, options vary by type. |  | Optional: \{\} <br /> |


#### ServiceAccountConfig



ServiceAccountConfig configures the ServiceAccount used by OpenBao pods.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name overrides the generated ServiceAccount name.<br />If not specified, defaults to "&lt;cluster-name&gt;-serviceaccount". |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations to add to the ServiceAccount.<br />Useful for cloud provider Workload Identity (e.g. eks.amazonaws.com/role-arn). |  | Optional: \{\} <br /> |


#### ServiceConfig



ServiceConfig controls how the main OpenBao Service is exposed.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _[ServiceType](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#servicetype-v1-core)_ | Type is the Kubernetes Service type, for example "ClusterIP" or "LoadBalancer". |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations are additional annotations to apply to the Service. |  | Optional: \{\} <br /> |


#### ServiceMonitorAuthorizationConfig



ServiceMonitorAuthorizationConfig configures Prometheus Operator endpoint authorization.



_Appears in:_
- [ServiceMonitorConfig](#servicemonitorconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the authorization type.<br />Defaults to Bearer when credentialsSecret is set. |  | Optional: \{\} <br /> |
| `credentialsSecret` _[ServiceMonitorKeySelector](#servicemonitorkeyselector)_ | CredentialsSecret references a Secret key containing the authorization credentials.<br />The Secret must exist in the same namespace as the ServiceMonitor. |  |  |


#### ServiceMonitorConfig



ServiceMonitorConfig configures the Prometheus ServiceMonitor.



_Appears in:_
- [MetricsConfig](#metricsconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether to create the ServiceMonitor.<br />Defaults to true if Metrics are enabled. | true |  |
| `interval` _string_ | Interval is the scrape interval. | 30s | Optional: \{\} <br /> |
| `scrapeTimeout` _string_ | ScrapeTimeout is the scrape timeout. | 10s | Optional: \{\} <br /> |
| `labels` _object (keys:string, values:string)_ | Labels are added to the ServiceMonitor metadata.<br />Use this for Prometheus selectors, such as release labels used by kube-prometheus-stack. |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations are added to the ServiceMonitor metadata. |  | Optional: \{\} <br /> |
| `jobLabel` _string_ | JobLabel selects the Service label Prometheus uses as the job label.<br />Defaults to app.kubernetes.io/name. |  | Optional: \{\} <br /> |
| `authorization` _[ServiceMonitorAuthorizationConfig](#servicemonitorauthorizationconfig)_ | Authorization configures an optional ServiceMonitor authorization block.<br />Use this for authenticated /v1/sys/metrics scraping. |  | Optional: \{\} <br /> |
| `tlsConfig` _[ServiceMonitorTLSConfig](#servicemonitortlsconfig)_ | TLSConfig configures TLS verification for the OpenBao scrape endpoint. |  | Optional: \{\} <br /> |


#### ServiceMonitorKeySelector



ServiceMonitorKeySelector identifies a key in a Secret or ConfigMap.



_Appears in:_
- [ServiceMonitorAuthorizationConfig](#servicemonitorauthorizationconfig)
- [ServiceMonitorTLSConfig](#servicemonitortlsconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the Secret or ConfigMap name. |  | MinLength: 1 <br /> |
| `key` _string_ | Key is the key within the Secret or ConfigMap.<br />Defaults to token for authorization credentials and ca.crt for CA references. |  | Optional: \{\} <br /> |


#### ServiceMonitorTLSConfig



ServiceMonitorTLSConfig configures TLS settings for the Prometheus Operator endpoint.



_Appears in:_
- [ServiceMonitorConfig](#servicemonitorconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serverName` _string_ | ServerName verifies the hostname in the OpenBao serving certificate. |  | Optional: \{\} <br /> |
| `insecureSkipVerify` _boolean_ | InsecureSkipVerify disables TLS certificate verification.<br />Use only for temporary non-production environments.<br />Hardened clusters reject insecureSkipVerify=true. |  | Optional: \{\} <br /> |
| `caConfigMap` _[ServiceMonitorKeySelector](#servicemonitorkeyselector)_ | CAConfigMap references a ConfigMap key containing the CA certificate.<br />Mutually exclusive with CASecret. |  | Optional: \{\} <br /> |
| `caSecret` _[ServiceMonitorKeySelector](#servicemonitorkeyselector)_ | CASecret references a Secret key containing the CA certificate.<br />Mutually exclusive with CAConfigMap. |  | Optional: \{\} <br /> |


#### SocketAuditOptions



SocketAuditOptions configures options for socket audit devices.
See: https://openbao.org/docs/audit/socket/



_Appears in:_
- [AuditDevice](#auditdevice)
- [SelfInitAuditDevice](#selfinitauditdevice)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `address` _string_ | Address is the socket server address to use.<br />Example: "127.0.0.1:9090" or "/tmp/audit.sock". |  | Optional: \{\} <br /> |
| `socketType` _string_ | SocketType is the socket type to use, any type compatible with net.Dial is acceptable.<br />Defaults to "tcp" if not specified. |  | Optional: \{\} <br /> |
| `writeTimeout` _string_ | WriteTimeout is the (deadline) time in seconds to allow writes to be completed over the socket.<br />A zero value means that write attempts will not time out.<br />Defaults to "2s" if not specified. |  | Optional: \{\} <br /> |


#### StaticSealConfig



StaticSealConfig configures the static seal type.
This is the default seal type managed by the operator.
See: https://openbao.org/docs/configuration/seal/static/



_Appears in:_
- [UnsealConfig](#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `currentKey` _string_ | CurrentKey is the path to the static unseal key file.<br />Defaults to "file:///etc/bao/unseal/key" (operator-managed). |  | Optional: \{\} <br /> |
| `currentKeyID` _string_ | CurrentKeyID is the identifier for the current unseal key.<br />Defaults to "operator-generated-v1" (operator-managed). |  | Optional: \{\} <br /> |


#### StorageConfig



StorageConfig captures storage-related configuration for the StatefulSet.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `size` _string_ | Size is the requested persistent volume size, for example "10Gi". |  | MinLength: 1 <br /> |
| `storageClassName` _string_ | StorageClassName is an optional StorageClass for the PVCs. |  | Optional: \{\} <br /> |


#### SyslogAuditOptions



SyslogAuditOptions configures options for syslog audit devices.
See: https://openbao.org/docs/audit/syslog/



_Appears in:_
- [AuditDevice](#auditdevice)
- [SelfInitAuditDevice](#selfinitauditdevice)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `facility` _string_ | Facility is the syslog facility to use.<br />Defaults to "AUTH" if not specified. |  | Optional: \{\} <br /> |
| `tag` _string_ | Tag is the syslog tag to use.<br />Defaults to "openbao" if not specified. |  | Optional: \{\} <br /> |


#### TLSConfig



TLSConfig captures TLS configuration for an OpenBaoCluster.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether TLS is enabled for the cluster. |  | Required: \{\} <br /> |
| `mode` _[TLSMode](#tlsmode)_ | Mode controls who manages the certificate lifecycle. | OperatorManaged | Enum: [OperatorManaged External ACME] <br />Optional: \{\} <br /> |
| `acme` _[ACMEConfig](#acmeconfig)_ | ACME configures settings when Mode is 'ACME'. |  | Optional: \{\} <br /> |
| `rotationPeriod` _string_ | RotationPeriod is a duration string (for example, "720h") controlling certificate rotation.<br />Only used when Mode is OperatorManaged. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `extraSANs` _string array_ | ExtraSANs lists additional subject alternative names for server certificates.<br />In OperatorManaged mode, the operator includes these names when issuing the certificate.<br />In External mode, the operator requires the supplied certificate to contain them.<br />Values that parse as IP addresses are treated as IP SANs; all other values are DNS SANs. |  | Optional: \{\} <br /> |


#### TLSKeyExchangeGroup

_Underlying type:_ _string_

TLSKeyExchangeGroup identifies a classical or hybrid post-quantum TLS group.
Pure MLKEM1024 is not supported by the operator's default TLS clients.

_Validation:_
- Enum: [CurveP256 CurveP384 CurveP521 X25519 X25519MLKEM768 SecP256r1MLKEM768 SecP384r1MLKEM1024]

_Appears in:_
- [ListenerConfig](#listenerconfig)

| Field | Description |
| --- | --- |
| `CurveP256` |  |
| `CurveP384` |  |
| `CurveP521` |  |
| `X25519` |  |
| `X25519MLKEM768` |  |
| `SecP256r1MLKEM768` |  |
| `SecP384r1MLKEM1024` |  |


#### TLSMode

_Underlying type:_ _string_

TLSMode controls who manages the certificate lifecycle.

_Validation:_
- Enum: [OperatorManaged External ACME]

_Appears in:_
- [TLSConfig](#tlsconfig)

| Field | Description |
| --- | --- |
| `OperatorManaged` | TLSModeOperatorManaged: The operator acts as the CA, generating keys and rotating certs (Current Behavior).<br /> |
| `External` | TLSModeExternal: The operator assumes Secrets are managed by an external entity (cert-manager, user, or CSI driver).<br />The operator will mount them but NOT modify/rotate them.<br /> |
| `ACME` | TLSModeACME: OpenBao uses its native ACME client to fetch certificates.<br />No Secrets are mounted. No sidecar is injected. Best for Zero Trust.<br /> |


#### TLSVersion

_Underlying type:_ _string_

TLSVersion identifies a supported TLS protocol version.

_Validation:_
- Enum: [tls12 tls13]

_Appears in:_
- [ListenerConfig](#listenerconfig)

| Field | Description |
| --- | --- |
| `tls12` | TLSVersion12 selects TLS 1.2.<br /> |
| `tls13` | TLSVersion13 selects TLS 1.3.<br /> |


#### TelemetryConfig



TelemetryConfig defines telemetry reporting configuration.
See: https://openbao.org/docs/configuration/telemetry/



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `usageGaugePeriod` _string_ | Common telemetry options<br />UsageGaugePeriod specifies the interval at which high-cardinality usage data is collected. |  | Optional: \{\} <br /> |
| `maximumGaugeCardinality` _integer_ | MaximumGaugeCardinality is the maximum cardinality of gauge labels. |  | Optional: \{\} <br /> |
| `disableHostname` _boolean_ | DisableHostname specifies if gauge values should be prefixed with the local hostname. |  | Optional: \{\} <br /> |
| `enableHostnameLabel` _boolean_ | EnableHostnameLabel specifies if all metric values should contain the host label. |  | Optional: \{\} <br /> |
| `metricsPrefix` _string_ | MetricsPrefix specifies the prefix used for metric values. |  | Optional: \{\} <br /> |
| `leaseMetricsEpsilon` _string_ | LeaseMetricsEpsilon specifies the size of the bucket used to measure future lease expiration. |  | Optional: \{\} <br /> |
| `prometheusRetentionTime` _string_ | Prometheus-specific options<br />PrometheusRetentionTime specifies how long to retain metrics in Prometheus format. |  | Optional: \{\} <br /> |
| `statsiteAddress` _string_ | Statsite-specific options<br />StatsiteAddress is the address of the statsite server. |  | Optional: \{\} <br /> |
| `statsdAddress` _string_ | StatsD-specific options<br />StatsdAddress is the address of the StatsD server. |  | Optional: \{\} <br /> |
| `dogStatsdAddress` _string_ | DogStatsD-specific options<br />DogStatsdAddress is the address of the DogStatsD server. |  | Optional: \{\} <br /> |
| `dogStatsdTags` _string array_ | DogStatsdTags are tags to add to all metrics. |  | Optional: \{\} <br /> |
| `circonusAPIKey` _string_ | Circonus-specific options<br />CirconusAPIKey is the API key for Circonus. |  | Optional: \{\} <br /> |
| `circonusAPIApp` _string_ | CirconusAPIApp is the API app name for Circonus. |  | Optional: \{\} <br /> |
| `circonusAPIURL` _string_ | CirconusAPIURL is the API URL for Circonus. |  | Optional: \{\} <br /> |
| `circonusSubmissionInterval` _string_ | CirconusSubmissionInterval is the submission interval for Circonus. |  | Optional: \{\} <br /> |
| `circonusCheckID` _string_ | CirconusCheckID is the check ID for Circonus. |  | Optional: \{\} <br /> |
| `circonusCheckForceMetricActivation` _string_ | CirconusCheckForceMetricActivation forces metric activation in Circonus. |  | Optional: \{\} <br /> |
| `circonusCheckInstanceID` _string_ | CirconusCheckInstanceID is the instance ID for Circonus. |  | Optional: \{\} <br /> |
| `circonusCheckSearchTag` _string_ | CirconusCheckSearchTag is the search tag for Circonus. |  | Optional: \{\} <br /> |
| `circonusCheckDisplayName` _string_ | CirconusCheckDisplayName is the display name for Circonus. |  | Optional: \{\} <br /> |
| `circonusCheckTags` _string_ | CirconusCheckTags is the tags for Circonus. |  | Optional: \{\} <br /> |
| `circonusBrokerID` _string_ | CirconusBrokerID is the broker ID for Circonus. |  | Optional: \{\} <br /> |
| `circonusBrokerSelectTag` _string_ | CirconusBrokerSelectTag is the broker select tag for Circonus. |  | Optional: \{\} <br /> |
| `stackdriverProjectID` _string_ | Stackdriver-specific options<br />StackdriverProjectID is the Google Cloud Project ID. |  | Optional: \{\} <br /> |
| `stackdriverLocation` _string_ | StackdriverLocation is the GCP or AWS region. |  | Optional: \{\} <br /> |
| `stackdriverNamespace` _string_ | StackdriverNamespace is a namespace identifier for the telemetry data. |  | Optional: \{\} <br /> |
| `stackdriverDebugLogs` _boolean_ | StackdriverDebugLogs specifies if OpenBao writes additional stackdriver debug logs. |  | Optional: \{\} <br /> |


#### TransitSealConfig



TransitSealConfig configures the Transit seal type.
See: https://openbao.org/docs/configuration/seal/transit/



_Appears in:_
- [UnsealConfig](#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `address` _string_ | Address is the full HTTPS address to the OpenBao cluster providing the Transit seal. |  | MinLength: 1 <br /> |
| `token` _string_ | Token is the OpenBao token to use for authentication.<br />Note: It is strongly recommended to use CredentialsSecretRef instead of setting this directly. |  | Optional: \{\} <br /> |
| `keyName` _string_ | KeyName is the transit key to use for encryption and decryption. |  | MinLength: 1 <br /> |
| `mountPath` _string_ | MountPath is the mount path to the transit secret engine. |  | MinLength: 1 <br /> |
| `namespace` _string_ | Namespace is the namespace path to the transit secret engine. |  | Optional: \{\} <br /> |
| `disableRenewal` _boolean_ | DisableRenewal disables automatic token renewal.<br />Set to true if token lifecycle is managed externally (e.g., by OpenBao Agent). |  | Optional: \{\} <br /> |
| `tlsCACert` _string_ | TLSCACert is the path to the CA certificate file for TLS communication. |  | Optional: \{\} <br /> |
| `tlsClientCert` _string_ | TLSClientCert is the path to the client certificate for TLS communication. |  | Optional: \{\} <br /> |
| `tlsClientKey` _string_ | TLSClientKey is the path to the private key for TLS communication. |  | Optional: \{\} <br /> |
| `tlsServerName` _string_ | TLSServerName is the SNI host name to use when connecting via TLS. |  | Optional: \{\} <br /> |
| `tlsSkipVerify` _boolean_ | TLSSkipVerify disables verification of TLS certificates.<br />Using this option is highly discouraged and decreases security. |  | Optional: \{\} <br /> |


#### UnsealConfig



UnsealConfig defines the auto-unseal configuration for an OpenBaoCluster.
If omitted, defaults to "static" mode managed by the operator.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type specifies the seal type.<br />Defaults to "static". | static | Enum: [static awskms gcpckms azurekeyvault transit kmip kms ocikms pkcs11] <br /> |
| `static` _[StaticSealConfig](#staticsealconfig)_ | Static configures the static seal type.<br />Optional when Type is "static" (operator provides defaults if omitted). |  | Optional: \{\} <br /> |
| `transit` _[TransitSealConfig](#transitsealconfig)_ | Transit configures the Transit seal type.<br />Required when Type is "transit". |  | Optional: \{\} <br /> |
| `awskms` _[AWSKMSSealConfig](#awskmssealconfig)_ | AWSKMS configures the AWS KMS seal type.<br />Required when Type is "awskms". |  | Optional: \{\} <br /> |
| `azureKeyVault` _[AzureKeyVaultSealConfig](#azurekeyvaultsealconfig)_ | AzureKeyVault configures the Azure Key Vault seal type.<br />Required when Type is "azurekeyvault". |  | Optional: \{\} <br /> |
| `gcpCloudKMS` _[GCPCloudKMSSealConfig](#gcpcloudkmssealconfig)_ | GCPCloudKMS configures the GCP Cloud KMS seal type.<br />Required when Type is "gcpckms". |  | Optional: \{\} <br /> |
| `kmip` _[KMIPSealConfig](#kmipsealconfig)_ | KMIP configures the KMIP seal type.<br />Required when Type is "kmip". |  | Optional: \{\} <br /> |
| `kms` _[KMSPluginSealConfig](#kmspluginsealconfig)_ | KMS configures a plugin-backed KMS seal.<br />Required when Type is "kms". |  | Optional: \{\} <br /> |
| `ocikms` _[OCIKMSSealConfig](#ocikmssealconfig)_ | OCIKMS configures the OCI KMS seal type.<br />Required when Type is "ocikms". |  | Optional: \{\} <br /> |
| `pkcs11` _[PKCS11SealConfig](#pkcs11sealconfig)_ | PKCS11 configures the PKCS#11 seal type.<br />Required when Type is "pkcs11". |  | Optional: \{\} <br /> |
| `credentialsSecretRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | CredentialsSecretRef references a Secret containing provider credentials<br />(for example AWS access keys, GCP credentials.json, Azure client-secret keys,<br />OCI SDK config for authTypeAPIKey mode, or plugin-backed KMS runtime files).<br />If using Workload Identity (IRSA, GKE WI, Azure MSI), this can be omitted.<br />For static unseal, the Secret supplies the original key instead of an<br />operator-generated key. Its files are mounted at /etc/bao/unseal; the<br />default currentKey reads its "key" entry. The operator does not create,<br />modify, or take ownership of this Secret. A cluster that already owns an<br />operator-generated unseal key keeps that key and ignores this reference.<br />The Secret must exist in the same namespace as the OpenBaoCluster.<br />Cross-namespace references are not allowed for security reasons. |  | Optional: \{\} <br /> |


#### UpdateStrategyType

_Underlying type:_ _string_

UpdateStrategyType defines the type of update strategy to use.

_Validation:_
- Enum: [RollingUpdate BlueGreen]

_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)
- [UpgradeConfig](#upgradeconfig)

| Field | Description |
| --- | --- |
| `RollingUpdate` | UpdateStrategyRollingUpdate uses a rolling update strategy (default).<br /> |
| `BlueGreen` | UpdateStrategyBlueGreen uses a blue/green deployment strategy.<br /> |


#### UpgradeConfig



UpgradeConfig defines configuration for upgrade operations.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `image` _string_ | Image is the container image to use for upgrade operations.<br />This image is used by Kubernetes Jobs created during upgrades (for example, blue/green<br />cluster orchestration actions). The executor runs inside the tenant namespace and<br />authenticates to OpenBao using a projected ServiceAccount token (JWT auth).<br />If not specified, OPERATOR_UPGRADE_IMAGE can supply a complete image reference, including a digest.<br />Otherwise, defaults to "&lt;repo&gt;:X.Y.Z" where &lt;repo&gt; is derived from OPERATOR_UPGRADE_IMAGE_REPOSITORY<br />(default: "ghcr.io/kubebao/openbao-upgrade") and the tag matches OPERATOR_VERSION. |  | Optional: \{\} <br /> |
| `preUpgradeSnapshot` _boolean_ | PreUpgradeSnapshot, when true, triggers a backup before any upgrade.<br />When enabled, the upgrade manager will create a backup using the backup<br />configuration (spec.backup.target, spec.backup.image, etc.) and<br />wait for it to complete before proceeding with the upgrade.<br />If the backup fails, the upgrade will be blocked and a Degraded condition<br />will be set with Reason=PreUpgradeBackupFailed.<br />Requires spec.backup to be configured with target, image, and<br />authentication (jwtAuthRole or tokenSecretRef). |  | Optional: \{\} <br /> |
| `jwtAuthRole` _string_ | JWTAuthRole is the name of the JWT Auth role configured in OpenBao<br />for upgrade executor Jobs. The executor authenticates with a projected<br />ServiceAccount token from &lt;cluster-name&gt;-upgrade-serviceaccount.<br />The role must be configured in OpenBao and must grant the permissions<br />required by the selected upgrade strategy, including:<br />- "read" capability on sys/health<br />- "sudo" and "update" capability on sys/step-down<br />- "read" capability on sys/storage/raft/autopilot/state<br />- for Blue/Green, raft join/configuration/remove-peer/promote/demote operations<br />The role must bind to the upgrade ServiceAccount (&lt;cluster-name&gt;-upgrade-serviceaccount),<br />which is automatically created by the operator.<br />If OIDC is enabled during initial SelfInit bootstrap and this field is<br />empty, a default role named "openbao-operator-upgrade" will be created.<br />For already-initialized clusters, configure this role explicitly or keep<br />the default role created during initial bootstrap.<br />This is the supported authentication mechanism for built-in upgrade orchestration. |  | Optional: \{\} <br /> |
| `strategy` _[UpdateStrategyType](#updatestrategytype)_ | Strategy defines the update strategy to use. | RollingUpdate | Enum: [RollingUpdate BlueGreen] <br /> |
| `requests` _[UpgradeRequestConfig](#upgraderequestconfig)_ | Requests defines explicit one-shot operator requests for the current<br />upgrade workflow. The operator acts only when a request value changes. |  | Optional: \{\} <br /> |
| `blueGreen` _[BlueGreenConfig](#bluegreenconfig)_ | BlueGreen configures the behavior when Strategy is BlueGreen. |  | Optional: \{\} <br /> |


#### UpgradeProgress



UpgradeProgress tracks the state of an in-progress upgrade.



_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `targetVersion` _string_ | TargetVersion is the version being upgraded to. |  |  |
| `fromVersion` _string_ | FromVersion is the version being upgraded from. |  |  |
| `startedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | StartedAt is when the upgrade began. |  | Optional: \{\} <br /> |
| `currentPartition` _integer_ | CurrentPartition is the current StatefulSet partition value. |  |  |
| `completedPods` _integer array_ | CompletedPods lists ordinals of pods that have been successfully upgraded. |  | Optional: \{\} <br /> |
| `lastStepDownTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastStepDownTime records when the last leader step-down was performed. |  | Optional: \{\} <br /> |
| `failure` _[ControllerErrorStatus](#controllererrorstatus)_ | Failure is the structured rolling-upgrade failure status.<br />When Failure.Reason is non-empty, the upgrade is considered failed. |  | Optional: \{\} <br /> |


#### UpgradeRequestConfig



UpgradeRequestConfig defines one-shot operator requests for upgrade workflows.



_Appears in:_
- [UpgradeConfig](#upgradeconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `retry` _string_ | Retry requests a retry of the current failed rolling upgrade when changed<br />to a new non-empty value.<br />The operator compares this value against status.upgradeRequests.lastHandledRetry<br />and acts only when the value changes. Recommended value is an RFC3339<br />timestamp string. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `promote` _string_ | Promote requests promotion of a held blue/green upgrade when changed to a<br />new non-empty value while spec.upgrade.blueGreen.autoPromote=false.<br />The operator compares this value against<br />status.upgradeRequests.lastHandledPromote and acts only when the value<br />changes. Recommended value is an RFC3339 timestamp string. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `rollback` _string_ | Rollback requests a manual abort or rollback of the current blue/green<br />upgrade when changed to a new non-empty value.<br />The operator compares this value against<br />status.upgradeRequests.lastHandledRollback and acts only when the value<br />changes. Recommended value is an RFC3339 timestamp string. |  | MinLength: 1 <br />Optional: \{\} <br /> |


#### UpgradeRequestStatus



UpgradeRequestStatus tracks which explicit upgrade request values have already been handled.



_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `lastHandledRetry` _string_ | LastHandledRetry is the last observed spec.upgrade.requests.retry value<br />that the operator has handled. |  | Optional: \{\} <br /> |
| `lastHandledPromote` _string_ | LastHandledPromote is the last observed spec.upgrade.requests.promote<br />value that the operator has handled. |  | Optional: \{\} <br /> |
| `lastHandledRollback` _string_ | LastHandledRollback is the last observed spec.upgrade.requests.rollback<br />value that the operator has handled. |  | Optional: \{\} <br /> |


#### ValidationHookConfig



ValidationHookConfig defines a user-supplied validation Job.



_Appears in:_
- [VerificationConfig](#verificationconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `image` _string_ | Image is the container image for the validation job. |  | MinLength: 1 <br /> |
| `command` _string array_ | Command is the command to run. |  | Optional: \{\} <br /> |
| `args` _string array_ | Args are arguments passed to the command. |  | Optional: \{\} <br /> |
| `timeoutSeconds` _integer_ | TimeoutSeconds is the job timeout (default: 300s). | 300 | Optional: \{\} <br /> |


#### VerificationConfig



VerificationConfig allows defining custom health checks before promotion.



_Appears in:_
- [BlueGreenConfig](#bluegreenconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `minSyncDuration` _string_ | MinSyncDuration ensures the Green cluster stays healthy as a non-voter<br />for at least this duration before promotion (e.g., "5m"). |  | Optional: \{\} <br /> |
| `prePromotionHook` _[ValidationHookConfig](#validationhookconfig)_ | PrePromotionHook specifies a Job template to run before promoting Green.<br />The job must complete successfully (exit 0) for promotion to proceed.<br />If the job fails, the operator either aborts or rolls back automatically<br />when blueGreen.autoRollback.onValidationFailure is enabled; otherwise it<br />holds for manual resolution.<br />The operator assigns the hook a stable identity for each upgrade attempt.<br />It does not recreate a missing Job after Job creation is committed. |  | Optional: \{\} <br /> |


#### WorkloadControllerStatus



WorkloadControllerStatus holds status owned by the workload controller.



_Appears in:_
- [OpenBaoClusterStatus](#openbaoclusterstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `policyRevision` _string_ | PolicyRevision identifies the last complete built-in policy bundle verified<br />or repaired successfully. This records reconciliation progress, not authorization. |  | Optional: \{\} <br /> |
| `policyReconciliation` _[PolicyReconciliationStatus](#policyreconciliationstatus)_ | PolicyReconciliation records individual policy observations and retry scheduling. |  | Optional: \{\} <br /> |
| `lastError` _[ControllerErrorStatus](#controllererrorstatus)_ | LastError is the last workload-controller error observed for this cluster. |  | Optional: \{\} <br /> |


#### WorkloadHardeningConfig



WorkloadHardeningConfig configures optional workload hardening features.



_Appears in:_
- [OpenBaoClusterSpec](#openbaoclusterspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `appArmorEnabled` _boolean_ | AppArmorEnabled controls whether the operator sets AppArmor profiles on<br />generated Pods and Jobs. Some Kubernetes environments do not support AppArmor;<br />this is opt-in to avoid scheduling failures. |  | Optional: \{\} <br /> |


#### WorkloadIdentityConfig



WorkloadIdentityConfig configures cloud workload identity metadata for backup and restore workloads.



_Appears in:_
- [BackupTarget](#backuptarget)
- [RestoreTest](#restoretest)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serviceAccountAnnotations` _object (keys:string, values:string)_ | ServiceAccountAnnotations are merged into the generated backup or restore ServiceAccount.<br />An existing restore executor ServiceAccount must already have these annotations;<br />the operator validates them without modifying that account.<br />This is typically used for provider-specific bindings such as GKE Workload Identity<br />or webhook-based AWS/Azure workload identity integrations. |  | Optional: \{\} <br /> |
| `podLabels` _object (keys:string, values:string)_ | PodLabels are merged into the generated backup or restore Job pod template.<br />This is typically used for provider-specific selectors such as Azure Workload Identity.<br />Operator-managed labels take precedence if the same key is specified here. |  | Optional: \{\} <br /> |
