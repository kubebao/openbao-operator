package jobenv

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	adapterauth "github.com/kubebao/openbao-operator/internal/adapter/auth"
	"github.com/kubebao/openbao-operator/internal/adapter/storageenv"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

// Options configures backup job environment variable construction.
type Options struct {
	BackupKey             string
	FilenamePrefix        string
	ClientConfig          portopenbao.ClientConfig
	TargetStatefulSetName string
}

// BuildEnvVars builds the environment variables for a backup job.
func BuildEnvVars(cluster *openbaov1alpha1.OpenBaoCluster, opts Options, tokenFilePath string) []corev1.EnvVar {
	backupCfg := cluster.Spec.Backup

	// Use provided StatefulSet name or default to cluster.Name.
	// This allows callers (e.g., upgrade managers) to specify the correct StatefulSet
	// without embedding upgrade-strategy-specific logic in the backup builder.
	statefulSetName := opts.TargetStatefulSetName
	if statefulSetName == "" {
		statefulSetName = cluster.Name
	}

	destination := storageenv.ResolveDestination(backupCfg.Target)

	env := []corev1.EnvVar{
		{Name: constants.EnvClusterNamespace, Value: cluster.Namespace},
		{Name: constants.EnvClusterName, Value: cluster.Name},
		{Name: constants.EnvStatefulSetName, Value: statefulSetName},
		{Name: constants.EnvClusterReplicas, Value: fmt.Sprintf("%d", cluster.Spec.Replicas)},
		{Name: constants.EnvBackupProvider, Value: destination.Provider},
		{Name: constants.EnvBackupEndpoint, Value: destination.Endpoint},
		{Name: constants.EnvBackupBucket, Value: destination.Bucket},
		{Name: constants.EnvBackupPathPrefix, Value: backupCfg.Target.PathPrefix},
	}

	env = storageenv.AppendProviderEnvVars(env, backupCfg.Target)

	// AWS Role ARN for Web Identity (IRSA)
	if destination.Provider == constants.StorageProviderS3 && backupCfg.Target.RoleARN != "" {
		env = append(env, corev1.EnvVar{Name: constants.EnvAWSRoleARN, Value: backupCfg.Target.RoleARN})
		env = append(env, corev1.EnvVar{Name: constants.EnvAWSWebIdentityTokenFile, Value: tokenFilePath})
	}

	// Add backup key if provided
	if opts.BackupKey != "" {
		env = append(env, corev1.EnvVar{Name: constants.EnvBackupKey, Value: opts.BackupKey})
	}

	// Add filename prefix for pre-upgrade backups (or custom prefix)
	if opts.FilenamePrefix != "" {
		env = append(env, corev1.EnvVar{Name: constants.EnvBackupFilenamePrefix, Value: opts.FilenamePrefix})
	}

	// S3 upload configuration (PartSize and Concurrency apply to all providers)
	if backupCfg.Target.PartSize > 0 {
		env = append(env, corev1.EnvVar{
			Name:  constants.EnvBackupPartSize,
			Value: fmt.Sprintf("%d", backupCfg.Target.PartSize),
		})
	}
	if backupCfg.Target.Concurrency > 0 {
		env = append(env, corev1.EnvVar{
			Name:  constants.EnvBackupConcurrency,
			Value: fmt.Sprintf("%d", backupCfg.Target.Concurrency),
		})
	}

	// Credentials secret reference
	// SECURITY: Do NOT pass cross-namespace references. Secrets must be in cluster.Namespace.
	if backupCfg.Target.CredentialsSecretRef != nil {
		env = append(env, corev1.EnvVar{
			Name:  constants.EnvBackupCredentialsSecretName,
			Value: backupCfg.Target.CredentialsSecretRef.Name,
		})
	}

	// JWT Auth configuration (preferred method)
	jwtRole := EffectiveBackupJWTRole(cluster)
	env = storageenv.AppendAuthEnvVars(env, jwtRole, false)
	if jwtRole != "" {
		env = append(env, corev1.EnvVar{
			Name:  constants.EnvOpenBaoJWTAuthStrategy,
			Value: portopenbao.NormalizeJWTAuthStrategyOrDefault(opts.ClientConfig.JWTAuthStrategy),
		})
	}

	// Token secret reference (fallback for token-based auth)
	// SECURITY: Do NOT pass cross-namespace references. Secrets must be in cluster.Namespace.
	if backupCfg.TokenSecretRef != nil {
		env = append(env, corev1.EnvVar{
			Name:  constants.EnvBackupTokenSecretName,
			Value: backupCfg.TokenSecretRef.Name,
		})
		// Only set auth method to token if JWT Auth is not configured.
		if jwtRole == "" {
			env = storageenv.AppendAuthEnvVars(env, "", true)
		}
	}

	// Smart Client Limits
	if opts.ClientConfig.RateLimitQPS > 0 {
		env = append(env, corev1.EnvVar{
			Name:  constants.EnvClientQPS,
			Value: fmt.Sprintf("%f", opts.ClientConfig.RateLimitQPS),
		})
	}
	if opts.ClientConfig.RateLimitBurst > 0 {
		env = append(env, corev1.EnvVar{
			Name:  constants.EnvClientBurst,
			Value: fmt.Sprintf("%d", opts.ClientConfig.RateLimitBurst),
		})
	}
	if opts.ClientConfig.CircuitBreakerFailureThreshold > 0 {
		env = append(env, corev1.EnvVar{
			Name:  constants.EnvClientCircuitBreakerFailureThreshold,
			Value: fmt.Sprintf("%d", opts.ClientConfig.CircuitBreakerFailureThreshold),
		})
	}
	if opts.ClientConfig.CircuitBreakerOpenDuration > 0 {
		env = append(env, corev1.EnvVar{
			Name:  constants.EnvClientCircuitBreakerOpenDuration,
			Value: opts.ClientConfig.CircuitBreakerOpenDuration.String(),
		})
	}

	return env
}

// OpenBaoJWTAudience returns the audience value used for projected OpenBao JWT tokens.
func OpenBaoJWTAudience() string {
	return adapterauth.OpenBaoJWTAudience()
}

// EffectiveBackupJWTRole returns the configured JWT role or defaults it
// when OIDC is enabled and the role is empty.
func EffectiveBackupJWTRole(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return storageenv.EffectiveJWTRole(
		cluster.Spec.Backup.JWTAuthRole,
		portauth.OperatorJWTBootstrapEnabled(cluster),
		portauth.RoleNameBackup,
	)
}
