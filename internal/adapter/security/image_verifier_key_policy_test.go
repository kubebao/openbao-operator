package security

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/stretchr/testify/require"

	"github.com/kubebao/openbao-operator/internal/port/imageverify"
)

func TestImageVerificationMinimumKeySize(t *testing.T) {
	trusted, err := parseTrustedRootWithPolicy(embeddedTrustedRootJSON)
	require.NoError(t, err)
	for _, bits := range []int{1024, 2048} {
		t.Run(strconv.Itoa(bits), func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, bits)
			require.NoError(t, err)
			der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
			require.NoError(t, err)
			config := imageverify.VerifyConfig{PublicKey: string(pem.EncodeToMemory(&pem.Block{
				Type: "PUBLIC KEY", Bytes: der,
			}))}
			opts, err := (&ImageVerifier{}).buildCheckOpts(config, nil, false, trusted)
			if bits == 1024 {
				require.ErrorContains(t, err, "at least 2048 bits")
				return
			}
			require.NoError(t, err)
			require.False(t, opts.IgnoreTlog)
			message := []byte("synthetic image key policy test")
			digest := sha256.Sum256(message)
			signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
			require.NoError(t, err)
			require.NoError(t, opts.SigVerifier.VerifySignature(bytes.NewReader(signature), bytes.NewReader(message)))
		})
	}
}

func TestTrustedRootChecksActualKeySize(t *testing.T) {
	for _, bits := range []int{1024, 2048} {
		t.Run(strconv.Itoa(bits), func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, bits)
			require.NoError(t, err)
			der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
			require.NoError(t, err)
			var document map[string]any
			require.NoError(t, json.Unmarshal(embeddedTrustedRootJSON, &document))
			log := document["tlogs"].([]any)[0].(map[string]any)
			publicKey := log["publicKey"].(map[string]any)
			publicKey["keyDetails"] = "PKIX_RSA_PKCS1V15_2048_SHA256"
			publicKey["rawBytes"] = base64.StdEncoding.EncodeToString(der)
			logID := sha256.Sum256(der)
			log["logId"].(map[string]any)["keyId"] = base64.StdEncoding.EncodeToString(logID[:])
			data, err := json.Marshal(document)
			require.NoError(t, err)
			_, err = parseTrustedRootWithPolicy(data)
			if bits == 1024 {
				require.ErrorContains(t, err, "at least 2048 bits")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestImageSigningCertificatePolicy(t *testing.T) {
	now := time.Now()
	issuer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour),
		NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &issuer.PublicKey, issuer)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	authority := &root.FulcioCertificateAuthority{Root: ca}
	secured := certificateAuthorityWithPolicy{CertificateAuthority: authority}
	for _, tt := range []struct {
		name    string
		bits    int
		expired bool
	}{
		{name: "strong signing certificate", bits: 2048},
		{name: "weak signing certificate", bits: 1024},
		{name: "expired strong certificate", bits: 2048, expired: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, tt.bits)
			require.NoError(t, err)
			leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
				KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}}
			if tt.expired {
				leaf.NotAfter = now.Add(-time.Minute)
			}
			der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, issuer)
			require.NoError(t, err)
			certificate, err := x509.ParseCertificate(der)
			require.NoError(t, err)
			_, originalErr := authority.Verify(certificate, now)
			chains, err := secured.Verify(certificate, now)
			switch {
			case tt.bits == 1024:
				require.NoError(t, originalErr, "the weak key must be the only reason this certificate fails")
				require.ErrorContains(t, err, "at least 2048 bits")
			case tt.expired:
				require.Error(t, originalErr)
				require.ErrorContains(t, err, "expired")
			default:
				require.NoError(t, err)
				require.Len(t, chains, 1)
				require.Len(t, chains[0], 2)
			}
		})
	}
}
