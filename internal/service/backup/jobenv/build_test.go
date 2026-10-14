package jobenv

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func envMap(env []corev1.EnvVar) map[string]string {
	out := make(map[string]string, len(env))
	for _, v := range env {
		out[v.Name] = v.Value
	}
	return out
}

func TestEffectiveBackupJWTRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cluster *openbaov1alpha1.OpenBaoCluster
		want    string
	}{
		{
			name: "configured role takes precedence",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Backup:   &openbaov1alpha1.BackupSchedule{JWTAuthRole: "custom-role"},
					SelfInit: &openbaov1alpha1.SelfInitConfig{Enabled: true, OIDC: &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true}},
				},
			},
			want: "custom-role",
		},
		{
			name: "self init oidc enabled defaults role",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Backup:   &openbaov1alpha1.BackupSchedule{},
					SelfInit: &openbaov1alpha1.SelfInitConfig{Enabled: true, OIDC: &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true}},
				},
			},
			want: portauth.RoleNameBackup,
		},
		{
			name: "oidc without self init leaves empty role",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Backup:   &openbaov1alpha1.BackupSchedule{},
					SelfInit: &openbaov1alpha1.SelfInitConfig{Enabled: false, OIDC: &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true}},
				},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveBackupJWTRole(tt.cluster); got != tt.want {
				t.Fatalf("EffectiveBackupJWTRole()=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildEnvVars_S3TokenFallbackAndClientSettings(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "tenant-ns"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			Backup: &openbaov1alpha1.BackupSchedule{
				Target: openbaov1alpha1.BackupTarget{
					Provider:             constants.StorageProviderS3,
					Endpoint:             "https://s3.example.test",
					Bucket:               "backups",
					PathPrefix:           "operator",
					Region:               "eu-west-1",
					UsePathStyle:         true,
					RoleARN:              "arn:aws:iam::123:role/backup",
					PartSize:             10485760,
					Concurrency:          4,
					CredentialsSecretRef: &corev1.LocalObjectReference{Name: "storage-creds"},
				},
				TokenSecretRef: &corev1.LocalObjectReference{Name: "backup-token"},
			},
		},
	}

	env := BuildEnvVars(cluster, Options{
		BackupKey:             "key-123",
		FilenamePrefix:        "pre-upgrade",
		TargetStatefulSetName: "cluster-a-green",
		ClientConfig: portopenbao.ClientConfig{
			RateLimitQPS:                   3.5,
			RateLimitBurst:                 12,
			CircuitBreakerFailureThreshold: 7,
			CircuitBreakerOpenDuration:     45 * time.Second,
		},
	}, "/var/run/secrets/tokens/openbao-backup")
	got := envMap(env)

	want := map[string]string{
		constants.EnvClusterNamespace:                     "tenant-ns",
		constants.EnvClusterName:                          "cluster-a",
		constants.EnvStatefulSetName:                      "cluster-a-green",
		constants.EnvClusterReplicas:                      "3",
		constants.EnvBackupProvider:                       constants.StorageProviderS3,
		constants.EnvBackupEndpoint:                       "https://s3.example.test",
		constants.EnvBackupBucket:                         "backups",
		constants.EnvBackupPathPrefix:                     "operator",
		constants.EnvBackupRegion:                         "eu-west-1",
		constants.EnvBackupUsePathStyle:                   "true",
		constants.EnvAWSRoleARN:                           "arn:aws:iam::123:role/backup",
		constants.EnvAWSWebIdentityTokenFile:              "/var/run/secrets/tokens/openbao-backup",
		constants.EnvBackupCredentialsSecretName:          "storage-creds",
		constants.EnvBackupTokenSecretName:                "backup-token",
		constants.EnvBackupAuthMethod:                     "token",
		constants.EnvBackupPartSize:                       "10485760",
		constants.EnvBackupConcurrency:                    "4",
		constants.EnvBackupKey:                            "key-123",
		constants.EnvBackupFilenamePrefix:                 "pre-upgrade",
		constants.EnvClientQPS:                            "3.500000",
		constants.EnvClientBurst:                          "12",
		constants.EnvClientCircuitBreakerFailureThreshold: "7",
		constants.EnvClientCircuitBreakerOpenDuration:     "45s",
	}

	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Fatalf("env[%s]=%q, want %q", key, got[key], wantValue)
		}
	}

	if _, exists := got[constants.EnvBackupJWTAuthRole]; exists {
		t.Fatalf("did not expect %s in token fallback mode", constants.EnvBackupJWTAuthRole)
	}
}

