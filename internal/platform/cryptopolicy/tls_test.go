package cryptopolicy

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTLSHandshakePolicy(t *testing.T) {
	strong, roots := policyCertificate(t, 2048)
	weak, weakRoots := policyCertificate(t, 1024)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecCertificate, ecRoots := policySignedCertificate(t, ecKey, ecKey)
	weakIssuer, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	strongLeaf, weakAnchor := policySignedCertificate(t, ecKey, weakIssuer)
	for _, tt := range []struct {
		name        string
		version     uint16
		cipher      uint16
		certificate tls.Certificate
		roots       *x509.CertPool
		bad         bool
	}{
		{name: "TLS 1.2 AES-GCM", version: tls.VersionTLS12,
			cipher: tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, certificate: strong, roots: roots},
		{name: "TLS 1.2 ChaCha20", version: tls.VersionTLS12,
			cipher: tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256, certificate: strong, roots: roots},
		{name: "TLS 1.3", version: tls.VersionTLS13, certificate: strong, roots: roots},
		{name: "TLS 1.2 ECDSA AES-GCM", version: tls.VersionTLS12,
			cipher: tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, certificate: ecCertificate, roots: ecRoots},
		{name: "TLS 1.2 ECDSA ChaCha20", version: tls.VersionTLS12,
			cipher: tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256, certificate: ecCertificate, roots: ecRoots},
		{name: "CBC-only", version: tls.VersionTLS12,
			cipher: tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA, certificate: strong, roots: roots, bad: true},
		{name: "static RSA", version: tls.VersionTLS12,
			cipher: tls.TLS_RSA_WITH_AES_128_GCM_SHA256, certificate: strong, roots: roots, bad: true},
		{name: "weak trusted TLS 1.3 leaf", version: tls.VersionTLS13,
			certificate: weak, roots: weakRoots, bad: true},
		{name: "strong leaf under weak trust anchor", version: tls.VersionTLS13,
			certificate: strongLeaf, roots: weakAnchor, bad: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &tls.Config{RootCAs: tt.roots, ServerName: "policy.test"}
			ConfigureTLS(client)
			server := &tls.Config{Certificates: []tls.Certificate{tt.certificate},
				MinVersion: tt.version, MaxVersion: tt.version, SessionTicketsDisabled: true}
			if tt.cipher != 0 {
				server.CipherSuites = []uint16{tt.cipher}
			}
			state, err := policyHandshake(t, client, server)
			if tt.bad {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.version, state.Version)
			require.NotEmpty(t, state.VerifiedChains)
		})
	}
}

func TestServerCertificateRotationPolicy(t *testing.T) {
	strong, roots := policyCertificate(t, 2048)
	weak, weakRoots := policyCertificate(t, 1024)
	server := &tls.Config{SessionTicketsDisabled: true}
	ConfigureServerTLS(server)
	// Match controller-runtime: install the watcher after applying TLS options.
	selected := &strong
	server.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return selected, nil }
	client := &tls.Config{RootCAs: roots, ServerName: "policy.test"}
	_, err := policyHandshake(t, client, server)
	require.NoError(t, err)
	selected = &weak
	client.RootCAs = weakRoots
	_, err = policyHandshake(t, client, server)
	require.ErrorContains(t, err, "RSA public key has 1024 bits")
}

func TestConfigureTLSKeepsVerificationAndVersion(t *testing.T) {
	want := errors.New("existing verification policy")
	config := &tls.Config{MinVersion: tls.VersionTLS13,
		VerifyConnection: func(tls.ConnectionState) error { return want }}
	ConfigureTLS(config)
	require.Equal(t, uint16(tls.VersionTLS13), config.MinVersion)
	require.ErrorIs(t, config.VerifyConnection(tls.ConnectionState{}), want)
}

func policyCertificate(t *testing.T, bits int) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	issuer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	key, err := rsa.GenerateKey(rand.Reader, bits)
	require.NoError(t, err)
	return policySignedCertificate(t, key, issuer)
}

func policySignedCertificate(t *testing.T, key, issuer crypto.Signer) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, issuer.Public(), issuer)
	require.NoError(t, err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
		DNSNames: []string{"policy.test"}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, key.Public(), issuer)
	require.NoError(t, err)
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}))
	require.NoError(t, err)
	root, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	return certificate, roots
}

func policyHandshake(t *testing.T, clientConfig, serverConfig *tls.Config) (tls.ConnectionState, error) {
	t.Helper()
	a, b := net.Pipe()
	defer func() {
		_ = a.Close()
		_ = b.Close()
	}()
	deadline := time.Now().Add(5 * time.Second)
	require.NoError(t, a.SetDeadline(deadline))
	require.NoError(t, b.SetDeadline(deadline))
	client, server := tls.Client(a, clientConfig), tls.Server(b, serverConfig)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.HandshakeContext(ctx) }()
	clientErr := client.HandshakeContext(ctx)
	if clientErr != nil {
		_ = a.Close()
	}
	return client.ConnectionState(), errors.Join(clientErr, <-done)
}
