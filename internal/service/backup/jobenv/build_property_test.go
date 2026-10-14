package jobenv

import (
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/testutil/proptest"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"pgregory.net/rapid"
)

func TestEffectiveBackupJWTRoleProperties(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		configuredRole := spacedOptionalIdentifier().Draw(rt, "configured_role")
		selfInitEnabled := rapid.Bool().Draw(rt, "self_init_enabled")
		oidcEnabled := rapid.Bool().Draw(rt, "oidc_enabled")

		cluster := &openbaov1alpha1.OpenBaoCluster{
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Backup: &openbaov1alpha1.BackupSchedule{JWTAuthRole: configuredRole},
				SelfInit: &openbaov1alpha1.SelfInitConfig{
					Enabled: selfInitEnabled,
					OIDC:    &openbaov1alpha1.SelfInitOIDCConfig{Enabled: oidcEnabled},
				},
			},
		}

		want := strings.TrimSpace(configuredRole)
		if want == "" && selfInitEnabled && oidcEnabled {
			want = portauth.RoleNameBackup
		}
		if got := EffectiveBackupJWTRole(cluster); got != want {
			t.Fatalf("EffectiveBackupJWTRole() = %q, want %q", got, want)
		}
	})
}

func TestBuildEnvVarsProperties(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		cluster := backupClusterGenerator().Draw(rt, "cluster")
		opts := backupOptionsGenerator().Draw(rt, "opts")
		tokenFilePath := "/" + proptest.Identifier().Draw(rt, "token_file_path")

		env := BuildEnvVars(cluster, opts, tokenFilePath)
		got := envMap(env)
		if len(got) != len(env) {
			t.Fatalf("BuildEnvVars produced duplicate names: %+v", env)
		}

		assertCommonEnvVars(t, got, cluster, opts)
		assertProviderEnvVars(t, got, cluster.Spec.Backup.Target, tokenFilePath)
		assertOptionalEnvVars(t, got, cluster, opts)
		assertAuthEnvVars(t, got, cluster, opts)
		assertClientLimitEnvVars(t, got, opts.ClientConfig)
	})
}

func assertCommonEnvVars(
	t *testing.T,
	got map[string]string,
	cluster *openbaov1alpha1.OpenBaoCluster,
	opts Options,
) {
	t.Helper()

	statefulSetName := opts.TargetStatefulSetName
	if statefulSetName == "" {
		statefulSetName = cluster.Name
	}
	assertEnvValue(t, got, constants.EnvClusterNamespace, cluster.Namespace)
	assertEnvValue(t, got, constants.EnvClusterName, cluster.Name)
	assertEnvValue(t, got, constants.EnvStatefulSetName, statefulSetName)
	assertEnvValue(t, got, constants.EnvClusterReplicas, fmt.Sprintf("%d", cluster.Spec.Replicas))
	assertEnvValue(t, got, constants.EnvBackupProvider, effectiveProvider(cluster.Spec.Backup.Target.Provider))
	assertEnvValue(t, got, constants.EnvBackupEndpoint, cluster.Spec.Backup.Target.Endpoint)
	bucket := cluster.Spec.Backup.Target.Bucket
	if effectiveProvider(cluster.Spec.Backup.Target.Provider) == constants.StorageProviderAzure &&
		cluster.Spec.Backup.Target.Azure != nil && cluster.Spec.Backup.Target.Azure.Container != "" {
		bucket = cluster.Spec.Backup.Target.Azure.Container
	}
	assertEnvValue(t, got, constants.EnvBackupBucket, bucket)
	assertEnvValue(t, got, constants.EnvBackupPathPrefix, cluster.Spec.Backup.Target.PathPrefix)
}

