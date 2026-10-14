---
title: OpenBaoRestore API
description: Fields, defaults, and validation for the OpenBaoRestore API.
eyebrow: Reference · Generated API
weight: 2
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
- [OpenBaoRestore](#openbaorestore)



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
- [UnsealConfig](../openbaocluster/#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `region` _string_ | Region is the AWS region where the encryption key lives. |  | MinLength: 1 <br /> |
| `kmsKeyID` _string_ | KMSKeyID is the AWS KMS key ID or ARN to use for encryption and decryption.<br />An alias in the format "alias/key-alias-name" may also be used. |  | MinLength: 1 <br /> |
| `endpoint` _string_ | Endpoint is the KMS API endpoint to be used for AWS KMS requests.<br />Useful when connecting to KMS over a VPC Endpoint. |  | Optional: \{\} <br /> |
| `accessKey` _string_ | AccessKey is the AWS access key ID to use.<br />Note: It is strongly recommended to use CredentialsSecretRef or Workload Identity (IRSA) instead. |  | Optional: \{\} <br /> |
| `secretKey` _string_ | SecretKey is the AWS secret access key to use.<br />Note: It is strongly recommended to use CredentialsSecretRef or Workload Identity (IRSA) instead. |  | Optional: \{\} <br /> |
| `sessionToken` _string_ | SessionToken specifies the AWS session token. |  | Optional: \{\} <br /> |


#### AzureKeyVaultSealConfig



AzureKeyVaultSealConfig configures the Azure Key Vault seal type.
See: https://openbao.org/docs/configuration/seal/azurekeyvault/



_Appears in:_
- [UnsealConfig](../openbaocluster/#unsealconfig)

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


#### BackupTarget



BackupTarget describes a generic, cloud-agnostic object storage destination.



_Appears in:_
- [BackupSchedule](../openbaocluster/#backupschedule)
- [RestoreSource](#restoresource)

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


#### GCPCloudKMSSealConfig



GCPCloudKMSSealConfig configures the GCP Cloud KMS seal type.
See: https://openbao.org/docs/configuration/seal/gcpckms/



_Appears in:_
- [UnsealConfig](../openbaocluster/#unsealconfig)

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


#### InitContainerConfig



InitContainerConfig configures the init container used to render OpenBao configuration.
The init container is responsible for rendering the final config.hcl from a template
using environment variables such as HOSTNAME and POD_IP.

The operator relies on this init container to render config.hcl at runtime. Disabling
the init container is not supported and will be rejected by validation.



_Appears in:_
- [OpenBaoClusterSpec](../openbaocluster/#openbaoclusterspec)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether the init container is used to render the configuration.<br />The operator requires the init container; disabling it is not supported. | true | Optional: \{\} <br /> |
| `image` _string_ | Image is the container image to use for the init container.<br />If not specified, OPERATOR_INIT_IMAGE can supply a complete image reference, including a digest.<br />Otherwise, defaults to "&lt;repo&gt;:X.Y.Z" where &lt;repo&gt; is derived from OPERATOR_INIT_IMAGE_REPOSITORY<br />(default: "ghcr.io/kubebao/openbao-init") and the tag matches OPERATOR_VERSION. |  | Optional: \{\} <br /> |


#### KMIPSealConfig



KMIPSealConfig configures the KMIP seal type.
See: https://openbao.org/docs/configuration/seal/kmip/



_Appears in:_
- [UnsealConfig](../openbaocluster/#unsealconfig)

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
- [UnsealConfig](../openbaocluster/#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `pluginName` _string_ | PluginName is the name of the plugin registered through a matching<br />plugin "kms" stanza. OpenBao uses this value as the seal stanza label. |  | MinLength: 1 <br /> |
| `config` _object (keys:string, values:string)_ | Config contains plugin-specific seal configuration rendered as string<br />attributes inside seal "&lt;pluginName&gt;". Keys must be valid HCL identifiers.<br />Values are stored in the OpenBaoCluster resource; use file paths to<br />credentialsSecretRef-mounted files for sensitive material instead of inline<br />secrets. |  | MaxProperties: 64 <br />Optional: \{\} <br /> |


#### OCIKMSSealConfig



OCIKMSSealConfig configures the OCI KMS seal type.
See: https://openbao.org/docs/configuration/seal/ocikms/



_Appears in:_
- [UnsealConfig](../openbaocluster/#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `keyID` _string_ | KeyID is the OCID of the master encryption key. |  | MinLength: 1 <br /> |
| `cryptoEndpoint` _string_ | CryptoEndpoint is the OCI KMS crypto endpoint. |  | MinLength: 1 <br /> |
| `managementEndpoint` _string_ | ManagementEndpoint is the OCI KMS management endpoint. |  | MinLength: 1 <br /> |
| `authTypeAPIKey` _boolean_ | AuthTypeAPIKey enables OCI API key authentication through an OCI SDK config file.<br />When false or omitted, OpenBao uses the default OCI principal flow for the runtime<br />environment, such as instance principal. |  | Optional: \{\} <br /> |
| `disabled` _boolean_ | Disabled disables this seal configuration, for example during seal migration. |  | Optional: \{\} <br /> |


#### OpenBaoRestore



OpenBaoRestore represents a request to restore an OpenBao cluster from a snapshot.
This resource is immutable after creation - it acts as a "job request".





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `openbao.org/v1alpha1` | | |
| `kind` _string_ | `OpenBaoRestore` | | |
| `spec` _[OpenBaoRestoreSpec](#openbaorestorespec)_ |  |  |  |
| `status` _[OpenBaoRestoreStatus](#openbaorestorestatus)_ |  |  |  |


#### OpenBaoRestoreSpec



OpenBaoRestoreSpec defines the desired state for a restore operation.
An OpenBaoRestore acts as a "job request" - it is immutable after creation.



_Appears in:_
- [OpenBaoRestore](#openbaorestore)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `cleanupAfterSeconds` _integer_ | CleanupAfterSeconds delays disposable target cleanup after snapshot application is confirmed. |  | Maximum: 86400 <br />Minimum: 0 <br />Optional: \{\} <br /> |
| `clusterTemplate` _[RestoreClusterTemplate](#restoreclustertemplate)_ | ClusterTemplate creates a new single-voter target. Existing targets are rejected. |  | Optional: \{\} <br /> |
| `targetLifecycle` _[RestoreTargetLifecycle](#restoretargetlifecycle)_ | TargetLifecycle retains recovery targets or deletes disposable test resources. |  | Enum: [Retain Disposable] <br />Optional: \{\} <br /> |
| `cluster` _string_ | Cluster is the name of the OpenBaoCluster to restore INTO.<br />Must be in the request namespace. ClusterTemplate requires a new name. |  | MinLength: 1 <br /> |
| `source` _[RestoreSource](#restoresource)_ | Source defines where the snapshot comes from. |  |  |
| `serviceAccountName` _string_ | ServiceAccountName selects an existing executor ServiceAccount in this<br />namespace. The operator does not create, modify, or delete this account.<br />When omitted, it creates &lt;cluster-name&gt;-restore-serviceaccount.<br />JWT authentication must authorize the selected account. Fresh targets add<br />its subject to their generated restore role before snapshot submission. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$` <br />Optional: \{\} <br /> |
| `jwtAuthRole` _string_ | JWTAuthRole is the name of the JWT Auth role configured in OpenBao<br />for restore operations. When set, the restore executor will use JWT Auth<br />(projected ServiceAccount token) instead of a static token.<br />The role must be configured in OpenBao and must grant the "update" capability on<br />sys/storage/raft/snapshot. To support force: true, it must also grant "update" on<br />sys/storage/raft/snapshot-force. The role must bind to the restore ServiceAccount<br />selected by serviceAccountName, or &lt;cluster-name&gt;-restore-serviceaccount<br />when serviceAccountName is omitted, in the cluster namespace.<br />If this field is empty and the target OpenBaoCluster has OIDC enabled,<br />the operator will default to using the "openbao-operator-restore" role. |  | Optional: \{\} <br /> |
| `tokenSecretRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | TokenSecretRef optionally references a Secret containing an OpenBao API<br />token to use for restore operations (fallback method).<br />The Secret must exist in the same namespace as the OpenBaoRestore.<br />Cross-namespace references are not allowed for security reasons.<br />The token must have permission to update sys/storage/raft/snapshot. To support<br />force: true, it must also have permission to update<br />sys/storage/raft/snapshot-force.<br />If JWTAuthRole is set, this field is ignored in favor of JWT Auth. |  | Optional: \{\} <br /> |
| `image` _string_ | Image is the container image to use for restore operations.<br />Defaults to the same image used for backup operations if not specified.<br />If the target OpenBaoCluster has image verification enabled, the operator will verify this image and pin the restore Job to the verified digest. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `force` _boolean_ | Force uses OpenBao's force-restore endpoint. This bypasses verification that<br />the snapshot is compatible with the target cluster's Shamir or auto-unseal<br />configuration. It also skips the controller checks that require the target<br />cluster to be initialized and not upgrading.<br />Use this break-glass option only when the normal verified restore cannot run<br />and the snapshot source and target seal compatibility have been validated by<br />another trusted process. | false | Optional: \{\} <br /> |
| `overrideOperationLock` _boolean_ | OverrideOperationLock allows the restore controller to clear an active cluster<br />operation lock (upgrade/backup) and proceed with restore. This is a break-glass<br />escape hatch intended for disaster recovery.<br />For safety, this requires force: true. When used, the controller emits a Warning<br />event and records a Condition on the OpenBaoRestore. | false | Optional: \{\} <br /> |


#### OpenBaoRestoreStatus



OpenBaoRestoreStatus defines the observed state of OpenBaoRestore.



_Appears in:_
- [OpenBaoRestore](#openbaorestore)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `target` _[RestoreTargetStatus](#restoretargetstatus)_ | Target binds the fresh cluster and storage to this request. |  | Optional: \{\} <br /> |
| `submissionClaim` _[RestoreSubmissionClaim](#restoresubmissionclaim)_ | SubmissionClaim reserves the only permitted snapshot submission. |  | Optional: \{\} <br /> |
| `restart` _[RestoreRestartStatus](#restorerestartstatus)_ | Restart records managed workload recovery after administrator Resume. |  | Optional: \{\} <br /> |
| `administratorDisposition` _[RestoreAdministratorDisposition](#restoreadministratordisposition)_ | AdministratorDisposition records an operation-bound recovery acknowledgement. |  | Enum: [Resume Abandon] <br />Optional: \{\} <br /> |
| `phase` _[RestorePhase](#restorephase)_ | Phase represents the current phase of the restore operation. | Pending | Enum: [Pending Validating Running Completed Failed Unknown] <br /> |
| `startTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | StartTime is when the restore operation started. |  | Optional: \{\} <br /> |
| `completionTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | CompletionTime is when the restore operation completed (success or failure). |  | Optional: \{\} <br /> |
| `execution` _[RestoreExecutionStatus](#restoreexecutionstatus)_ | Execution records the stable operation identity and durable lifecycle receipts. |  | Optional: \{\} <br /> |
| `snapshotKey` _string_ | SnapshotKey is the key of the snapshot that was restored. |  | Optional: \{\} <br /> |
| `snapshotSize` _integer_ | SnapshotSize is the size of the restored snapshot in bytes. |  | Optional: \{\} <br /> |
| `message` _string_ | Message provides additional details about the current phase. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions represent the latest available observations of the restore's state. |  | Optional: \{\} <br /> |


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
- [UnsealConfig](../openbaocluster/#unsealconfig)

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
- [OpenBaoClusterSpec](../openbaocluster/#openbaoclusterspec)
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
- [OpenBaoConfiguration](../openbaocluster/#openbaoconfiguration)
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
- [OpenBaoClusterSpec](../openbaocluster/#openbaoclusterspec)
- [ReadReplicaTemplateConfig](../openbaocluster/#readreplicatemplateconfig)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `labels` _object (keys:string, values:string)_ | Labels are merged into the generated OpenBao Pod template labels.<br />Operator-managed labels take precedence if the same key is specified here. |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations are merged into the generated OpenBao Pod template annotations.<br />Operator-managed annotations take precedence if the same key is specified here. |  | Optional: \{\} <br /> |


#### RestoreAdministratorDisposition

_Underlying type:_ _string_

RestoreAdministratorDisposition records the administrator's recovery decision.



_Appears in:_
- [OpenBaoRestoreStatus](#openbaorestorestatus)

| Field | Description |
| --- | --- |
| `Resume` |  |
| `Abandon` |  |


#### RestoreClusterTemplate



RestoreClusterTemplate is the supported fresh recovery target profile.
Administrators must prepare the destination namespace network boundary.



_Appears in:_
- [OpenBaoRestoreSpec](#openbaorestorespec)
- [RestoreTest](../openbaocluster/#restoretest)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version must match the administrator-observed snapshot source version. |  | Enum: [2.7.0 2.7.1] <br /> |
| `image` _string_ | Image defaults to the version-derived image. |  | Optional: \{\} <br /> |
| `storage` _[StorageConfig](#storageconfig)_ |  |  |  |
| `tls` _[TLSConfig](#tlsconfig)_ |  |  |  |
| `unseal` _[UnsealConfig](../openbaocluster/#unsealconfig)_ | Unseal uses the same provider configuration as OpenBaoCluster. The target<br />must be able to decrypt the snapshot with the original seal key material. |  |  |
| `serviceAccount` _[ServiceAccountConfig](#serviceaccountconfig)_ | ServiceAccount configures credentials supplied through workload identity. |  | Optional: \{\} <br /> |
| `podMetadata` _[PodMetadataConfig](#podmetadataconfig)_ | PodMetadata supplies provider-specific workload identity metadata. |  | Optional: \{\} <br /> |
| `plugins` _[Plugin](#plugin) array_ | Plugins declares KMS seal plugins required by Unseal. OpenBao 2.7 requires<br />plugins for AWS, Azure, GCP, OCI, and PKCS#11 seals. |  | Optional: \{\} <br /> |
| `configuration` _[RestoreTargetConfiguration](#restoretargetconfiguration)_ | Configuration configures KMS plugin behavior for the target server. |  | Optional: \{\} <br /> |
| `resources` _[ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#resourcerequirements-v1-core)_ |  |  | Optional: \{\} <br /> |
| `initContainer` _[InitContainerConfig](#initcontainerconfig)_ |  |  | Optional: \{\} <br /> |
| `imagePullSecrets` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core) array_ |  |  | MaxItems: 2 <br />Optional: \{\} <br /> |


#### RestoreExecutionResult

_Underlying type:_ _string_

RestoreExecutionResult is the persisted terminal result of a restore Job.

_Validation:_
- Enum: [Succeeded Failed]

_Appears in:_
- [RestoreExecutionStatus](#restoreexecutionstatus)

| Field | Description |
| --- | --- |
| `Succeeded` | RestoreExecutionResultSucceeded indicates the restore Job succeeded.<br /> |
| `Failed` | RestoreExecutionResultFailed indicates the restore Job failed.<br /> |


#### RestoreExecutionStage

_Underlying type:_ _string_

RestoreExecutionStage identifies the durable execution boundary reached by a restore.

_Validation:_
- Enum: [Prepared Committed Created TerminalObserved FollowThroughComplete Unknown]

_Appears in:_
- [RestoreExecutionStatus](#restoreexecutionstatus)

| Field | Description |
| --- | --- |
| `Prepared` | RestoreExecutionStagePrepared indicates validation and resource preparation<br />completed, but Job creation has not been committed.<br /> |
| `Committed` | RestoreExecutionStageCommitted indicates the controller durably committed to<br />one Job creation attempt. A missing Job after this point is ambiguous and is<br />not recreated automatically.<br /> |
| `Created` | RestoreExecutionStageCreated indicates the controller persisted the created Job identity.<br /> |
| `TerminalObserved` | RestoreExecutionStageTerminalObserved indicates the controller persisted the terminal Job result.<br /> |
| `FollowThroughComplete` | RestoreExecutionStageFollowThroughComplete indicates post-restore voter and<br />read-replica recovery completed.<br /> |
| `Unknown` | RestoreExecutionStageUnknown indicates the controller cannot prove whether<br />the committed execution ran.<br /> |


#### RestoreExecutionStatus



RestoreExecutionStatus records the identity and durable receipts for one restore execution.



_Appears in:_
- [OpenBaoRestoreStatus](#openbaorestorestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `targetUID` _[UID](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#uid-types-pkg)_ | TargetUID prevents a replacement cluster from inheriting this execution. |  | Optional: \{\} <br /> |
| `operationID` _string_ | OperationID identifies this immutable restore execution. |  |  |
| `stage` _[RestoreExecutionStage](#restoreexecutionstage)_ | Stage is the latest durable execution boundary observed by the controller. |  | Enum: [Prepared Committed Created TerminalObserved FollowThroughComplete Unknown] <br /> |
| `jobName` _string_ | JobName is the expected restore Job name for this execution. |  |  |
| `jobUID` _[UID](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#uid-types-pkg)_ | JobUID is the UID returned for the created restore Job. |  | Optional: \{\} <br /> |
| `preparedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | PreparedAt is when validation and execution preparation completed. |  | Optional: \{\} <br /> |
| `committedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | CommittedAt is when the controller committed to one Job creation attempt. |  | Optional: \{\} <br /> |
| `createdAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | CreatedAt is when the controller persisted the created Job receipt. |  | Optional: \{\} <br /> |
| `terminalResult` _[RestoreExecutionResult](#restoreexecutionresult)_ | TerminalResult is the persisted terminal Job result. |  | Enum: [Succeeded Failed] <br />Optional: \{\} <br /> |
| `terminalObservedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | TerminalObservedAt is when the controller persisted the terminal Job result. |  | Optional: \{\} <br /> |
| `followThroughCompletedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | FollowThroughCompletedAt is when post-restore recovery completed. |  | Optional: \{\} <br /> |


#### RestorePhase

_Underlying type:_ _string_

RestorePhase represents the current phase of a restore operation.

_Validation:_
- Enum: [Pending Validating Running Completed Failed Unknown]

_Appears in:_
- [OpenBaoRestoreStatus](#openbaorestorestatus)

| Field | Description |
| --- | --- |
| `Pending` | RestorePhasePending indicates the restore has been created but not yet started.<br /> |
| `Validating` | RestorePhaseValidating indicates the controller is validating preconditions.<br /> |
| `Running` | RestorePhaseRunning indicates the restore job is executing.<br /> |
| `Completed` | RestorePhaseCompleted indicates the restore completed successfully.<br /> |
| `Failed` | RestorePhaseFailed indicates the restore failed.<br /> |
| `Unknown` | RestorePhaseUnknown indicates the controller cannot determine whether the<br />destructive restore operation ran. The controller does not retry an<br />execution in this phase.<br /> |


#### RestoreRestartPod



RestoreRestartPod binds a managed restart to an original Pod and its StatefulSet.



_Appears in:_
- [RestoreRestartStatus](#restorerestartstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the original Pod name. |  |  |
| `uid` _[UID](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#uid-types-pkg)_ | UID identifies the original Pod; a different UID marks a replacement. |  |  |
| `statefulSetUID` _[UID](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#uid-types-pkg)_ | StatefulSetUID identifies the StatefulSet that owned the original Pod. |  |  |


#### RestoreRestartStatus



RestoreRestartStatus records Resume before restarting workloads. Replacement
Pod UIDs provide retry evidence; this status does not prove snapshot application.



_Appears in:_
- [OpenBaoRestoreStatus](#openbaorestorestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `pods` _[RestoreRestartPod](#restorerestartpod) array_ | Pods contains the original voters and read replicas. |  | MinItems: 1 <br /> |
| `completedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | CompletedAt records that all bound Pods were replaced and became ready. |  | Optional: \{\} <br /> |


#### RestoreSource



RestoreSource defines where the snapshot comes from.



_Appears in:_
- [OpenBaoRestoreSpec](#openbaorestorespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `expectedClusterID` _string_ | ExpectedClusterID is the native ID observed on the snapshot source.<br />Required for a fresh target; supplied metadata is an administrator assertion. |  | MaxLength: 128 <br />Optional: \{\} <br /> |
| `expectedVersion` _string_ | ExpectedVersion is the source version observed when taking the snapshot. |  | MaxLength: 64 <br />Optional: \{\} <br /> |
| `expectedDigest` _string_ | ExpectedDigest pins the staged bytes before submission. |  | Pattern: `^sha256:[a-f0-9]\{64\}$` <br />Optional: \{\} <br /> |
| `expectedSize` _integer_ |  |  | Maximum: 8.589934592e+09 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `target` _[BackupTarget](#backuptarget)_ | Target reuses BackupTarget for storage connection details.<br />This includes endpoint, bucket, region, credentials, etc. |  |  |
| `key` _string_ | Key is the full path to the snapshot object in the bucket.<br />For example, "clusters/prod/2025-10-14-120000.snap". |  | MinLength: 1 <br /> |


#### RestoreSubmissionClaim



RestoreSubmissionClaim is a one-way submission reservation. A persisted claim
never authorizes a restarted executor to submit again.



_Appears in:_
- [OpenBaoRestoreStatus](#openbaorestorestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `podUID` _[UID](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#uid-types-pkg)_ | PodUID identifies the executor Pod that won the claim. |  |  |
| `targetPodName` _string_ | TargetPodName is the OpenBao Pod that received the snapshot. |  |  |
| `targetPodUID` _[UID](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#uid-types-pkg)_ | TargetPodUID identifies the OpenBao Pod that received the snapshot. |  |  |
| `targetPodIP` _string_ | TargetPodIP is the address the executor connected to. |  |  |
| `targetContainerID` _string_ | TargetContainerID identifies the OpenBao container that received the snapshot. |  |  |
| `digest` _string_ | Digest is sha256: followed by the lowercase digest of the staged snapshot bytes. |  | Pattern: `^sha256:[a-f0-9]\{64\}$` <br /> |
| `size` _integer_ | Size is the staged snapshot size in bytes. |  | Maximum: 8.589934592e+09 <br />Minimum: 1 <br /> |
| `claimedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | ClaimedAt records when submission was reserved. |  |  |


#### RestoreTargetCleanup

_Underlying type:_ _string_

RestoreTargetCleanup describes deletion of a disposable target's resources.



_Appears in:_
- [RestoreTargetStatus](#restoretargetstatus)

| Field | Description |
| --- | --- |
| `Pending` |  |
| `Complete` |  |
| `Failed` |  |


#### RestoreTargetConfiguration



RestoreTargetConfiguration exposes server settings needed by KMS seal plugins.



_Appears in:_
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `plugin` _[PluginConfig](#pluginconfig)_ | Plugin controls downloads, registration, and file ownership for Plugins.<br />Set autoDownload to true to download declared OCI plugins on startup. |  | Optional: \{\} <br /> |


#### RestoreTargetLifecycle

_Underlying type:_ _string_

RestoreTargetLifecycle determines whether a fresh target is retained or deleted.



_Appears in:_
- [OpenBaoRestoreSpec](#openbaorestorespec)

| Field | Description |
| --- | --- |
| `Retain` |  |
| `Disposable` |  |


#### RestoreTargetStatus



RestoreTargetStatus records the single creation attempt and original identities.
Cleanup reports Kubernetes object deletion, never physical process fencing.



_Appears in:_
- [OpenBaoRestoreStatus](#openbaorestorestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `reservedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | ReservedAt records the single target creation attempt. |  |  |
| `uid` _[UID](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#uid-types-pkg)_ | UID identifies the target OpenBaoCluster created for this request. |  | Optional: \{\} <br /> |
| `dataPVCUID` _[UID](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#uid-types-pkg)_ | DataPVCUID identifies the target's original data PersistentVolumeClaim. |  | Optional: \{\} <br /> |
| `bootstrapClusterID` _string_ | BootstrapClusterID is the native cluster ID of the empty target before restore. |  | Optional: \{\} <br /> |
| `appliedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | AppliedAt records when the target reported the expected source identity. |  | Optional: \{\} <br /> |
| `cleanup` _[RestoreTargetCleanup](#restoretargetcleanup)_ | Cleanup is Pending, Complete, or Failed. A failed ownership check preserves<br />the refused resource; cleanup can still delete the original bound cluster. |  | Enum: [Pending Complete Failed] <br />Optional: \{\} <br /> |


#### ServiceAccountConfig



ServiceAccountConfig configures the ServiceAccount used by OpenBao pods.



_Appears in:_
- [OpenBaoClusterSpec](../openbaocluster/#openbaoclusterspec)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name overrides the generated ServiceAccount name.<br />If not specified, defaults to "&lt;cluster-name&gt;-serviceaccount". |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations to add to the ServiceAccount.<br />Useful for cloud provider Workload Identity (e.g. eks.amazonaws.com/role-arn). |  | Optional: \{\} <br /> |


#### StaticSealConfig



StaticSealConfig configures the static seal type.
This is the default seal type managed by the operator.
See: https://openbao.org/docs/configuration/seal/static/



_Appears in:_
- [UnsealConfig](../openbaocluster/#unsealconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `currentKey` _string_ | CurrentKey is the path to the static unseal key file.<br />Defaults to "file:///etc/bao/unseal/key" (operator-managed). |  | Optional: \{\} <br /> |
| `currentKeyID` _string_ | CurrentKeyID is the identifier for the current unseal key.<br />Defaults to "operator-generated-v1" (operator-managed). |  | Optional: \{\} <br /> |


#### StorageConfig



StorageConfig captures storage-related configuration for the StatefulSet.



_Appears in:_
- [OpenBaoClusterSpec](../openbaocluster/#openbaoclusterspec)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `size` _string_ | Size is the requested persistent volume size, for example "10Gi". |  | MinLength: 1 <br /> |
| `storageClassName` _string_ | StorageClassName is an optional StorageClass for the PVCs. |  | Optional: \{\} <br /> |


#### TLSConfig



TLSConfig captures TLS configuration for an OpenBaoCluster.



_Appears in:_
- [OpenBaoClusterSpec](../openbaocluster/#openbaoclusterspec)
- [RestoreClusterTemplate](#restoreclustertemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether TLS is enabled for the cluster. |  | Required: \{\} <br /> |
| `mode` _[TLSMode](#tlsmode)_ | Mode controls who manages the certificate lifecycle. | OperatorManaged | Enum: [OperatorManaged External ACME] <br />Optional: \{\} <br /> |
| `acme` _[ACMEConfig](#acmeconfig)_ | ACME configures settings when Mode is 'ACME'. |  | Optional: \{\} <br /> |
| `rotationPeriod` _string_ | RotationPeriod is a duration string (for example, "720h") controlling certificate rotation.<br />Only used when Mode is OperatorManaged. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `extraSANs` _string array_ | ExtraSANs lists additional subject alternative names for server certificates.<br />In OperatorManaged mode, the operator includes these names when issuing the certificate.<br />In External mode, the operator requires the supplied certificate to contain them.<br />Values that parse as IP addresses are treated as IP SANs; all other values are DNS SANs. |  | Optional: \{\} <br /> |


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


#### TransitSealConfig



TransitSealConfig configures the Transit seal type.
See: https://openbao.org/docs/configuration/seal/transit/



_Appears in:_
- [UnsealConfig](../openbaocluster/#unsealconfig)

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
- [OpenBaoClusterSpec](../openbaocluster/#openbaoclusterspec)
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


#### WorkloadIdentityConfig



WorkloadIdentityConfig configures cloud workload identity metadata for backup and restore workloads.



_Appears in:_
- [BackupTarget](#backuptarget)
- [RestoreTest](../openbaocluster/#restoretest)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serviceAccountAnnotations` _object (keys:string, values:string)_ | ServiceAccountAnnotations are merged into the generated backup or restore ServiceAccount.<br />An existing restore executor ServiceAccount must already have these annotations;<br />the operator validates them without modifying that account.<br />This is typically used for provider-specific bindings such as GKE Workload Identity<br />or webhook-based AWS/Azure workload identity integrations. |  | Optional: \{\} <br /> |
| `podLabels` _object (keys:string, values:string)_ | PodLabels are merged into the generated backup or restore Job pod template.<br />This is typically used for provider-specific selectors such as Azure Workload Identity.<br />Operator-managed labels take precedence if the same key is specified here. |  | Optional: \{\} <br /> |
