package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestJWKMinimumKeySize(t *testing.T) {
	for _, bits := range []int{1024, 2048} {
		t.Run(strconv.Itoa(bits), func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, bits)
			require.NoError(t, err)
			raw := jwkKey{Kty: "RSA", N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}
			template := &x509.Certificate{SerialNumber: big.NewInt(1),
				NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
			der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
			require.NoError(t, err)
			for _, input := range []jwkKey{raw, {Kty: "RSA", X5c: []string{base64.StdEncoding.EncodeToString(der)}}} {
				_, err := pemPublicKeyFromJWK(input)
				if bits == 1024 {
					require.ErrorContains(t, err, "at least 2048 bits")
				} else {
					require.NoError(t, err)
				}
			}
		})
	}
}
