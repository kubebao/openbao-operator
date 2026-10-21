package workloadidentity

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestEvaluateCloudUnsealIdentity(t *testing.T) {
	scheme := testReadinessScheme(t)

	t.Run("aws workload identity annotations are surfaced", func(t *testing.T) {
		testCloudUnsealIdentityAWSWorkloadIdentityAnnotations(t, scheme)
	})
	t.Run("aws without explicit metadata remains ambient", func(t *testing.T) {
		testCloudUnsealIdentityAWSAmbient(t, scheme)
	})
	t.Run("missing credentials secret is surfaced", func(t *testing.T) {
		testCloudUnsealIdentityMissingSecret(t, scheme)
	})
	t.Run("gcp explicit credentials are not treated as ambient", func(t *testing.T) {
		testCloudUnsealIdentityGCPExplicit(t, scheme)
	})
	t.Run("azure ambient identity mentions both metadata surfaces", func(t *testing.T) {
		testCloudUnsealIdentityAzureAmbient(t, scheme)
	})
	t.Run("azure operator-managed workload identity is explicit", func(t *testing.T) {
		testCloudUnsealIdentityAzureOperatorWorkloadIdentity(t, scheme)
	})
	t.Run("azure operator-managed workload identity checks its settings secret", func(t *testing.T) {
		testCloudUnsealIdentityAzureOperatorWorkloadIdentityMissingSecret(t, scheme)
	})
	t.Run("oci api key without secret is explicit", func(t *testing.T) {
		testCloudUnsealIdentityOCIExplicitAPIKey(t, scheme)
	})
	t.Run("oci api key secret requires api key mode", func(t *testing.T) {
		testCloudUnsealIdentityOCISecretRequiresAPIKeyMode(t, scheme)
	})
	t.Run("oci ambient path is surfaced", func(t *testing.T) {
		testCloudUnsealIdentityOCIAmbient(t, scheme)
	})
}

func testCloudUnsealIdentityAWSWorkloadIdentityAnnotations(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "awskms",
				AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
					Region:   "eu-central-1",
					KMSKeyID: "alias/openbao",
				},
			},
			ServiceAccount: &openbaov1alpha1.ServiceAccountConfig{
				Annotations: map[string]string{
					"eks.amazonaws.com/role-arn": "arn:aws:iam::123456789012:role/openbao",
				},
			},
		},
	}

	readiness := evaluateCloudUnsealIdentityForTest(t, scheme, cluster)
	if readiness.Status != metav1.ConditionTrue || readiness.Reason != constants.ReasonWorkloadIdentityConfigured {
		t.Fatalf("readiness = %#v, want true/%s", readiness, constants.ReasonWorkloadIdentityConfigured)
	}
	if readiness.Mode != CloudUnsealIdentityModeAmbient {
		t.Fatalf("mode = %q, want %q", readiness.Mode, CloudUnsealIdentityModeAmbient)
	}
	if !strings.Contains(readiness.Message, "standard AWS credential chain") {
		t.Fatalf("message = %q, want AWS credential-chain guidance", readiness.Message)
	}
}

func testCloudUnsealIdentityAWSAmbient(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "awskms",
				AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
					Region:   "eu-central-1",
					KMSKeyID: "alias/openbao",
				},
			},
		},
	}

	readiness := evaluateCloudUnsealIdentityForTest(t, scheme, cluster)
	if readiness.Status != metav1.ConditionTrue || readiness.Reason != constants.ReasonAmbientIdentityAssumed {
		t.Fatalf("readiness = %#v, want true/%s", readiness, constants.ReasonAmbientIdentityAssumed)
	}
}

func testCloudUnsealIdentityMissingSecret(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "awskms",
				CredentialsSecretRef: &corev1.LocalObjectReference{
					Name: "aws-creds",
				},
				AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
					Region:   "eu-central-1",
					KMSKeyID: "alias/openbao",
				},
			},
		},
	}

	readiness := evaluateCloudUnsealIdentityForTest(t, scheme, cluster)
	if readiness.Status != metav1.ConditionFalse || readiness.Reason != constants.ReasonCredentialsSecretMissing {
		t.Fatalf("readiness = %#v, want false/%s", readiness, constants.ReasonCredentialsSecretMissing)
	}
	if !strings.Contains(readiness.Message, "aws-creds") {
		t.Fatalf("message = %q, want missing Secret name", readiness.Message)
	}
}

func testCloudUnsealIdentityGCPExplicit(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "gcpckms",
				GCPCloudKMS: &openbaov1alpha1.GCPCloudKMSSealConfig{
					Project:     "proj",
					Region:      "europe-west1",
					KeyRing:     "ring",
					CryptoKey:   "key",
					Credentials: "/etc/gcp/creds.json",
				},
			},
		},
	}

	readiness := evaluateCloudUnsealIdentityForTest(t, scheme, cluster)
	if readiness.Status != metav1.ConditionTrue || readiness.Reason != "Ready" {
		t.Fatalf("readiness = %#v, want true/Ready", readiness)
	}
	if readiness.Mode != CloudUnsealIdentityModeExplicit {
		t.Fatalf("mode = %q, want %q", readiness.Mode, CloudUnsealIdentityModeExplicit)
	}
	if !strings.Contains(readiness.Message, "operator only projects it automatically") {
		t.Fatalf("message = %q, want explicit credentials guidance", readiness.Message)
	}
}

