package security

import (
	"crypto/x509"
	"fmt"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/kubebao/openbao-operator/internal/platform/cryptopolicy"
)

func parseTrustedRootWithPolicy(data []byte) (root.TrustedMaterial, error) {
	trusted, err := root.NewTrustedRootFromJSON(data)
	if err != nil {
		return nil, err
	}
	for _, logs := range []map[string]*root.TransparencyLog{trusted.RekorLogs(), trusted.CTLogs()} {
		for _, log := range logs {
			if err := cryptopolicy.ValidatePublicKey(log.PublicKey); err != nil {
				return nil, fmt.Errorf("transparency-log key policy: %w", err)
			}
		}
	}
	for _, authority := range trusted.FulcioCertificateAuthorities() {
		ca, ok := authority.(*root.FulcioCertificateAuthority)
		if !ok {
			return nil, fmt.Errorf("unsupported certificate authority type %T", authority)
		}
		chain := append([]*x509.Certificate{ca.Root}, ca.Intermediates...)
		if err := cryptopolicy.ValidateCertificates(chain); err != nil {
			return nil, fmt.Errorf("signing authority key policy: %w", err)
		}
	}
	for _, authority := range trusted.TimestampingAuthorities() {
		tsa, ok := authority.(*root.SigstoreTimestampingAuthority)
		if !ok {
			return nil, fmt.Errorf("unsupported timestamping authority type %T", authority)
		}
		chain := append([]*x509.Certificate{tsa.Root}, tsa.Intermediates...)
		if tsa.Leaf != nil {
			chain = append(chain, tsa.Leaf)
		}
		if err := cryptopolicy.ValidateCertificates(chain); err != nil {
			return nil, fmt.Errorf("timestamping authority key policy: %w", err)
		}
	}
	return trustedRootWithPolicy{TrustedRoot: trusted}, nil
}

type trustedRootWithPolicy struct{ *root.TrustedRoot }

func (r trustedRootWithPolicy) FulcioCertificateAuthorities() []root.CertificateAuthority {
	authorities := r.TrustedRoot.FulcioCertificateAuthorities()
	secured := make([]root.CertificateAuthority, 0, len(authorities))
	for _, authority := range authorities {
		secured = append(secured, certificateAuthorityWithPolicy{CertificateAuthority: authority})
	}
	return secured
}

type certificateAuthorityWithPolicy struct{ root.CertificateAuthority }

func (ca certificateAuthorityWithPolicy) Verify(cert *x509.Certificate, timestamp time.Time) ([][]*x509.Certificate, error) {
	if err := cryptopolicy.ValidateCertificates([]*x509.Certificate{cert}); err != nil {
		return nil, fmt.Errorf("image signing certificate key policy: %w", err)
	}
	chains, err := ca.CertificateAuthority.Verify(cert, timestamp)
	if err != nil {
		return nil, err
	}
	for _, chain := range chains {
		if err := cryptopolicy.ValidateCertificates(chain); err != nil {
			return nil, fmt.Errorf("image signing chain key policy: %w", err)
		}
	}
	return chains, nil
}
