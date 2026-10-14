package storageenv

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	"github.com/kubebao/openbao-operator/internal/port/blobstore"
)

const (
	storageProviderS3    = "s3"
	storageProviderGCS   = "gcs"
	storageProviderAzure = "azure"

	envBackupInsecureSkipVerify  = "BACKUP_INSECURE_SKIP_VERIFY"
	envBackupRegion              = "BACKUP_REGION"
	envBackupUsePathStyle        = "BACKUP_USE_PATH_STYLE"
	envBackupGCSProject          = "BACKUP_GCS_PROJECT"
	envBackupAzureStorageAccount = "BACKUP_AZURE_STORAGE_ACCOUNT"
	envBackupAzureContainer      = "BACKUP_AZURE_CONTAINER"
	envBackupJWTAuthRole         = "BACKUP_JWT_AUTH_ROLE"
	envBackupAuthMethod          = "BACKUP_AUTH_METHOD"

	backupAuthMethodJWT   = "jwt"
	backupAuthMethodToken = "token"
)

// ResolveDestination maps an API target to its effective storage destination.
func ResolveDestination(target openbaov1alpha1.BackupTarget) blobstore.Destination {
	azureContainer := ""
	if target.Azure != nil {
		azureContainer = target.Azure.Container
	}
	return blobstore.ResolveDestination(blobstore.Destination{
		Provider: target.Provider,
		Endpoint: target.Endpoint,
		Bucket:   target.Bucket,
		Region:   target.Region,
	}, azureContainer)
}

// EffectiveJWTRole returns the configured role or a default when OIDC is enabled.
func EffectiveJWTRole(configuredRole string, oidcEnabled bool, defaultRole string) string {
	return portauth.EffectiveJWTRole(configuredRole, oidcEnabled, defaultRole)
}

// AppendProviderEnvVars appends provider-specific BACKUP_* env vars.
func AppendProviderEnvVars(envVars []corev1.EnvVar, target openbaov1alpha1.BackupTarget) []corev1.EnvVar {
	destination := ResolveDestination(target)
	if target.InsecureSkipVerify {
		envVars = append(envVars, corev1.EnvVar{
			Name:  envBackupInsecureSkipVerify,
			Value: "true",
		})
	}

	switch destination.Provider {
	case storageProviderS3:
		envVars = append(envVars, corev1.EnvVar{Name: envBackupRegion, Value: destination.Region})
		envVars = append(envVars, corev1.EnvVar{Name: envBackupUsePathStyle, Value: fmt.Sprintf("%t", target.UsePathStyle)})
	case storageProviderGCS:
		if target.GCS != nil && target.GCS.Project != "" {
			envVars = append(envVars, corev1.EnvVar{Name: envBackupGCSProject, Value: target.GCS.Project})
		}
	case storageProviderAzure:
		if target.Azure != nil {
			if target.Azure.StorageAccount != "" {
				envVars = append(envVars, corev1.EnvVar{Name: envBackupAzureStorageAccount, Value: target.Azure.StorageAccount})
			}
			if target.Azure.Container != "" {
				envVars = append(envVars, corev1.EnvVar{Name: envBackupAzureContainer, Value: target.Azure.Container})
			}
		}
	}

	return envVars
}

// AppendRestoreProviderEnvVars appends restore-only provider env vars.
func AppendRestoreProviderEnvVars(envVars []corev1.EnvVar, target openbaov1alpha1.BackupTarget) []corev1.EnvVar {
	destination := ResolveDestination(target)
	if destination.Provider != storageProviderS3 {
		return envVars
	}

	envVars = append(envVars, corev1.EnvVar{
		Name:  constants.EnvRestoreRegion,
		Value: destination.Region,
	})
	envVars = append(envVars, corev1.EnvVar{
		Name:  constants.EnvRestoreUsePathStyle,
		Value: fmt.Sprintf("%t", target.UsePathStyle),
	})
	return envVars
}

// AppendAuthEnvVars appends JWT/token auth env vars used by backup executor logic.
func AppendAuthEnvVars(envVars []corev1.EnvVar, jwtRole string, hasTokenSecret bool) []corev1.EnvVar {
	if jwtRole != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  envBackupJWTAuthRole,
			Value: jwtRole,
		})
		envVars = append(envVars, corev1.EnvVar{
			Name:  envBackupAuthMethod,
			Value: backupAuthMethodJWT,
		})
		return envVars
	}

	if hasTokenSecret {
		envVars = append(envVars, corev1.EnvVar{
			Name:  envBackupAuthMethod,
			Value: backupAuthMethodToken,
		})
	}
	return envVars
}
