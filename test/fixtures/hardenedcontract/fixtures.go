package hardenedfixtures

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/hardenedcontract"
)

// Fixture describes one structurally valid Hardened cluster and the rule each
// enforcement layer is expected to report. AuthorizationOnly fixtures must be
// submitted as an unprivileged, otherwise-authorized API user.
type Fixture struct {
	Name              string
	AdmissionRule     hardenedcontract.RuleID
	RuntimeRule       hardenedcontract.RuleID
	AuthorizationOnly bool
	Configure         func(*openbaov1alpha1.OpenBaoCluster)
}

// NewValidCluster returns the common accepted baseline for all fixtures.
func NewValidCluster(namespace, name string) *openbaov1alpha1.OpenBaoCluster {
	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.4.4",
			Image:    "openbao/openbao:2.4.4",
			Replicas: 3,
			Profile:  openbaov1alpha1.ProfileHardened,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled: true,
				Mode:    openbaov1alpha1.TLSModeExternal,
			},
			Storage: openbaov1alpha1.StorageConfig{Size: "10Gi"},
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				Requests: []openbaov1alpha1.SelfInitRequest{
					{
						Name:      "health-check",
						Operation: openbaov1alpha1.SelfInitOperationRead,
						Path:      "sys/health",
					},
				},
			},
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "awskms",
				AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
					Region:   "eu-central-1",
					KMSKeyID: "alias/openbao-unseal",
				},
			},
		},
	}
}

