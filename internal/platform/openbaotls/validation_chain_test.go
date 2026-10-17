package openbaotls

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestValidateExternalServerSecretIntermediateChain(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"}}
	cluster.Spec.TLS.Enabled = true
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeExternal
	serverName := portopenbao.ComputeTLSServerName(cluster)
	trusted := newExternalTLSChain(t, serverName, false, nil)
	untrusted := newExternalTLSChain(t, serverName, false, nil)
	for _, tt := range []struct {
		name        string
		configure   func(*corev1.Secret, *corev1.Secret)
		wantErrPart string
	}{
		{name: "leaf and intermediate with root-only trust"},
		{
			name: "root included in server chain",
			configure: func(_ *corev1.Secret, server *corev1.Secret) {
				server.Data["tls.crt"] = append(server.Data["tls.crt"], trusted.root...)
			},
		},
		{
			name: "missing intermediate",
			configure: func(_ *corev1.Secret, server *corev1.Secret) {
				server.Data["tls.crt"] = trusted.leaf
			},
			wantErrPart: "unknown authority",
		},
		{
			name: "wrong intermediate",
			configure: func(_ *corev1.Secret, server *corev1.Secret) {
				server.Data["tls.crt"] = append(append([]byte{}, trusted.leaf...), untrusted.intermediate...)
			},
			wantErrPart: "unknown authority",
		},
		{
			name: "untrusted complete chain cannot add a trust root",
			configure: func(_ *corev1.Secret, server *corev1.Secret) {
				server.Data["tls.crt"] = append(append(append([]byte{}, untrusted.leaf...), untrusted.intermediate...), untrusted.root...)
				server.Data["tls.key"] = untrusted.key
				server.Data["ca.crt"] = untrusted.root
			},
			wantErrPart: "unknown authority",
		},
		{
			name: "malformed intermediate",
			configure: func(_ *corev1.Secret, server *corev1.Secret) {
				server.Data["tls.crt"] = append(append([]byte{}, trusted.leaf...), pem.EncodeToMemory(&pem.Block{
					Type: "CERTIFICATE", Bytes: []byte("malformed-certificate"),
				})...)
			},
			wantErrPart: "tls.crt key policy",
		},
		{
			name: "mismatched private key",
			configure: func(_ *corev1.Secret, server *corev1.Secret) {
				server.Data["tls.key"] = untrusted.key
			},
			wantErrPart: "do not form a valid key pair",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ca := &corev1.Secret{Data: map[string][]byte{"ca.crt": trusted.root}}
			server := &corev1.Secret{Data: map[string][]byte{
				"tls.crt": append(append([]byte{}, trusted.leaf...), trusted.intermediate...), "tls.key": trusted.key,
			}}
			if tt.configure != nil {
				tt.configure(ca, server)
			}
			err := ValidateExternalServerSecret(cluster, ca, server)
			if tt.wantErrPart != "" {
				require.ErrorContains(t, err, tt.wantErrPart)
				return
			}
			require.NoError(t, err)
		})
	}

	for _, tt := range []struct {
		name             string
		weakIntermediate bool
		configure        func(*x509.Certificate, *x509.Certificate)
		wantErrPart      string
	}{
		{
			name: "expired intermediate", configure: func(intermediate, _ *x509.Certificate) {
				intermediate.NotAfter = time.Now().Add(-time.Minute)
			}, wantErrPart: "expired",
		},
		{
			name: "expired leaf", configure: func(_, leaf *x509.Certificate) {
				leaf.NotAfter = time.Now().Add(-time.Minute)
			}, wantErrPart: "expired",
		},
		{
			name: "wrong server name", configure: func(_, leaf *x509.Certificate) {
				leaf.DNSNames = []string{"other.example"}
			}, wantErrPart: "not trusted for",
		},
		{
			name: "client-only leaf", configure: func(_, leaf *x509.Certificate) {
				leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
			}, wantErrPart: "incompatible key usage",
		},
		{name: "weak intermediate key", weakIntermediate: true, wantErrPart: "at least 2048 bits"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			chain := newExternalTLSChain(t, serverName, tt.weakIntermediate, tt.configure)
			ca := &corev1.Secret{Data: map[string][]byte{"ca.crt": chain.root}}
			server := &corev1.Secret{Data: map[string][]byte{
				"tls.crt": append(append([]byte{}, chain.leaf...), chain.intermediate...), "tls.key": chain.key,
			}}
			require.ErrorContains(t, ValidateExternalServerSecret(cluster, ca, server), tt.wantErrPart)
		})
	}
}

type externalTLSChain struct {
	root         []byte
	intermediate []byte
	leaf         []byte
	key          []byte
}

func newExternalTLSChain(t *testing.T, serverName string, weakIntermediate bool, configure func(*x509.Certificate, *x509.Certificate)) externalTLSChain {
	t.Helper()
	newKey := func() *ecdsa.PrivateKey {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		return key
	}
	rootKey, leafKey := newKey(), newKey()
	var intermediateKey crypto.Signer = newKey()
	if weakIntermediate {
		key, err := rsa.GenerateKey(rand.Reader, 1024)
		require.NoError(t, err)
		intermediateKey = key
	}
	now := time.Now()
	root := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-root"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	intermediate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "test-intermediate"},
		NotBefore: root.NotBefore, NotAfter: root.NotAfter,
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "test-server"},
		NotBefore: root.NotBefore, NotAfter: root.NotAfter, DNSNames: []string{serverName},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if configure != nil {
		configure(intermediate, leaf)
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, rootKey.Public(), rootKey)
	require.NoError(t, err)
	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediate, root, intermediateKey.Public(), rootKey)
	require.NoError(t, err)
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, intermediate, leafKey.Public(), intermediateKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	require.NoError(t, err)
	return externalTLSChain{
		root:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}),
		intermediate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: intermediateDER}),
		leaf:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		key:          pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}
}
