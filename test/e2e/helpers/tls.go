package helpers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	caCertKey = "ca.crt"
	caKeyKey  = "ca.key"

	tlsCertKey = "tls.crt"
	tlsKeyKey  = "tls.key"
)

// EnsureExternalTLSSecrets creates the Secrets required for TLS External mode:
// - <cluster>-tls-ca (Opaque, keys: ca.crt, ca.key)
// - <cluster>-tls-server (kubernetes.io/tls, keys: tls.crt, tls.key, ca.crt)
//
// The SANs match the operator-generated certificate shape closely enough to
// satisfy probes (localhost) and in-cluster DNS names.
// Optional serverNames replace the default DNS SANs for TLS contract tests.
func EnsureExternalTLSSecrets(
	ctx context.Context,
	c client.Client,
	namespace string,
	clusterName string,
	replicas int32,
	serverNames ...string,
) error {
	if c == nil {
		return fmt.Errorf("kubernetes client is required")
	}
	if namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if clusterName == "" {
		return fmt.Errorf("cluster name is required")
	}
	if replicas < 1 {
		return fmt.Errorf("replicas must be >= 1")
	}

	caCertPEM, caKeyPEM, err := generateCA(clusterName)
	if err != nil {
		return err
	}

	serverCertPEM, serverKeyPEM, err := generateServerCert(
		namespace,
		clusterName,
		replicas,
		caCertPEM,
		caKeyPEM,
		serverNames...,
	)
	if err != nil {
		return err
	}

	caSecretName := clusterName + constants.SuffixTLSCA
	serverSecretName := clusterName + constants.SuffixTLSServer

	caSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caSecretName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			caCertKey: caCertPEM,
			caKeyKey:  caKeyPEM,
		},
	}

	err = c.Create(ctx, caSecret)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create external TLS CA Secret %s/%s: %w", namespace, caSecretName, err)
	}

	serverSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serverSecretName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			tlsCertKey: serverCertPEM,
			tlsKeyKey:  serverKeyPEM,
			caCertKey:  caCertPEM,
		},
	}

	err = c.Create(ctx, serverSecret)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create external TLS server Secret %s/%s: %w", namespace, serverSecretName, err)
	}

	// Ensure both Secrets are readable back (helps surface eventual-consistency issues early).
	if err := c.Get(ctx, types.NamespacedName{Name: caSecretName, Namespace: namespace}, &corev1.Secret{}); err != nil {
		return fmt.Errorf("failed to read back external TLS CA Secret %s/%s: %w", namespace, caSecretName, err)
	}
	if err := c.Get(
		ctx,
		types.NamespacedName{Name: serverSecretName, Namespace: namespace},
		&corev1.Secret{},
	); err != nil {
		return fmt.Errorf("failed to read back external TLS server Secret %s/%s: %w", namespace, serverSecretName, err)
	}

	return nil
}

func generateCA(clusterName string) ([]byte, []byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate CA private key: %w", err)
	}

	serialNumber, err := randSerialNumber()
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("%s OpenBao Root CA (e2e)", clusterName),
			Organization: []string{"OpenBao Operator E2E"},
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal ECDSA private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

func generateServerCert(
	namespace string,
	clusterName string,
	replicas int32,
	caCertPEM []byte,
	caKeyPEM []byte,
	serverNames ...string,
) ([]byte, []byte, error) {
	caCert, caKey, err := parseCA(caCertPEM, caKeyPEM)
	if err != nil {
		return nil, nil, err
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate server private key: %w", err)
	}

	serialNumber, err := randSerialNumber()
	if err != nil {
		return nil, nil, err
	}

	dnsNames := []string{
		"localhost",
		fmt.Sprintf("%s.%s.svc", clusterName, namespace),
		fmt.Sprintf("*.%s.%s.svc", clusterName, namespace),
		fmt.Sprintf("*.%s.svc", namespace),
		// Add the common DNS SAN used for Raft auto-join with leader_tls_servername
		// This matches what the operator includes in operator-managed certificates
		fmt.Sprintf("openbao-cluster-%s.local", clusterName),
	}

	for i := int32(0); i < replicas; i++ {
		dnsNames = append(dnsNames, fmt.Sprintf("%s-%d.%s.%s.svc", clusterName, i, clusterName, namespace))
	}
	if len(serverNames) > 0 {
		dnsNames = serverNames
	}

	ipAddresses := []net.IP{net.ParseIP("127.0.0.1")}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("%s.openbao.svc", clusterName),
			Organization: []string{"OpenBao Operator E2E"},
		},
		NotBefore:   now.Add(-1 * time.Hour),
		NotAfter:    now.AddDate(0, 0, 365),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:    dnsNames,
		IPAddresses: ipAddresses,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &privateKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create server certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal ECDSA private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

func parseCA(certPEM []byte, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("failed to decode CA certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		return nil, nil, fmt.Errorf("failed to decode CA private key PEM")
	}
	privateKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CA private key: %w", err)
	}

	return cert, privateKey, nil
}

func randSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to generate serial number: %w", err)
	}
	return serialNumber, nil
}
