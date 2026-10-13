package constants

// Environment variable keys shared between the operator and helper binaries.
const (
	// Kubernetes metadata
	EnvHostname = "HOSTNAME"
	EnvPodIP    = "POD_IP"

	// OpenBao configuration and service registration
	EnvBaoAPIAddr      = "BAO_API_ADDR"
	EnvBaoK8sPodName   = "BAO_K8S_POD_NAME"
	EnvBaoK8sNamespace = "BAO_K8S_NAMESPACE"

	// Backup executor (cluster context)
	EnvClusterNamespace = "CLUSTER_NAMESPACE"
	EnvClusterName      = "CLUSTER_NAME"
	EnvClusterReplicas  = "CLUSTER_REPLICAS"
	EnvStatefulSetName  = "STATEFULSET_NAME" // StatefulSet name for pod discovery (may include revision for Blue/Green)

	// Upgrade executor
	EnvUpgradeAction        = "UPGRADE_ACTION"
	EnvUpgradeJWTAuthRole   = "UPGRADE_JWT_AUTH_ROLE"
	EnvUpgradeBlueReplicas  = "UPGRADE_BLUE_REPLICAS"
	EnvUpgradeBlueRevision  = "UPGRADE_BLUE_REVISION"
	EnvUpgradeGreenRevision = "UPGRADE_GREEN_REVISION"
	EnvUpgradeSyncThreshold = "UPGRADE_SYNC_THRESHOLD"
	EnvUpgradeTimeout       = "UPGRADE_TIMEOUT"

	// Autopilot executor
	EnvAutopilotJWTAuthRole             = "AUTOPILOT_JWT_AUTH_ROLE"
	EnvAutopilotMinQuorum               = "AUTOPILOT_MIN_QUORUM"
	EnvAutopilotCleanupDeadServers      = "AUTOPILOT_CLEANUP_DEAD_SERVERS"
	EnvAutopilotDeadServerThreshold     = "AUTOPILOT_DEAD_SERVER_THRESHOLD"
	EnvAutopilotServerStabilizationTime = "AUTOPILOT_SERVER_STABILIZATION_TIME"
	EnvAutopilotLastContactThreshold    = "AUTOPILOT_LAST_CONTACT_THRESHOLD"
	EnvAutopilotMaxTrailingLogs         = "AUTOPILOT_MAX_TRAILING_LOGS"

	// Smart Client Limits
	EnvClientQPS                            = "OPENBAO_CLIENT_QPS"
	EnvClientBurst                          = "OPENBAO_CLIENT_BURST"
	EnvClientCircuitBreakerFailureThreshold = "OPENBAO_CLIENT_CB_FAILURE_THRESHOLD"
	EnvClientCircuitBreakerOpenDuration     = "OPENBAO_CLIENT_CB_OPEN_DURATION"

	// Backup executor (S3/object storage target)
	EnvBackupProvider       = "BACKUP_PROVIDER" // Storage provider: s3, gcs, azure
	EnvBackupEndpoint       = "BACKUP_ENDPOINT"
	EnvBackupBucket         = "BACKUP_BUCKET"
	EnvBackupPathPrefix     = "BACKUP_PATH_PREFIX"
	EnvBackupFilenamePrefix = "BACKUP_FILENAME_PREFIX"
	EnvBackupKey            = "BACKUP_KEY"

	// S3-specific backup config
	EnvBackupRegion             = "BACKUP_REGION"
	EnvBackupUsePathStyle       = "BACKUP_USE_PATH_STYLE"
	EnvBackupInsecureSkipVerify = "BACKUP_INSECURE_SKIP_VERIFY"

	// GCS-specific backup config
	EnvBackupGCSProject = "BACKUP_GCS_PROJECT"

	// Azure-specific backup config
	EnvBackupAzureStorageAccount = "BACKUP_AZURE_STORAGE_ACCOUNT"
	EnvBackupAzureContainer      = "BACKUP_AZURE_CONTAINER"

	EnvBackupPartSize    = "BACKUP_PART_SIZE"
	EnvBackupConcurrency = "BACKUP_CONCURRENCY"

	// Backup executor (credentials + auth)
	EnvBackupAuthMethod  = "BACKUP_AUTH_METHOD"
	EnvBackupJWTAuthRole = "BACKUP_JWT_AUTH_ROLE"

	EnvBackupTokenPath       = "BACKUP_TOKEN_PATH"       // #nosec G101 -- This is an environment variable name constant, not a credential
	EnvBackupCredentialsPath = "BACKUP_CREDENTIALS_PATH" // #nosec G101 -- This is an environment variable name constant, not a credential
	EnvJWTTokenPath          = "JWT_TOKEN_PATH"          // #nosec G101 -- This is an environment variable name constant, not a credential
	EnvTLSCAPath             = "TLS_CA_PATH"
	EnvTLSServerName         = "TLS_SERVER_NAME"

	EnvBackupCredentialsSecretName      = "BACKUP_CREDENTIALS_SECRET_NAME"      // #nosec G101 -- This is an environment variable name constant, not a credential
	EnvBackupCredentialsSecretNamespace = "BACKUP_CREDENTIALS_SECRET_NAMESPACE" // #nosec G101 -- This is an environment variable name constant, not a credential
	EnvBackupTokenSecretName            = "BACKUP_TOKEN_SECRET_NAME"
	EnvBackupTokenSecretNamespace       = "BACKUP_TOKEN_SECRET_NAMESPACE"

	EnvAWSRoleARN              = "AWS_ROLE_ARN"
	EnvAWSWebIdentityTokenFile = "AWS_WEB_IDENTITY_TOKEN_FILE" // #nosec G101 -- This is an environment variable name constant, not a credential

	// Restore executor
	EnvRestoreKey          = "RESTORE_KEY"
	EnvRestoreBucket       = "RESTORE_BUCKET"
	EnvRestoreEndpoint     = "RESTORE_ENDPOINT"
	EnvRestoreRegion       = "RESTORE_REGION"
	EnvRestoreUsePathStyle = "RESTORE_USE_PATH_STYLE"
	EnvRestoreForce        = "RESTORE_FORCE"

	// Operator
	EnvOperatorVersion                      = "OPERATOR_VERSION"
	EnvOpenBaoJWTAudience                   = "OPENBAO_JWT_AUDIENCE"
	EnvOpenBaoJWTAuthStrategy               = "OPENBAO_JWT_AUTH_STRATEGY"
	EnvTenantNamespacePodSecurityLabelsMode = "OPENBAO_TENANT_NAMESPACE_POD_SECURITY_LABELS_MODE"

	// Operator-managed image repositories
	EnvOperatorBackupImageRepo  = "OPERATOR_BACKUP_IMAGE_REPOSITORY"
	EnvOperatorUpgradeImageRepo = "OPERATOR_UPGRADE_IMAGE_REPOSITORY"
	EnvOperatorInitImageRepo    = "OPERATOR_INIT_IMAGE_REPOSITORY"

	// Complete helper image references take precedence over repository/version defaults.
	EnvOperatorBackupImage  = "OPERATOR_BACKUP_IMAGE"
	EnvOperatorUpgradeImage = "OPERATOR_UPGRADE_IMAGE"
	EnvOperatorInitImage    = "OPERATOR_INIT_IMAGE"
)

// Default image repositories.
const (
	// DefaultBackupImageRepository is the default image repository used for backup executor.
	DefaultBackupImageRepository = "ghcr.io/kubebao/openbao-backup"

	// DefaultUpgradeImageRepository is the default image repository used for upgrade executor.
	DefaultUpgradeImageRepository = "ghcr.io/kubebao/openbao-upgrade"

	// DefaultInitImageRepository is the default image repository used for the init (config rendering + wrapper) container.
	DefaultInitImageRepository = "ghcr.io/kubebao/openbao-init"
)

// Backup authentication method values.
const (
	BackupAuthMethodJWT   = "jwt"
	BackupAuthMethodToken = "token"
)

// Platform values.
const (
	PlatformAuto       = "auto"
	PlatformKubernetes = "kubernetes"
	PlatformOpenShift  = "openshift"
)

// Storage provider values.
const (
	StorageProviderS3    = "s3"
	StorageProviderGCS   = "gcs"
	StorageProviderAzure = "azure"
)
