package bootstrap

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUnsealClientMinimumKeySize(t *testing.T) {
	for _, bits := range []int{1024, 2048} {
		t.Run(strconv.Itoa(bits), func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, bits)
			require.NoError(t, err)
			template := &x509.Certificate{SerialNumber: big.NewInt(1),
				NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
			der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
			require.NoError(t, err)
			certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
			privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
			for _, err := range []error{
				validateTransitCAPEM(certificate),
				validateTransitClientKeyPairPEM(certificate, privateKey),
			} {
				if bits == 1024 {
					require.ErrorContains(t, err, "at least 2048 bits")
				} else {
					require.NoError(t, err)
				}
			}
		})
	}
}
