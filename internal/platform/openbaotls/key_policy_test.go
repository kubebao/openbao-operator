package openbaotls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestExternalServerMinimumKeySize(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "policy"}}
	cluster.Spec.TLS.Enabled = true
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeExternal
	issuer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1),
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &issuer.PublicKey, issuer)
	require.NoError(t, err)
	caSecret := &corev1.Secret{Data: map[string][]byte{
		"ca.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
	}}
	for _, bits := range []int{1024, 2048} {
		t.Run(strconv.Itoa(bits), func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, bits)
			require.NoError(t, err)
			leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
				DNSNames: []string{portopenbao.ComputeTLSServerName(cluster)}, KeyUsage: x509.KeyUsageDigitalSignature,
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, issuer)
			require.NoError(t, err)
			serverSecret := &corev1.Secret{Data: map[string][]byte{
				"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
				"tls.key": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
			}}
			err = ValidateExternalServerSecret(cluster, caSecret, serverSecret)
			if bits == 1024 {
				require.ErrorContains(t, err, "at least 2048 bits")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
