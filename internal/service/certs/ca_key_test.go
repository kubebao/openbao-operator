package certs

import (
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestParseECDSAPrivateKey(t *testing.T) {
	certPEM, validPEM, err := generateCA(&api.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "parser-test"}}, time.Now())
	require.NoError(t, err)
	certificate, err := parseCertificate(certPEM)
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		data      []byte
		wantError string
	}{
		{name: "valid EC private key", data: validPEM},
		{name: "empty", wantError: "failed to decode ECDSA private key PEM"},
		{name: "plain text", data: []byte("invalid private key"), wantError: "failed to decode ECDSA private key PEM"},
		{name: "truncated PEM", data: []byte("-----BEGIN EC PRIVATE KEY-----\ninvalid"), wantError: "failed to decode ECDSA private key PEM"},
		{name: "wrong block type", data: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("invalid")}), wantError: `expected EC PRIVATE KEY, got "RSA PRIVATE KEY"`},
		{name: "invalid DER", data: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("invalid")}), wantError: "failed to parse ECDSA private key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				key, err := parseECDSAPrivateKey(tc.data)
				if tc.wantError != "" {
					require.ErrorContains(t, err, tc.wantError)
					require.Nil(t, key)
					return
				}
				require.NoError(t, err)
				require.True(t, key.PublicKey.Equal(certificate.PublicKey))
			})
		})
	}
}