func assertProviderEnvVars(
	t *testing.T,
	got map[string]string,
	target openbaov1alpha1.BackupTarget,
	tokenFilePath string,
) {
	t.Helper()

	if target.InsecureSkipVerify {
		assertEnvValue(t, got, constants.EnvBackupInsecureSkipVerify, "true")
	} else {
		assertEnvAbsent(t, got, constants.EnvBackupInsecureSkipVerify)
	}

	switch effectiveProvider(target.Provider) {
	case constants.StorageProviderS3:
		region := target.Region
		if region == "" {
			region = constants.DefaultS3Region
		}
		assertEnvValue(t, got, constants.EnvBackupRegion, region)
		assertEnvValue(t, got, constants.EnvBackupUsePathStyle, fmt.Sprintf("%t", target.UsePathStyle))
		if target.RoleARN != "" {
			assertEnvValue(t, got, constants.EnvAWSRoleARN, target.RoleARN)
			assertEnvValue(t, got, constants.EnvAWSWebIdentityTokenFile, tokenFilePath)
		} else {
			assertEnvAbsent(t, got, constants.EnvAWSRoleARN)
			assertEnvAbsent(t, got, constants.EnvAWSWebIdentityTokenFile)
		}
		assertEnvAbsent(t, got, constants.EnvBackupGCSProject)
		assertEnvAbsent(t, got, constants.EnvBackupAzureStorageAccount)
		assertEnvAbsent(t, got, constants.EnvBackupAzureContainer)
	case constants.StorageProviderGCS:
		assertEnvAbsent(t, got, constants.EnvBackupRegion)
		assertEnvAbsent(t, got, constants.EnvBackupUsePathStyle)
		assertEnvAbsent(t, got, constants.EnvAWSRoleARN)
		assertEnvAbsent(t, got, constants.EnvAWSWebIdentityTokenFile)
		if target.GCS != nil && target.GCS.Project != "" {
			assertEnvValue(t, got, constants.EnvBackupGCSProject, target.GCS.Project)
		} else {
			assertEnvAbsent(t, got, constants.EnvBackupGCSProject)
		}
		assertEnvAbsent(t, got, constants.EnvBackupAzureStorageAccount)
		assertEnvAbsent(t, got, constants.EnvBackupAzureContainer)
	case constants.StorageProviderAzure:
		assertEnvAbsent(t, got, constants.EnvBackupRegion)
		assertEnvAbsent(t, got, constants.EnvBackupUsePathStyle)
		assertEnvAbsent(t, got, constants.EnvAWSRoleARN)
		assertEnvAbsent(t, got, constants.EnvAWSWebIdentityTokenFile)
		assertEnvAbsent(t, got, constants.EnvBackupGCSProject)
		if target.Azure != nil && target.Azure.StorageAccount != "" {
			assertEnvValue(t, got, constants.EnvBackupAzureStorageAccount, target.Azure.StorageAccount)
		} else {
			assertEnvAbsent(t, got, constants.EnvBackupAzureStorageAccount)
		}
		if target.Azure != nil && target.Azure.Container != "" {
			assertEnvValue(t, got, constants.EnvBackupAzureContainer, target.Azure.Container)
		} else {
			assertEnvAbsent(t, got, constants.EnvBackupAzureContainer)
		}
	}
}

func assertOptionalEnvVars(
	t *testing.T,
	got map[string]string,
	cluster *openbaov1alpha1.OpenBaoCluster,
	opts Options,
) {
	t.Helper()

	assertEnvPresentWhenNonEmpty(t, got, constants.EnvBackupKey, opts.BackupKey)
	assertEnvPresentWhenNonEmpty(t, got, constants.EnvBackupFilenamePrefix, opts.FilenamePrefix)

	target := cluster.Spec.Backup.Target
	if target.PartSize > 0 {
		assertEnvValue(t, got, constants.EnvBackupPartSize, fmt.Sprintf("%d", target.PartSize))
	} else {
		assertEnvAbsent(t, got, constants.EnvBackupPartSize)
	}
	if target.Concurrency > 0 {
		assertEnvValue(t, got, constants.EnvBackupConcurrency, fmt.Sprintf("%d", target.Concurrency))
	} else {
		assertEnvAbsent(t, got, constants.EnvBackupConcurrency)
	}
	if target.CredentialsSecretRef != nil {
		assertEnvValue(t, got, constants.EnvBackupCredentialsSecretName, target.CredentialsSecretRef.Name)
	} else {
		assertEnvAbsent(t, got, constants.EnvBackupCredentialsSecretName)
	}
}

