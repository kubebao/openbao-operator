package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestValidateTransitUnsealPrerequisites(t *testing.T) {
	t.Run("inline token without Secret-backed files passes", func(t *testing.T) {
		cluster := newMinimalCluster("transit-inline-token", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "transit",
			Transit: &openbaov1alpha1.TransitSealConfig{
				Address:   "https://infra-bao.example",
				KeyName:   "autounseal",
				MountPath: "transit/",
				Token:     "inline-token",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})

	t.Run("transit address rejects insecure and ambiguous destinations", func(t *testing.T) {
		tests := []struct {
			name    string
			address string
			want    string
		}{
			{
				name:    "plain http",
				address: "http://infra-bao.example",
				want:    "must use HTTPS",
			},
			{
				name:    "userinfo",
				address: "https://token@infra-bao.example",
				want:    "must not include userinfo",
			},
			{
				name:    "localhost",
				address: "https://localhost:8200",
				want:    "must not point to localhost",
			},
			{
				name:    "loopback ip",
				address: "https://127.0.0.1:8200",
				want:    "must not point to loopback",
			},
			{
				name:    "link local ip",
				address: "https://169.254.169.254/latest/meta-data",
				want:    "must not point to loopback",
			},
			{
				name:    "query",
				address: "https://infra-bao.example?token=leak",
				want:    "must not include query or fragment",
			},
			{
				name:    "numeric host",
				address: "https://2130706433:8200",
				want:    "must not use numeric host forms",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cluster := newMinimalCluster("transit-address-"+tt.name, "default")
				cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
					Type: "transit",
					Transit: &openbaov1alpha1.TransitSealConfig{
						Address:   tt.address,
						KeyName:   "autounseal",
						MountPath: "transit/",
						Token:     "inline-token",
					},
				}

				mgr := NewManager(newTestClient(t), testScheme, "operator-system")
				err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
				if err == nil {
					t.Fatal("validateUnsealPrerequisites() error = nil, want error")
				}
				if !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
					t.Fatalf("expected permanent prerequisites missing error, got %v", err)
				}
				if !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
				}
			})
		}
	})

	t.Run("transit credentials Secret cannot reference operator managed system Secrets", func(t *testing.T) {
		cluster := newMinimalCluster("transit-system-secret", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "transit",
			CredentialsSecretRef: &corev1.LocalObjectReference{
				Name: "transit-system-secret-root-token",
			},
			Transit: &openbaov1alpha1.TransitSealConfig{
				Address:   "https://infra-bao.example",
				KeyName:   "autounseal",
				MountPath: "transit/",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil {
			t.Fatal("validateUnsealPrerequisites() error = nil, want error")
		}
		if !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
		if !strings.Contains(err.Error(), "operator-managed system Secret") {
			t.Fatalf("error = %q, want operator-managed system Secret", err.Error())
		}
	})

	t.Run("secret-backed transit files require credentials Secret", func(t *testing.T) {
		cluster := newMinimalCluster("transit-missing-secret-ref", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "transit",
			Transit: &openbaov1alpha1.TransitSealConfig{
				Address:   "https://infra-bao.example",
				KeyName:   "autounseal",
				MountPath: "transit/",
				TLSCACert: "/etc/bao/seal-creds/ca.crt",
				Token:     "inline-token",
				Namespace: "infra",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil {
			t.Fatal("validateUnsealPrerequisites() error = nil, want error")
		}
		if !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
		if reason, ok := operatorerrors.Reason(err); !ok || reason != reasonPrerequisitesMissing {
			t.Fatalf("reason = %q,%v want %q,true", reason, ok, reasonPrerequisitesMissing)
		}
	})

	t.Run("transit Secret missing required keys is rejected early", func(t *testing.T) {
		cluster := newMinimalCluster("transit-missing-key", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "transit",
			CredentialsSecretRef: &corev1.LocalObjectReference{
				Name: "infra-bao-token",
			},
			Transit: &openbaov1alpha1.TransitSealConfig{
				Address:   "https://infra-bao.example",
				KeyName:   "autounseal",
				MountPath: "transit/",
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "infra-bao-token",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"ca.crt": []byte("dummy-ca"),
			},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil {
			t.Fatal("validateUnsealPrerequisites() error = nil, want error")
		}
		if !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
	})

	t.Run("transit Secret satisfies referenced keys", func(t *testing.T) {
		caPEM, _, err := newClientCertKeyPair()
		if err != nil {
			t.Fatalf("newClientCertKeyPair() error = %v", err)
		}
		clientCertPEM, clientKeyPEM, err := newClientCertKeyPair()
		if err != nil {
			t.Fatalf("newClientCertKeyPair() error = %v", err)
		}

		cluster := newMinimalCluster("transit-valid-secret", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "transit",
			CredentialsSecretRef: &corev1.LocalObjectReference{
				Name: "infra-bao-token",
			},
			Transit: &openbaov1alpha1.TransitSealConfig{
				Address:       "https://infra-bao.example",
				KeyName:       "autounseal",
				MountPath:     "transit/",
				TLSCACert:     "/etc/bao/seal-creds/ca.crt",
				TLSClientCert: "/etc/bao/seal-creds/client.crt",
				TLSClientKey:  "/etc/bao/seal-creds/client.key",
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "infra-bao-token",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"token":      []byte("s.123"),
				"ca.crt":     caPEM,
				"client.crt": clientCertPEM,
				"client.key": clientKeyPEM,
			},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})

	t.Run("transit client cert and key must be set together", func(t *testing.T) {
		cluster := newMinimalCluster("transit-missing-client-key", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "transit",
			Transit: &openbaov1alpha1.TransitSealConfig{
				Address:       "https://infra-bao.example",
				KeyName:       "autounseal",
				MountPath:     "transit/",
				TLSClientCert: "/etc/bao/seal-creds/client.crt",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil {
			t.Fatal("validateUnsealPrerequisites() error = nil, want error")
		}
		if !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
	})

	t.Run("transit tls file paths must use the seal creds mount", func(t *testing.T) {
		cluster := newMinimalCluster("transit-invalid-path", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "transit",
			Transit: &openbaov1alpha1.TransitSealConfig{
				Address:   "https://infra-bao.example",
				KeyName:   "autounseal",
				MountPath: "transit/",
				TLSCACert: "/etc/ssl/certs/ca.crt",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil {
			t.Fatal("validateUnsealPrerequisites() error = nil, want error")
		}
		if !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
	})

	t.Run("transit credentials Secret rejects invalid ca pem", func(t *testing.T) {
		cluster := newMinimalCluster("transit-invalid-ca", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "transit",
			CredentialsSecretRef: &corev1.LocalObjectReference{
				Name: "infra-bao-token",
			},
			Transit: &openbaov1alpha1.TransitSealConfig{
				Address:   "https://infra-bao.example",
				KeyName:   "autounseal",
				MountPath: "transit/",
				TLSCACert: "/etc/bao/seal-creds/ca.crt",
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "infra-bao-token",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"token":  []byte("s.123"),
				"ca.crt": []byte("not a cert"),
			},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil {
			t.Fatal("validateUnsealPrerequisites() error = nil, want error")
		}
		if !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
	})

	t.Run("transit credentials Secret rejects mismatched client cert and key", func(t *testing.T) {
		certPEM, _, err := newClientCertKeyPair()
		if err != nil {
			t.Fatalf("newClientCertKeyPair() error = %v", err)
		}
		_, otherKeyPEM, err := newClientCertKeyPair()
		if err != nil {
			t.Fatalf("newClientCertKeyPair() error = %v", err)
		}

		cluster := newMinimalCluster("transit-mismatched-client-key", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "transit",
			CredentialsSecretRef: &corev1.LocalObjectReference{
				Name: "infra-bao-token",
			},
			Transit: &openbaov1alpha1.TransitSealConfig{
				Address:       "https://infra-bao.example",
				KeyName:       "autounseal",
				MountPath:     "transit/",
				TLSClientCert: "/etc/bao/seal-creds/client.crt",
				TLSClientKey:  "/etc/bao/seal-creds/client.key",
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "infra-bao-token",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"token":      []byte("s.123"),
				"client.crt": certPEM,
				"client.key": otherKeyPEM,
			},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		err = mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil {
			t.Fatal("validateUnsealPrerequisites() error = nil, want error")
		}
		if !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
	})
}

func TestValidateGCPCKMSUnsealPrerequisites(t *testing.T) {
	t.Run("adc without credentials path passes", func(t *testing.T) {
		cluster := newMinimalCluster("gcpckms-adc", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "gcpckms",
			GCPCloudKMS: &openbaov1alpha1.GCPCloudKMSSealConfig{
				Project:   "demo",
				Region:    "europe-west1",
				KeyRing:   "bao",
				CryptoKey: "autounseal",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})

	t.Run("mounted credentials path requires secret ref and valid json", func(t *testing.T) {
		cluster := newMinimalCluster("gcpckms-mounted-creds", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "gcpckms",
			GCPCloudKMS: &openbaov1alpha1.GCPCloudKMSSealConfig{
				Project:     "demo",
				Region:      "europe-west1",
				KeyRing:     "bao",
				CryptoKey:   "autounseal",
				Credentials: "/etc/bao/seal-creds/credentials.json",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}

		cluster.Spec.Unseal.CredentialsSecretRef = &corev1.LocalObjectReference{Name: "gcp-creds"}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "gcp-creds", Namespace: "default"},
			Data:       map[string][]byte{"credentials.json": []byte("not json")},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr = NewManager(client, testScheme, "operator-system")
		err = mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}

		secret.Data["credentials.json"] = []byte(`{"type":"service_account"}`)
		client = fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr = NewManager(client, testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})
}

func TestValidateAWSKMSUnsealPrerequisites(t *testing.T) {
	t.Run("ambient auth without credentials secret passes", func(t *testing.T) {
		cluster := newMinimalCluster("awskms-ambient", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "awskms",
			AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
				Region:   "eu-central-1",
				KMSKeyID: "alias/openbao",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})

	t.Run("credentials Secret cannot reference operator managed system Secrets", func(t *testing.T) {
		cluster := newMinimalCluster("awskms-system-secret", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type:                 "awskms",
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "awskms-system-secret-root-token"},
			AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
				Region:   "eu-central-1",
				KMSKeyID: "alias/openbao",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil {
			t.Fatal("validateUnsealPrerequisites() error = nil, want error")
		}
		if !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
		if !strings.Contains(err.Error(), "operator-managed system Secret") {
			t.Fatalf("error = %q, want operator-managed system Secret", err.Error())
		}
	})

	t.Run("secret-backed aws kms requires access key and secret key", func(t *testing.T) {
		cluster := newMinimalCluster("awskms-secret", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type:                 "awskms",
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "aws-creds"},
			AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
				Region:   "eu-central-1",
				KMSKeyID: "alias/openbao",
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "aws-creds", Namespace: "default"},
			Data: map[string][]byte{
				"AWS_ACCESS_KEY_ID": []byte("AKIA..."),
			},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}

		secret.Data["AWS_SECRET_ACCESS_KEY"] = []byte("secret")
		client = fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr = NewManager(client, testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})
}

func TestValidateAzureKeyVaultUnsealPrerequisites(t *testing.T) {
	t.Run("managed identity without credentials secret passes", func(t *testing.T) {
		cluster := newMinimalCluster("azurekeyvault-managed-identity", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "azurekeyvault",
			AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
				VaultName: "vault",
				KeyName:   "key",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})

	t.Run("secret-backed azure key vault requires tenant, client, and secret", func(t *testing.T) {
		cluster := newMinimalCluster("azurekeyvault-secret", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type:                 "azurekeyvault",
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "azure-creds"},
			AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
				VaultName: "vault",
				KeyName:   "key",
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "azure-creds", Namespace: "default"},
			Data: map[string][]byte{
				"AZURE_CLIENT_ID": []byte("client"),
			},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}

		secret.Data["AZURE_TENANT_ID"] = []byte("tenant")
		secret.Data["AZURE_CLIENT_SECRET"] = []byte("secret")
		client = fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr = NewManager(client, testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})

	t.Run("workload identity secret needs no credential keys", func(t *testing.T) {
		tests := []struct {
			name    string
			secret  *corev1.Secret
			wantErr error
		}{
			{
				name:    "missing Secret",
				wantErr: operatorerrors.ErrPermanentPrerequisitesMissing,
			},
			{
				name: "settings-only Secret",
				secret: &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "azure-settings", Namespace: "default"},
					Data: map[string][]byte{
						"AZURE_ENVIRONMENT": []byte("AzurePublicCloud"),
					},
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cluster := newMinimalCluster("azurekeyvault-workload-identity", "default")
				cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
					Type:                 "azurekeyvault",
					CredentialsSecretRef: &corev1.LocalObjectReference{Name: "azure-settings"},
					AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
						VaultName:        "vault",
						KeyName:          "key",
						TenantID:         "tenant",
						WorkloadIdentity: &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "client"},
					},
				}
				builder := fake.NewClientBuilder().WithScheme(testScheme)
				if tt.secret != nil {
					builder = builder.WithObjects(tt.secret)
				}

				mgr := NewManager(builder.Build(), testScheme, "operator-system")
				err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
				if tt.wantErr == nil {
					if err != nil {
						t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
					}
					return
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("validateUnsealPrerequisites() error = %v, want %v", err, tt.wantErr)
				}
			})
		}
	})
}

func TestValidateOCIKMSUnsealPrerequisites(t *testing.T) {
	t.Run("ambient principal without credentials secret passes", func(t *testing.T) {
		cluster := newMinimalCluster("ocikms-ambient", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "ocikms",
			OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
				KeyID:              "ocid1.key.oc1..example",
				CryptoEndpoint:     "https://kms.us-ashburn-1.oraclecloud.com",
				ManagementEndpoint: "https://kms.us-ashburn-1.oraclecloud.com",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})

	t.Run("credentials secret requires api-key mode", func(t *testing.T) {
		cluster := newMinimalCluster("ocikms-secret-without-api-key", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type:                 "ocikms",
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "oci-creds"},
			OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
				KeyID:              "ocid1.key.oc1..example",
				CryptoEndpoint:     "https://kms.us-ashburn-1.oraclecloud.com",
				ManagementEndpoint: "https://kms.us-ashburn-1.oraclecloud.com",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
	})

	t.Run("api-key mode requires config key", func(t *testing.T) {
		cluster := newMinimalCluster("ocikms-api-key-missing-config", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type:                 "ocikms",
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "oci-creds"},
			OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
				KeyID:              "ocid1.key.oc1..example",
				CryptoEndpoint:     "https://kms.us-ashburn-1.oraclecloud.com",
				ManagementEndpoint: "https://kms.us-ashburn-1.oraclecloud.com",
				AuthTypeAPIKey:     boolPtr(true),
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "oci-creds", Namespace: "default"},
			Data: map[string][]byte{
				"private.pem": []byte("dummy"),
			},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
	})

	t.Run("api-key mode requires default profile with key_file", func(t *testing.T) {
		cluster := newMinimalCluster("ocikms-api-key-invalid-config", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type:                 "ocikms",
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "oci-creds"},
			OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
				KeyID:              "ocid1.key.oc1..example",
				CryptoEndpoint:     "https://kms.us-ashburn-1.oraclecloud.com",
				ManagementEndpoint: "https://kms.us-ashburn-1.oraclecloud.com",
				AuthTypeAPIKey:     boolPtr(true),
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "oci-creds", Namespace: "default"},
			Data: map[string][]byte{
				"config": []byte("[OTHER]\nuser=ocid1.user.oc1..example\n"),
			},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}

		secret.Data["config"] = []byte(
			"[DEFAULT]\n" +
				"user=ocid1.user.oc1..example\n" +
				"fingerprint=aa:bb:cc\n" +
				"tenancy=ocid1.tenancy.oc1..example\n" +
				"region=us-ashburn-1\n",
		)
		client = fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr = NewManager(client, testScheme, "operator-system")
		err = mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
	})

	t.Run("api-key mode requires key_file under seal creds mount", func(t *testing.T) {
		cluster := newMinimalCluster("ocikms-api-key-invalid-key-file-path", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type:                 "ocikms",
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "oci-creds"},
			OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
				KeyID:              "ocid1.key.oc1..example",
				CryptoEndpoint:     "https://kms.us-ashburn-1.oraclecloud.com",
				ManagementEndpoint: "https://kms.us-ashburn-1.oraclecloud.com",
				AuthTypeAPIKey:     boolPtr(true),
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "oci-creds", Namespace: "default"},
			Data: map[string][]byte{
				"config": []byte(
					"[DEFAULT]\n" +
						"user=ocid1.user.oc1..example\n" +
						"fingerprint=aa:bb:cc\n" +
						"tenancy=ocid1.tenancy.oc1..example\n" +
						"region=us-ashburn-1\n" +
						"key_file=/tmp/private.pem\n",
				),
			},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
	})

	t.Run("api-key mode validates config and referenced key file", func(t *testing.T) {
		cluster := newMinimalCluster("ocikms-api-key-valid", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type:                 "ocikms",
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "oci-creds"},
			OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
				KeyID:              "ocid1.key.oc1..example",
				CryptoEndpoint:     "https://kms.us-ashburn-1.oraclecloud.com",
				ManagementEndpoint: "https://kms.us-ashburn-1.oraclecloud.com",
				AuthTypeAPIKey:     boolPtr(true),
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "oci-creds", Namespace: "default"},
			Data: map[string][]byte{
				"config": []byte(
					"[DEFAULT]\n" +
						"user=ocid1.user.oc1..example\n" +
						"fingerprint=aa:bb:cc\n" +
						"tenancy=ocid1.tenancy.oc1..example\n" +
						"region=us-ashburn-1\n" +
						"key_file=/etc/bao/seal-creds/private.pem\n",
				),
				"private.pem": []byte("-----BEGIN PRIVATE KEY-----\nMIIBVwIBADANBgkqhkiG9w0BAQEFAASCAT8wggE7AgEAAkEApQ==\n-----END PRIVATE KEY-----\n"),
			},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})
}

func TestValidateKMIPUnsealPrerequisites(t *testing.T) {
	t.Run("certificate and key are required", func(t *testing.T) {
		cluster := newMinimalCluster("kmip-missing-cert-key", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "kmip",
			KMIP: &openbaov1alpha1.KMIPSealConfig{
				Endpoint: "kmip.example:5696",
				KMSKeyID: "openbao-key",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
	})

	t.Run("mounted certificate and key require secret-backed files", func(t *testing.T) {
		certPEM, keyPEM, err := newClientCertKeyPair()
		if err != nil {
			t.Fatalf("newClientCertKeyPair() error = %v", err)
		}
		caPEM, _, err := newClientCertKeyPair()
		if err != nil {
			t.Fatalf("newClientCertKeyPair() error = %v", err)
		}

		cluster := newMinimalCluster("kmip-mounted-files", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "kmip",
			KMIP: &openbaov1alpha1.KMIPSealConfig{
				Endpoint:   "kmip.example:5696",
				KMSKeyID:   "openbao-key",
				ClientCert: "/etc/bao/seal-creds/client.crt",
				ClientKey:  "/etc/bao/seal-creds/client.key",
				CACert:     "/etc/bao/seal-creds/ca.crt",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err = mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}

		cluster.Spec.Unseal.CredentialsSecretRef = &corev1.LocalObjectReference{Name: "kmip-creds"}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "kmip-creds", Namespace: "default"},
			Data: map[string][]byte{
				"client.crt": certPEM,
				"client.key": keyPEM,
				"ca.crt":     caPEM,
			},
		}
		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr = NewManager(client, testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})
}

func TestValidateKMSPluginUnsealPrerequisites(t *testing.T) {
	t.Run("no credentials Secret is optional", func(t *testing.T) {
		cluster := newMinimalCluster("kms-plugin-no-secret", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: portopenbao.SealTypeKMSPlugin,
			KMS: &openbaov1alpha1.KMSPluginSealConfig{
				PluginName: "corp-kms",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})

	t.Run("missing credentials Secret is rejected when referenced", func(t *testing.T) {
		cluster := newMinimalCluster("kms-plugin-missing-secret", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: portopenbao.SealTypeKMSPlugin,
			CredentialsSecretRef: &corev1.LocalObjectReference{
				Name: "kms-plugin-runtime",
			},
			KMS: &openbaov1alpha1.KMSPluginSealConfig{
				PluginName: "corp-kms",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
		if !strings.Contains(err.Error(), "kms plugin credentials Secret default/kms-plugin-runtime not found") {
			t.Fatalf("error = %v, want missing KMS plugin credentials Secret", err)
		}
	})

	t.Run("referenced credentials Secret may exist with plugin-owned keys", func(t *testing.T) {
		cluster := newMinimalCluster("kms-plugin-secret", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: portopenbao.SealTypeKMSPlugin,
			CredentialsSecretRef: &corev1.LocalObjectReference{
				Name: "kms-plugin-runtime",
			},
			KMS: &openbaov1alpha1.KMSPluginSealConfig{
				PluginName: "corp-kms",
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "kms-plugin-runtime", Namespace: "default"},
			Data: map[string][]byte{
				"ca.crt": []byte("plugin-owned"),
			},
		}

		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})
}

func TestValidatePKCS11UnsealPrerequisites(t *testing.T) {
	t.Run("inline pin passes", func(t *testing.T) {
		cluster := newMinimalCluster("pkcs11-inline-pin", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: portopenbao.SealTypePKCS11,
			PKCS11: &openbaov1alpha1.PKCS11SealConfig{
				Lib:      "/usr/lib/softhsm/libsofthsm2.so",
				Slot:     "0",
				KeyLabel: "bao",
				PIN:      "1234",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})

	t.Run("missing pin requires secret key", func(t *testing.T) {
		cluster := newMinimalCluster("pkcs11-secret-pin", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: portopenbao.SealTypePKCS11,
			PKCS11: &openbaov1alpha1.PKCS11SealConfig{
				Lib:        "/usr/lib/softhsm/libsofthsm2.so",
				TokenLabel: "bao-token",
				KeyLabel:   "bao",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}

		cluster.Spec.Unseal.CredentialsSecretRef = &corev1.LocalObjectReference{Name: "pkcs11-creds"}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "pkcs11-creds", Namespace: "default"},
			Data:       map[string][]byte{"BAO_HSM_PIN": []byte("1234")},
		}
		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr = NewManager(client, testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})

	t.Run("slot and token label are mutually exclusive", func(t *testing.T) {
		cluster := newMinimalCluster("pkcs11-slot-and-token-label", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: portopenbao.SealTypePKCS11,
			PKCS11: &openbaov1alpha1.PKCS11SealConfig{
				Lib:        "/usr/lib/softhsm/libsofthsm2.so",
				Slot:       "0",
				TokenLabel: "bao-token",
				KeyLabel:   "bao",
				PIN:        "1234",
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
	})

	t.Run("runtime env mappings require credentials secret keys", func(t *testing.T) {
		cluster := newMinimalCluster("pkcs11-runtime-env", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: portopenbao.SealTypePKCS11,
			CredentialsSecretRef: &corev1.LocalObjectReference{
				Name: "pkcs11-creds",
			},
			PKCS11: &openbaov1alpha1.PKCS11SealConfig{
				Lib:        "/usr/lib/softhsm/libsofthsm2.so",
				TokenLabel: "bao-token",
				KeyLabel:   "bao",
				Runtime: &openbaov1alpha1.PKCS11RuntimeConfig{
					Env: []openbaov1alpha1.PKCS11RuntimeEnvVar{
						{Name: "CRYPTOSERVER", SecretKey: "cryptoserver"},
					},
					FileEnv: []openbaov1alpha1.PKCS11RuntimeFileEnvVar{
						{Name: "CS_PKCS11_R3_CFG", SecretKey: "cs_pkcs11_R3.cfg"},
					},
				},
			},
		}

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "pkcs11-creds", Namespace: "default"},
			Data: map[string][]byte{
				"BAO_HSM_PIN":  []byte("1234"),
				"cryptoserver": []byte("hsm.example.test"),
			},
		}
		client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr := NewManager(client, testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
		if !strings.Contains(err.Error(), "cs_pkcs11_R3.cfg") {
			t.Fatalf("error = %v, want missing runtime file key", err)
		}

		secret.Data["cs_pkcs11_R3.cfg"] = []byte("config")
		client = fake.NewClientBuilder().WithScheme(testScheme).WithObjects(secret).Build()
		mgr = NewManager(client, testScheme, "operator-system")
		if err := mgr.validateUnsealPrerequisites(context.Background(), cluster); err != nil {
			t.Fatalf("validateUnsealPrerequisites() error = %v, want nil", err)
		}
	})

	t.Run("runtime env mappings cannot override seal-owned env vars", func(t *testing.T) {
		cluster := newMinimalCluster("pkcs11-runtime-reserved-env", "default")
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: portopenbao.SealTypePKCS11,
			PKCS11: &openbaov1alpha1.PKCS11SealConfig{
				Lib:        "/usr/lib/softhsm/libsofthsm2.so",
				TokenLabel: "bao-token",
				KeyLabel:   "bao",
				PIN:        "1234",
				Runtime: &openbaov1alpha1.PKCS11RuntimeConfig{
					Env: []openbaov1alpha1.PKCS11RuntimeEnvVar{
						{Name: "BAO_HSM_PIN", SecretKey: "pin"},
					},
				},
			},
		}

		mgr := NewManager(newTestClient(t), testScheme, "operator-system")
		err := mgr.validateUnsealPrerequisites(context.Background(), cluster)
		if err == nil || !errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			t.Fatalf("expected permanent prerequisites missing error, got %v", err)
		}
		if !strings.Contains(err.Error(), "managed by spec.unseal.pkcs11") {
			t.Fatalf("error = %v, want seal-owned env rejection", err)
		}
	})
}

func newClientCertKeyPair() ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName: "transit-client",
		},
		NotBefore: time.Now().Add(-time.Minute),
		NotAfter:  time.Now().Add(time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return certPEM, keyPEM, nil
}

func boolPtr(v bool) *bool {
	return &v
}

func TestRemovedSealFailsBeforeCredentialLookup(t *testing.T) {
	cluster := newMinimalCluster("removed-seal", "default")
	cluster.Spec.Version = "2.7.0"
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{Type: "awskms"}
	manager := &Manager{}
	err := manager.validateUnsealPrerequisites(context.Background(), cluster)
	if err == nil || !strings.Contains(err.Error(), "install the external KMS plugin") {
		t.Fatalf("expected missing plugin prerequisite before credential lookup, got %v", err)
	}
}