func testCloudUnsealIdentityAzureAmbient(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "azurekeyvault",
				AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
					VaultName: "vault",
					KeyName:   "key",
				},
			},
			ServiceAccount: &openbaov1alpha1.ServiceAccountConfig{
				Annotations: map[string]string{
					"azure.workload.identity/client-id": "1234",
				},
			},
			PodMetadata: &openbaov1alpha1.PodMetadataConfig{
				Labels: map[string]string{
					"azure.workload.identity/use": "true",
				},
			},
		},
	}

	readiness := evaluateCloudUnsealIdentityForTest(t, scheme, cluster)
	if readiness.Mode != CloudUnsealIdentityModeAmbient {
		t.Fatalf("mode = %q, want %q", readiness.Mode, CloudUnsealIdentityModeAmbient)
	}
	if readiness.Reason != constants.ReasonWorkloadIdentityConfigured {
		t.Fatalf("reason = %q, want %q", readiness.Reason, constants.ReasonWorkloadIdentityConfigured)
	}
	if !strings.Contains(readiness.Message, "both spec.serviceAccount.annotations and spec.podMetadata.labels") {
		t.Fatalf("message = %q, want both metadata surfaces", readiness.Message)
	}
}

func testCloudUnsealIdentityAzureOperatorWorkloadIdentity(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "azurekeyvault",
				AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
					VaultName:        "vault",
					KeyName:          "key",
					TenantID:         "tenant",
					WorkloadIdentity: &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "1234"},
				},
			},
		},
	}

	readiness := evaluateCloudUnsealIdentityForTest(t, scheme, cluster)
	if readiness.Mode != CloudUnsealIdentityModeExplicit {
		t.Fatalf("mode = %q, want %q", readiness.Mode, CloudUnsealIdentityModeExplicit)
	}
	if !strings.Contains(readiness.Message, `client ID "1234"`) || !strings.Contains(readiness.Message, "api://AzureADTokenExchange") {
		t.Fatalf("message = %q, want the client ID and the token audience", readiness.Message)
	}
}

func testCloudUnsealIdentityAzureOperatorWorkloadIdentityMissingSecret(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type:                 "azurekeyvault",
				CredentialsSecretRef: &corev1.LocalObjectReference{Name: "azure-settings"},
				AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
					VaultName:        "vault",
					KeyName:          "key",
					TenantID:         "tenant",
					WorkloadIdentity: &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "1234"},
				},
			},
		},
	}

	readiness := evaluateCloudUnsealIdentityForTest(t, scheme, cluster)
	if readiness.Status != metav1.ConditionFalse || readiness.Reason != constants.ReasonCredentialsSecretMissing {
		t.Fatalf("status = %q, reason = %q, want False/%q", readiness.Status, readiness.Reason, constants.ReasonCredentialsSecretMissing)
	}
}

func testCloudUnsealIdentityOCIExplicitAPIKey(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "ocikms",
				OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
					KeyID:              "ocid1.key.oc1..example",
					CryptoEndpoint:     "https://kms.example",
					ManagementEndpoint: "https://kms.example",
					AuthTypeAPIKey:     boolPtr(true),
				},
			},
		},
	}

	readiness := evaluateCloudUnsealIdentityForTest(t, scheme, cluster)
	if readiness.Mode != CloudUnsealIdentityModeExplicit {
		t.Fatalf("mode = %q, want %q", readiness.Mode, CloudUnsealIdentityModeExplicit)
	}
	if readiness.Reason != "Ready" {
		t.Fatalf("reason = %q, want Ready", readiness.Reason)
	}
	if !strings.Contains(readiness.Message, "OCI_CONFIG_FILE") {
		t.Fatalf("message = %q, want OCI config guidance", readiness.Message)
	}
}

func testCloudUnsealIdentityOCISecretRequiresAPIKeyMode(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "ocikms",
				CredentialsSecretRef: &corev1.LocalObjectReference{
					Name: "oci-creds",
				},
				OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
					KeyID:              "ocid1.key.oc1..example",
					CryptoEndpoint:     "https://kms.example",
					ManagementEndpoint: "https://kms.example",
				},
			},
		},
	}

	readiness := evaluateCloudUnsealIdentityForTest(t, scheme, cluster)
	if readiness.Status != metav1.ConditionFalse || readiness.Reason != "PrerequisitesMissing" {
		t.Fatalf("readiness = %#v, want false/PrerequisitesMissing", readiness)
	}
	if !strings.Contains(readiness.Message, "authTypeAPIKey=true") {
		t.Fatalf("message = %q, want api-key mode guidance", readiness.Message)
	}
}

func testCloudUnsealIdentityOCIAmbient(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "ocikms",
				OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
					KeyID:              "ocid1.key.oc1..example",
					CryptoEndpoint:     "https://kms.example",
					ManagementEndpoint: "https://kms.example",
				},
			},
		},
	}

	readiness := evaluateCloudUnsealIdentityForTest(t, scheme, cluster)
	if readiness.Mode != CloudUnsealIdentityModeAmbient {
		t.Fatalf("mode = %q, want %q", readiness.Mode, CloudUnsealIdentityModeAmbient)
	}
	if readiness.Reason != constants.ReasonAmbientIdentityAssumed {
		t.Fatalf("reason = %q, want %q", readiness.Reason, constants.ReasonAmbientIdentityAssumed)
	}
	if !strings.Contains(readiness.Message, "default OCI principal flow") {
		t.Fatalf("message = %q, want OCI ambient guidance", readiness.Message)
	}
}

func evaluateCloudUnsealIdentityForTest(
	t *testing.T,
	scheme *runtime.Scheme,
	cluster *openbaov1alpha1.OpenBaoCluster,
) CloudUnsealIdentityReadiness {
	t.Helper()

	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	readiness, applicable, err := EvaluateCloudUnsealIdentity(context.Background(), reader, cluster)
	if err != nil {
		t.Fatalf("EvaluateCloudUnsealIdentity() error = %v", err)
	}
	if !applicable {
		t.Fatal("expected cloud unseal identity evaluation to apply")
	}

	return readiness
}

func boolPtr(v bool) *bool {
	return &v
}