func assertAuthEnvVars(
	t *testing.T,
	got map[string]string,
	cluster *openbaov1alpha1.OpenBaoCluster,
	opts Options,
) {
	t.Helper()

	jwtRole := EffectiveBackupJWTRole(cluster)
	if jwtRole != "" {
		assertEnvValue(t, got, constants.EnvBackupJWTAuthRole, jwtRole)
		assertEnvValue(t, got, constants.EnvBackupAuthMethod, constants.BackupAuthMethodJWT)
		assertEnvValue(
			t,
			got,
			constants.EnvOpenBaoJWTAuthStrategy,
			portopenbao.NormalizeJWTAuthStrategyOrDefault(opts.ClientConfig.JWTAuthStrategy),
		)
	} else {
		assertEnvAbsent(t, got, constants.EnvBackupJWTAuthRole)
		assertEnvAbsent(t, got, constants.EnvOpenBaoJWTAuthStrategy)
		if cluster.Spec.Backup.TokenSecretRef != nil {
			assertEnvValue(t, got, constants.EnvBackupAuthMethod, constants.BackupAuthMethodToken)
		} else {
			assertEnvAbsent(t, got, constants.EnvBackupAuthMethod)
		}
	}

	if cluster.Spec.Backup.TokenSecretRef != nil {
		assertEnvValue(t, got, constants.EnvBackupTokenSecretName, cluster.Spec.Backup.TokenSecretRef.Name)
	} else {
		assertEnvAbsent(t, got, constants.EnvBackupTokenSecretName)
	}
}

func assertClientLimitEnvVars(t *testing.T, got map[string]string, cfg portopenbao.ClientConfig) {
	t.Helper()

	if cfg.RateLimitQPS > 0 {
		assertEnvValue(t, got, constants.EnvClientQPS, fmt.Sprintf("%f", cfg.RateLimitQPS))
	} else {
		assertEnvAbsent(t, got, constants.EnvClientQPS)
	}
	if cfg.RateLimitBurst > 0 {
		assertEnvValue(t, got, constants.EnvClientBurst, fmt.Sprintf("%d", cfg.RateLimitBurst))
	} else {
		assertEnvAbsent(t, got, constants.EnvClientBurst)
	}
	if cfg.CircuitBreakerFailureThreshold > 0 {
		assertEnvValue(
			t,
			got,
			constants.EnvClientCircuitBreakerFailureThreshold,
			fmt.Sprintf("%d", cfg.CircuitBreakerFailureThreshold),
		)
	} else {
		assertEnvAbsent(t, got, constants.EnvClientCircuitBreakerFailureThreshold)
	}
	if cfg.CircuitBreakerOpenDuration > 0 {
		assertEnvValue(t, got, constants.EnvClientCircuitBreakerOpenDuration, cfg.CircuitBreakerOpenDuration.String())
	} else {
		assertEnvAbsent(t, got, constants.EnvClientCircuitBreakerOpenDuration)
	}
}

func assertEnvValue(t *testing.T, got map[string]string, name, want string) {
	t.Helper()

	if got[name] != want {
		t.Fatalf("env[%s] = %q, want %q", name, got[name], want)
	}
}

func assertEnvPresentWhenNonEmpty(t *testing.T, got map[string]string, name, want string) {
	t.Helper()

	if want == "" {
		assertEnvAbsent(t, got, name)
		return
	}
	assertEnvValue(t, got, name, want)
}

func assertEnvAbsent(t *testing.T, got map[string]string, name string) {
	t.Helper()

	if value, ok := got[name]; ok {
		t.Fatalf("env[%s] = %q, want absent", name, value)
	}
}

func backupClusterGenerator() *rapid.Generator[*openbaov1alpha1.OpenBaoCluster] {
	return rapid.Custom(func(t *rapid.T) *openbaov1alpha1.OpenBaoCluster {
		backup := &openbaov1alpha1.BackupSchedule{
			Schedule:    "0 0 * * *",
			JWTAuthRole: spacedOptionalIdentifier().Draw(t, "jwt_auth_role"),
			Target:      backupTargetGenerator().Draw(t, "target"),
			TokenSecretRef: optionalLocalObjectReference().
				Draw(t, "token_secret_ref"),
		}
		return &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      proptest.Identifier().Draw(t, "name"),
				Namespace: proptest.Identifier().Draw(t, "namespace"),
			},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Replicas: int32(rapid.IntRange(0, 5).Draw(t, "replicas")),
				SelfInit: &openbaov1alpha1.SelfInitConfig{
					Enabled: rapid.Bool().Draw(t, "self_init_enabled"),
					OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
						Enabled: rapid.Bool().Draw(t, "oidc_enabled"),
					},
				},
				Backup: backup,
			},
		}
	})
}