// Fixtures returns one accepted baseline plus every known Hardened admission
// violation class. RuntimeRule is populated only for the intentionally smaller
// Go runtime/readiness subset.
func Fixtures() []Fixture {
	return []Fixture{
		{Name: "valid-baseline"},
		{
			Name: "backup-nested-s3-identity",
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				configureBackup(cluster)
				cluster.Spec.Backup.Target.RoleARN = ""
				cluster.Spec.Backup.Target.S3 = &openbaov1alpha1.S3TargetConfig{RoleARN: ptr.To("nested-role")}
			},
		},
		{
			Name:          "backup-suppressed-legacy-s3-identity",
			AdmissionRule: hardenedcontract.RuleStorageExplicitIdentity,
			RuntimeRule:   hardenedcontract.RuleStorageExplicitIdentity,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				configureBackup(cluster)
				cluster.Spec.Backup.Target.S3 = &openbaov1alpha1.S3TargetConfig{RoleARN: ptr.To("")}
			},
		},
		{
			Name:          "tls-disabled",
			AdmissionRule: hardenedcontract.RuleHardenedBaseline,
			RuntimeRule:   hardenedcontract.RuleRuntimeTLSEnabled,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.TLS.Enabled = false
			},
		},
		{
			Name:          "transit-inline-token",
			AdmissionRule: hardenedcontract.RuleTransitInlineToken,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
					Type: "transit",
					Transit: &openbaov1alpha1.TransitSealConfig{
						Address:   "https://infra-bao.example",
						Token:     "fixture-inline-token",
						KeyName:   "autounseal",
						MountPath: "transit/",
					},
				}
			},
		},
		{
			Name:          "aws-inline-secret-key",
			AdmissionRule: hardenedcontract.RuleAWSInlineSecretKey,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Unseal.AWSKMS.SecretKey = "fixture-secret-key"
			},
		},
		{
			Name:          "aws-inline-session-token",
			AdmissionRule: hardenedcontract.RuleAWSInlineSessionToken,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Unseal.AWSKMS.SessionToken = "fixture-session-token"
			},
		},
		{
			Name:          "azure-inline-client-secret",
			AdmissionRule: hardenedcontract.RuleAzureInlineClientSecret,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
					Type: "azurekeyvault",
					AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
						VaultName:    "fixture-vault",
						KeyName:      "fixture-key",
						ClientSecret: "fixture-client-secret",
					},
				}
			},
		},
		{
			Name:          "pkcs11-inline-pin",
			AdmissionRule: hardenedcontract.RulePKCS11InlinePIN,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
					Type: "pkcs11",
					PKCS11: &openbaov1alpha1.PKCS11SealConfig{
						Lib:      "/usr/lib/libpkcs11.so",
						Slot:     "0",
						PIN:      "fixture-pin",
						KeyLabel: "openbao",
					},
				}
			},
		},
		{
			Name:          "kms-plugin-inline-config",
			AdmissionRule: hardenedcontract.RuleKMSPluginInlineConfig,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
					Type: "kms",
					KMS: &openbaov1alpha1.KMSPluginSealConfig{
						PluginName: "corp-kms",
						Config:     map[string]string{"token": "fixture-token"},
					},
				}
			},
		},
		{
			Name:          "image-verification-disabled",
			AdmissionRule: hardenedcontract.RuleImageVerificationEnabled,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.ImageVerification = &openbaov1alpha1.ImageVerificationConfig{
					Enabled:       false,
					FailurePolicy: "Block",
				}
			},
		},
		{
			Name:          "operator-image-verification-disabled",
			AdmissionRule: hardenedcontract.RuleOperatorImageVerificationEnabled,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.OperatorImageVerification = &openbaov1alpha1.ImageVerificationConfig{
					Enabled:       false,
					FailurePolicy: "Block",
				}
			},
		},
		{
			Name:          "image-verification-warn",
			AdmissionRule: hardenedcontract.RuleImageVerificationFailurePolicy,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.ImageVerification = &openbaov1alpha1.ImageVerificationConfig{
					Enabled:       true,
					FailurePolicy: "Warn",
				}
			},
		},
		{
			Name:          "operator-image-verification-warn",
			AdmissionRule: hardenedcontract.RuleOperatorImageVerificationPolicy,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.OperatorImageVerification = &openbaov1alpha1.ImageVerificationConfig{
					Enabled:       true,
					FailurePolicy: "Warn",
				}
			},
		},
		{
			Name:          "run-as-non-root-disabled",
			AdmissionRule: hardenedcontract.RuleRunAsNonRoot,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.SecurityContext = &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(false)}
			},
		},
		{
			Name:          "unconfined-seccomp",
			AdmissionRule: hardenedcontract.RuleSeccompProfile,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.SecurityContext = &corev1.PodSecurityContext{
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
				}
			},
		},
		{
			Name:          "root-user",
			AdmissionRule: hardenedcontract.RuleRootIdentity,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.SecurityContext = &corev1.PodSecurityContext{RunAsUser: ptr.To(int64(0))}
			},
		},
		{
			Name:          "root-supplemental-group",
			AdmissionRule: hardenedcontract.RuleRootSupplementalGroups,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.SecurityContext = &corev1.PodSecurityContext{SupplementalGroups: []int64{0}}
			},
		},
		{
			Name:          "pod-sysctl",
			AdmissionRule: hardenedcontract.RulePodSysctls,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.SecurityContext = &corev1.PodSecurityContext{
					Sysctls: []corev1.Sysctl{{Name: "kernel.shm_rmid_forced", Value: "1"}},
				}
			},
		},
		{
			Name:          "windows-security-options",
			AdmissionRule: hardenedcontract.RuleWindowsSecurityOptions,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.SecurityContext = &corev1.PodSecurityContext{
					WindowsOptions: &corev1.WindowsSecurityContextOptions{},
				}
			},
		},
		{
			Name:          "listener-tls-disabled",
			AdmissionRule: hardenedcontract.RuleListenerTLS,
			RuntimeRule:   hardenedcontract.RuleListenerTLS,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{
					Listener: &openbaov1alpha1.ListenerConfig{TLSDisable: ptr.To(true)},
				}
			},
		},
		{
			Name:          "backup-tls-verification-disabled",
			AdmissionRule: hardenedcontract.RuleStorageTLSVerification,
			RuntimeRule:   hardenedcontract.RuleStorageTLSVerification,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				configureBackup(cluster)
				cluster.Spec.Backup.Target.InsecureSkipVerify = true
			},
		},
		{
			Name:          "backup-ambient-identity",
			AdmissionRule: hardenedcontract.RuleStorageExplicitIdentity,
			RuntimeRule:   hardenedcontract.RuleStorageExplicitIdentity,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				configureBackup(cluster)
				cluster.Spec.Backup.Target.RoleARN = ""
			},
		},
		{
			Name:          "service-monitor-tls-verification-disabled",
			AdmissionRule: hardenedcontract.RuleServiceMonitorTLSVerification,
			RuntimeRule:   hardenedcontract.RuleServiceMonitorTLSVerification,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Observability = &openbaov1alpha1.ObservabilityConfig{
					Metrics: &openbaov1alpha1.MetricsConfig{
						Enabled: true,
						ServiceMonitor: &openbaov1alpha1.ServiceMonitorConfig{
							Enabled: true,
							TLSConfig: &openbaov1alpha1.ServiceMonitorTLSConfig{
								InsecureSkipVerify: ptr.To(true),
							},
						},
					},
				}
			},
		},
		{
			Name:          "gateway-backend-tls-disabled",
			AdmissionRule: hardenedcontract.RuleGatewayBackendTLS,
			RuntimeRule:   hardenedcontract.RuleGatewayBackendTLS,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Gateway = &openbaov1alpha1.GatewayConfig{
					Enabled:  true,
					Hostname: "bao.example.com",
					GatewayRef: openbaov1alpha1.GatewayReference{
						Name: "shared-gateway",
					},
					BackendTLS: &openbaov1alpha1.BackendTLSConfig{Enabled: ptr.To(false)},
				}
			},
		},
		{
			Name:          "detect-deadlocks",
			AdmissionRule: hardenedcontract.RuleDangerousRuntimeFlags,
			RuntimeRule:   hardenedcontract.RuleDangerousRuntimeFlags,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{DetectDeadlocks: ptr.To(true)}
			},
		},
		{
			Name:          "raw-storage-endpoint",
			AdmissionRule: hardenedcontract.RuleDangerousRuntimeFlags,
			RuntimeRule:   hardenedcontract.RuleDangerousRuntimeFlags,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{RawStorageEndpoint: ptr.To(true)}
			},
		},
		{
			Name:          "introspection-endpoint",
			AdmissionRule: hardenedcontract.RuleDangerousRuntimeFlags,
			RuntimeRule:   hardenedcontract.RuleDangerousRuntimeFlags,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{IntrospectionEndpoint: ptr.To(true)}
			},
		},
		{
			Name:          "unsafe-api-audit-creation",
			AdmissionRule: hardenedcontract.RuleDangerousRuntimeFlags,
			RuntimeRule:   hardenedcontract.RuleDangerousRuntimeFlags,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{
					UnsafeAllowAPIAuditCreation: ptr.To(true),
				}
			},
		},
		{
			Name:          "raw-ingress-rules",
			AdmissionRule: hardenedcontract.RuleRawIngressRules,
			RuntimeRule:   hardenedcontract.RuleRawIngressRules,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Network = &openbaov1alpha1.NetworkConfig{
					IngressRules: []networkingv1.NetworkPolicyIngressRule{{}},
				}
			},
		},
		{
			Name:          "empty-trusted-ingress-peer",
			AdmissionRule: hardenedcontract.RuleTrustedIngressPeers,
			RuntimeRule:   hardenedcontract.RuleTrustedIngressPeers,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Network = &openbaov1alpha1.NetworkConfig{
					TrustedIngressPeers: []networkingv1.NetworkPolicyPeer{{}},
				}
			},
		},
		{
			Name:          "trusted-ingress-cidr-containing-loopback",
			AdmissionRule: hardenedcontract.RuleTrustedIngressPeers,
			RuntimeRule:   hardenedcontract.RuleTrustedIngressPeers,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Network = &openbaov1alpha1.NetworkConfig{
					TrustedIngressPeers: []networkingv1.NetworkPolicyPeer{
						{IPBlock: &networkingv1.IPBlock{CIDR: "126.0.0.0/7"}},
					},
				}
			},
		},
		{
			Name:          "egress-missing-ports",
			AdmissionRule: hardenedcontract.RuleEgressRules,
			RuntimeRule:   hardenedcontract.RuleEgressRules,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Network = &openbaov1alpha1.NetworkConfig{
					EgressRules: []networkingv1.NetworkPolicyEgressRule{
						{To: []networkingv1.NetworkPolicyPeer{explicitNamespacePeer("objectstore")}},
					},
				}
			},
		},
		{
			Name:          "egress-wildcard-ip-block",
			AdmissionRule: hardenedcontract.RuleEgressRules,
			RuntimeRule:   hardenedcontract.RuleEgressRules,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				port := intstr.FromInt32(443)
				cluster.Spec.Network = &openbaov1alpha1.NetworkConfig{
					EgressRules: []networkingv1.NetworkPolicyEgressRule{
						{
							To: []networkingv1.NetworkPolicyPeer{
								{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0"}},
							},
							Ports: []networkingv1.NetworkPolicyPort{{Port: &port}},
						},
					},
				}
			},
		},
		{
			Name:          "operation-egress-missing",
			AdmissionRule: hardenedcontract.RuleOperationEgressRequired,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				configureBackup(cluster)
				cluster.Spec.Network = nil
			},
		},
		{
			Name:          "backup-http-endpoint",
			AdmissionRule: hardenedcontract.RuleBackupEndpointScheme,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				configureBackup(cluster)
				cluster.Spec.Backup.Target.Endpoint = "http://objectstore.example.com"
			},
		},
		{
			Name:          "single-replica",
			AdmissionRule: hardenedcontract.RuleMinimumReplicas,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Replicas = 1
			},
		},
		{
			Name:              "custom-image-trust-roots-without-authorization",
			AdmissionRule:     hardenedcontract.RuleCustomImageTrustRootsAuthorization,
			AuthorizationOnly: true,
			Configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.ImageVerification = &openbaov1alpha1.ImageVerificationConfig{
					Enabled:       true,
					FailurePolicy: "Block",
					IssuerRegExp:  "^https://issuer.example.com$",
					SubjectRegExp: "^https://github.com/example/repo/.+$",
					IgnoreTlog:    true,
				}
			},
		},
	}
}

func configureBackup(cluster *openbaov1alpha1.OpenBaoCluster) {
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule:    "0 0 * * *",
		JWTAuthRole: "backup-role",
		Target: openbaov1alpha1.BackupTarget{
			Provider: "s3",
			Endpoint: "https://objectstore.example.com",
			Bucket:   "openbao-backups",
			RoleARN:  "arn:aws:iam::123456789012:role/openbao-backup",
		},
	}
	cluster.Spec.Network = &openbaov1alpha1.NetworkConfig{
		EgressRules: []networkingv1.NetworkPolicyEgressRule{safeEgressRule()},
	}
}

func safeEgressRule() networkingv1.NetworkPolicyEgressRule {
	port := intstr.FromInt32(443)
	return networkingv1.NetworkPolicyEgressRule{
		To:    []networkingv1.NetworkPolicyPeer{explicitNamespacePeer("objectstore")},
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: ptr.To(corev1.ProtocolTCP), Port: &port}},
	}
}

func explicitNamespacePeer(namespace string) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace},
		},
	}
}
