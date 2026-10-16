package cryptopolicy

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidatePublicKey(t *testing.T) {
	rsaKey := func(bits int) *rsa.PublicKey {
		return &rsa.PublicKey{N: new(big.Int).Lsh(big.NewInt(1), uint(bits-1)), E: 65537}
	}
	ecKey := func(curve elliptic.Curve) *ecdsa.PublicKey {
		key, err := ecdsa.GenerateKey(curve, rand.Reader)
		require.NoError(t, err)
		return &key.PublicKey
	}
	for _, tt := range []struct {
		name string
		key  crypto.PublicKey
		bad  bool
	}{
		{name: "RSA 1024", key: rsaKey(1024), bad: true},
		{name: "RSA 2047", key: rsaKey(2047), bad: true},
		{name: "RSA 2048", key: rsaKey(2048)},
		{name: "RSA 4096", key: rsaKey(4096)},
		{name: "nil RSA", key: (*rsa.PublicKey)(nil), bad: true},
		{name: "missing RSA modulus", key: &rsa.PublicKey{E: 65537}, bad: true},
		{name: "P-224", key: ecKey(elliptic.P224())},
		{name: "P-256", key: ecKey(elliptic.P256())},
		{name: "P-384", key: ecKey(elliptic.P384())},
		{name: "P-521", key: ecKey(elliptic.P521())},
		{name: "nil ECDSA", key: (*ecdsa.PublicKey)(nil), bad: true},
		{name: "Ed25519", key: ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))},
		{name: "truncated Ed25519", key: ed25519.PublicKey(make([]byte, 16)), bad: true},
		{name: "unknown key", key: []byte("key"), bad: true},
		{name: "nil", bad: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePublicKey(tt.key)
			if tt.bad {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