func backupTargetGenerator() *rapid.Generator[openbaov1alpha1.BackupTarget] {
	return rapid.Custom(func(t *rapid.T) openbaov1alpha1.BackupTarget {
		target := openbaov1alpha1.BackupTarget{
			Provider:   providerGenerator().Draw(t, "provider"),
			Endpoint:   proptest.OptionalIdentifier().Draw(t, "endpoint"),
			Bucket:     proptest.Identifier().Draw(t, "bucket"),
			PathPrefix: proptest.OptionalIdentifier().Draw(t, "path_prefix"),
			CredentialsSecretRef: optionalLocalObjectReference().
				Draw(t, "credentials_secret_ref"),
			PartSize:           int64(rapid.IntRange(-1, 2).Draw(t, "part_size")),
			Concurrency:        int32(rapid.IntRange(-1, 2).Draw(t, "concurrency")),
			Region:             proptest.OptionalIdentifier().Draw(t, "region"),
			RoleARN:            proptest.OptionalIdentifier().Draw(t, "role_arn"),
			UsePathStyle:       rapid.Bool().Draw(t, "use_path_style"),
			InsecureSkipVerify: rapid.Bool().Draw(t, "insecure_skip_verify"),
		}
		if rapid.Bool().Draw(t, "has_gcs") {
			target.GCS = &openbaov1alpha1.GCSTargetConfig{
				Project: proptest.OptionalIdentifier().Draw(t, "gcs_project"),
			}
		}
		if rapid.Bool().Draw(t, "has_azure") {
			target.Azure = &openbaov1alpha1.AzureTargetConfig{
				StorageAccount: proptest.OptionalIdentifier().Draw(t, "azure_storage_account"),
				Container:      proptest.OptionalIdentifier().Draw(t, "azure_container"),
			}
		}
		return target
	})
}

func backupOptionsGenerator() *rapid.Generator[Options] {
	return rapid.Custom(func(t *rapid.T) Options {
		return Options{
			BackupKey:             proptest.OptionalIdentifier().Draw(t, "backup_key"),
			FilenamePrefix:        proptest.OptionalIdentifier().Draw(t, "filename_prefix"),
			TargetStatefulSetName: proptest.OptionalIdentifier().Draw(t, "statefulset_name"),
			ClientConfig: portopenbao.ClientConfig{
				JWTAuthStrategy:                jwtAuthStrategyGenerator().Draw(t, "jwt_auth_strategy"),
				RateLimitQPS:                   float64(rapid.IntRange(-1, 4).Draw(t, "rate_limit_qps")),
				RateLimitBurst:                 rapid.IntRange(-1, 4).Draw(t, "rate_limit_burst"),
				CircuitBreakerFailureThreshold: rapid.IntRange(-1, 4).Draw(t, "circuit_breaker_threshold"),
				CircuitBreakerOpenDuration: time.Duration(rapid.IntRange(-1, 4).
					Draw(t, "circuit_breaker_open_duration")) * time.Second,
			},
		}
	})
}

func providerGenerator() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{
		"",
		constants.StorageProviderS3,
		constants.StorageProviderGCS,
		constants.StorageProviderAzure,
	})
}

func jwtAuthStrategyGenerator() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{
		"",
		portopenbao.JWTAuthStrategyInline,
		portopenbao.JWTAuthStrategyStandard,
		"unsupported",
	})
}

func optionalLocalObjectReference() *rapid.Generator[*corev1.LocalObjectReference] {
	return rapid.Custom(func(t *rapid.T) *corev1.LocalObjectReference {
		if !rapid.Bool().Draw(t, "present") {
			return nil
		}
		return &corev1.LocalObjectReference{Name: proptest.Identifier().Draw(t, "name")}
	})
}

func spacedOptionalIdentifier() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		value := proptest.OptionalIdentifier().Draw(t, "value")
		if value == "" {
			return rapid.SampledFrom([]string{"", " ", "\t"}).Draw(t, "empty_spacing")
		}
		return rapid.SampledFrom([]string{"", " ", "\t"}).Draw(t, "prefix") +
			value +
			rapid.SampledFrom([]string{"", " ", "\n"}).Draw(t, "suffix")
	})
}

func effectiveProvider(provider string) string {
	if provider == "" {
		return constants.StorageProviderS3
	}
	return provider
}