func TestBuildEnvVars_OIDCDefaultsJWTAuthRole(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-b", Namespace: "tenant-ns"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
			SelfInit: &openbaov1alpha1.SelfInitConfig{Enabled: true, OIDC: &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true}},
			Backup: &openbaov1alpha1.BackupSchedule{
				Target:         openbaov1alpha1.BackupTarget{Bucket: "backups"},
				TokenSecretRef: &corev1.LocalObjectReference{Name: "backup-token"},
			},
		},
	}

	env := BuildEnvVars(cluster, Options{
		ClientConfig: portopenbao.ClientConfig{JWTAuthStrategy: portopenbao.JWTAuthStrategyStandard},
	}, "/tmp/token")
	got := envMap(env)

	if got[constants.EnvBackupJWTAuthRole] != portauth.RoleNameBackup {
		t.Fatalf("EnvBackupJWTAuthRole=%q, want %q", got[constants.EnvBackupJWTAuthRole], portauth.RoleNameBackup)
	}
	if got[constants.EnvBackupAuthMethod] != "jwt" {
		t.Fatalf("EnvBackupAuthMethod=%q, want jwt", got[constants.EnvBackupAuthMethod])
	}
	if got[constants.EnvOpenBaoJWTAuthStrategy] != portopenbao.JWTAuthStrategyStandard {
		t.Fatalf("EnvOpenBaoJWTAuthStrategy=%q, want standard", got[constants.EnvOpenBaoJWTAuthStrategy])
	}
	if got[constants.EnvBackupTokenSecretName] != "backup-token" {
		t.Fatalf("expected token secret name to remain set for fallback path")
	}
	if got[constants.EnvStatefulSetName] != "cluster-b" {
		t.Fatalf("expected statefulset fallback to cluster name, got %q", got[constants.EnvStatefulSetName])
	}
}

func TestBuildEnvVars_ProviderSpecificBranches(t *testing.T) {
	t.Parallel()

	t.Run("gcs provider", func(t *testing.T) {
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "gcs", Namespace: "ns"},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Replicas: 1,
				Backup: &openbaov1alpha1.BackupSchedule{
					Target: openbaov1alpha1.BackupTarget{
						Provider: constants.StorageProviderGCS,
						Bucket:   "gcs-bucket",
						GCS:      &openbaov1alpha1.GCSTargetConfig{Project: "proj-a"},
					},
				},
			},
		}
		got := envMap(BuildEnvVars(cluster, Options{}, "/tmp/token"))
		if got[constants.EnvBackupProvider] != constants.StorageProviderGCS {
			t.Fatalf("provider=%q, want gcs", got[constants.EnvBackupProvider])
		}
		if got[constants.EnvBackupGCSProject] != "proj-a" {
			t.Fatalf("gcs project=%q, want proj-a", got[constants.EnvBackupGCSProject])
		}
		if _, exists := got[constants.EnvBackupRegion]; exists {
			t.Fatalf("did not expect s3 region env for gcs provider")
		}
	})

	t.Run("azure provider", func(t *testing.T) {
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "azure", Namespace: "ns"},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Replicas: 1,
				Backup: &openbaov1alpha1.BackupSchedule{
					Target: openbaov1alpha1.BackupTarget{
						Provider: constants.StorageProviderAzure,
						Bucket:   "unused",
						Azure: &openbaov1alpha1.AzureTargetConfig{
							StorageAccount: "acct-a",
							Container:      "container-a",
						},
					},
				},
			},
		}
		got := envMap(BuildEnvVars(cluster, Options{}, "/tmp/token"))
		if got[constants.EnvBackupProvider] != constants.StorageProviderAzure {
			t.Fatalf("provider=%q, want azure", got[constants.EnvBackupProvider])
		}
		if got[constants.EnvBackupAzureStorageAccount] != "acct-a" {
			t.Fatalf("storage account=%q, want acct-a", got[constants.EnvBackupAzureStorageAccount])
		}
		if got[constants.EnvBackupAzureContainer] != "container-a" {
			t.Fatalf("container=%q, want container-a", got[constants.EnvBackupAzureContainer])
		}
		if got[constants.EnvBackupBucket] != "container-a" {
			t.Fatalf("bucket=%q, want effective container-a", got[constants.EnvBackupBucket])
		}
	})
}
