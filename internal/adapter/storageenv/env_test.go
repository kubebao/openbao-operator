package storageenv

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const boolTrueString = "true"

func envMap(envVars []corev1.EnvVar) map[string]string {
	values := make(map[string]string, len(envVars))
	for _, envVar := range envVars {
		values[envVar.Name] = envVar.Value
	}
	return values
}

func TestEffectiveJWTRole(t *testing.T) {
	if got := EffectiveJWTRole("custom-role", true, "default-role"); got != "custom-role" {
		t.Fatalf("configured role should win, got %q", got)
	}
	if got := EffectiveJWTRole("", true, "default-role"); got != "default-role" {
		t.Fatalf("default role should apply when oidc enabled, got %q", got)
	}
	if got := EffectiveJWTRole("", false, "default-role"); got != "" {
		t.Fatalf("empty role expected when oidc disabled, got %q", got)
	}
}

func TestAppendProviderEnvVars_S3(t *testing.T) {
	target := openbaov1alpha1.BackupTarget{
		Provider:           constants.StorageProviderS3,
		Region:             "",
		UsePathStyle:       true,
		InsecureSkipVerify: true,
	}

	env := envMap(AppendProviderEnvVars(nil, target))
	if env[constants.EnvBackupRegion] != constants.DefaultS3Region {
		t.Fatalf("BACKUP_REGION = %q, want %q", env[constants.EnvBackupRegion], constants.DefaultS3Region)
	}
	if env[constants.EnvBackupUsePathStyle] != boolTrueString {
		t.Fatalf("BACKUP_USE_PATH_STYLE = %q, want true", env[constants.EnvBackupUsePathStyle])
	}
	if env[constants.EnvBackupInsecureSkipVerify] != boolTrueString {
		t.Fatalf("BACKUP_INSECURE_SKIP_VERIFY = %q, want true", env[constants.EnvBackupInsecureSkipVerify])
	}
}

func TestAppendProviderEnvVars_GCSAzure(t *testing.T) {
	gcsEnv := envMap(AppendProviderEnvVars(nil, openbaov1alpha1.BackupTarget{
		Provider: constants.StorageProviderGCS,
		GCS:      &openbaov1alpha1.GCSTargetConfig{Project: "test-project"},
	}))
	if gcsEnv[constants.EnvBackupGCSProject] != "test-project" {
		t.Fatalf("BACKUP_GCS_PROJECT = %q, want test-project", gcsEnv[constants.EnvBackupGCSProject])
	}

	azureEnv := envMap(AppendProviderEnvVars(nil, openbaov1alpha1.BackupTarget{
		Provider: constants.StorageProviderAzure,
		Azure: &openbaov1alpha1.AzureTargetConfig{
			StorageAccount: "acct",
			Container:      "container",
		},
	}))
	if azureEnv[constants.EnvBackupAzureStorageAccount] != "acct" {
		t.Fatalf("BACKUP_AZURE_STORAGE_ACCOUNT = %q, want acct", azureEnv[constants.EnvBackupAzureStorageAccount])
	}
	if azureEnv[constants.EnvBackupAzureContainer] != "container" {
		t.Fatalf("BACKUP_AZURE_CONTAINER = %q, want container", azureEnv[constants.EnvBackupAzureContainer])
	}
}

func TestAppendRestoreProviderEnvVars(t *testing.T) {
	s3Env := envMap(AppendRestoreProviderEnvVars(nil, openbaov1alpha1.BackupTarget{
		Provider:     constants.StorageProviderS3,
		Region:       "eu-west-1",
		UsePathStyle: true,
	}))
	if s3Env[constants.EnvRestoreRegion] != "eu-west-1" {
		t.Fatalf("RESTORE_REGION = %q, want eu-west-1", s3Env[constants.EnvRestoreRegion])
	}
	if s3Env[constants.EnvRestoreUsePathStyle] != boolTrueString {
		t.Fatalf("RESTORE_USE_PATH_STYLE = %q, want true", s3Env[constants.EnvRestoreUsePathStyle])
	}

	gcsEnv := envMap(AppendRestoreProviderEnvVars(nil, openbaov1alpha1.BackupTarget{
		Provider: constants.StorageProviderGCS,
	}))
	if _, exists := gcsEnv[constants.EnvRestoreRegion]; exists {
		t.Fatalf("did not expect %s for non-S3 provider", constants.EnvRestoreRegion)
	}
}

func TestAppendAuthEnvVars(t *testing.T) {
	jwtEnv := envMap(AppendAuthEnvVars(nil, "jwt-role", true))
	if jwtEnv[constants.EnvBackupJWTAuthRole] != "jwt-role" {
		t.Fatalf("BACKUP_JWT_AUTH_ROLE = %q, want jwt-role", jwtEnv[constants.EnvBackupJWTAuthRole])
	}
	if jwtEnv[constants.EnvBackupAuthMethod] != constants.BackupAuthMethodJWT {
		t.Fatalf("BACKUP_AUTH_METHOD = %q, want %q", jwtEnv[constants.EnvBackupAuthMethod], constants.BackupAuthMethodJWT)
	}

	tokenEnv := envMap(AppendAuthEnvVars(nil, "", true))
	if tokenEnv[constants.EnvBackupAuthMethod] != constants.BackupAuthMethodToken {
		t.Fatalf("BACKUP_AUTH_METHOD = %q, want %q", tokenEnv[constants.EnvBackupAuthMethod], constants.BackupAuthMethodToken)
	}
}
