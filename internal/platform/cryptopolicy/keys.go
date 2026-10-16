// Package cryptopolicy defines the operator's minimum public-key and TLS policy.
package cryptopolicy

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// ValidatePublicKey checks the algorithm and minimum size of an imported key.
func ValidatePublicKey(publicKey crypto.PublicKey) error {
	switch key := publicKey.(type) {
	case *rsa.PublicKey:
		if key == nil || key.N == nil || key.N.Sign() <= 0 || key.E < 3 || key.E%2 == 0 {
			return fmt.Errorf("invalid RSA public key")
		}
		if bits := key.N.BitLen(); bits < 2048 {
			return fmt.Errorf("RSA public key has %d bits; at least 2048 bits are required", bits)
		}
	case *ecdsa.PublicKey:
		if key == nil || key.Curve == nil || key.Params() == nil {
			return fmt.Errorf("invalid ECDSA public key")
		}
		if bits := key.Params().BitSize; bits < 224 {
			return fmt.Errorf("ECDSA curve has %d bits; at least 224 bits are required", bits)
		}
		//nolint:staticcheck // Guard incomplete legacy keys before Bytes reads their coordinates.
		if key.X == nil || key.Y == nil {
			return fmt.Errorf("invalid ECDSA public key")
		}
		if _, err := key.Bytes(); err != nil {
			return fmt.Errorf("invalid ECDSA public key: %w", err)
		}
	case ed25519.PublicKey:
		if len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid Ed25519 public key length")
		}
	default:
		return fmt.Errorf("unsupported public key type %T", publicKey)
	}
	return nil
}

// ValidateCertificates checks every certificate's public key, including trust anchors.
func ValidateCertificates(certificates []*x509.Certificate) error {
	for i, certificate := range certificates {
		if certificate == nil {
			return fmt.Errorf("certificate %d is nil", i)
		}
		if err := ValidatePublicKey(certificate.PublicKey); err != nil {
			return fmt.Errorf("certificate %d: %w", i, err)
		}
	}
	return nil
}

// ValidatePEMCertificates checks all certificate blocks in a supplied PEM bundle.
func ValidatePEMCertificates(data []byte) error {
	count := 0
	for len(data) > 0 {
		block, rest := pem.Decode(data)
		if block == nil {
			break
		}
		data = rest
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("parse certificate %d: %w", count, err)
		}
		if err := ValidatePublicKey(certificate.PublicKey); err != nil {
			return fmt.Errorf("certificate %d: %w", count, err)
		}
		count++
	}
	if count == 0 {
		return fmt.Errorf("expected one or more PEM certificates")
	}
	return nil
}

// ValidateTLSCertificate checks the actual DER chain rather than cached leaf metadata.
func ValidateTLSCertificate(certificate *tls.Certificate) error {
	if certificate == nil || len(certificate.Certificate) == 0 {
		return fmt.Errorf("TLS certificate chain is empty")
	}
	for i, der := range certificate.Certificate {
		parsed, err := x509.ParseCertificate(der)
		if err != nil {
			return fmt.Errorf("parse TLS certificate %d: %w", i, err)
		}
		if err := ValidatePublicKey(parsed.PublicKey); err != nil {
			return fmt.Errorf("TLS certificate %d: %w", i, err)
		}
	}
	return nil
}
