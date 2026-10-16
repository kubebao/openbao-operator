package cryptopolicy

import (
	"crypto/tls"
	"fmt"
	"net/http"
)

// ConfigureTLS requires TLS 1.2+, ephemeral AEAD suites, and strong peer keys.
// Go retains control of TLS 1.3 cipher suites and key-exchange groups.
func ConfigureTLS(config *tls.Config) {
	if config.MinVersion < tls.VersionTLS12 {
		config.MinVersion = tls.VersionTLS12
	}
	config.CipherSuites = []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	}
	previous := config.VerifyConnection
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if err := ValidateCertificates(state.PeerCertificates); err != nil {
			return fmt.Errorf("TLS peer key policy: %w", err)
		}
		for _, chain := range state.VerifiedChains {
			if err := ValidateCertificates(chain); err != nil {
				return fmt.Errorf("TLS verified-chain key policy: %w", err)
			}
		}
		if previous != nil {
			return previous(state)
		}
		return nil
	}
}

// ConfigureServerTLS also checks serving certificates, including watcher updates.
// Select the final callbacks at handshake time because the metrics server installs
// its certificate watcher after applying TLS options.
func ConfigureServerTLS(config *tls.Config) {
	ConfigureTLS(config)
	previous := config.GetConfigForClient
	config.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		selected := config
		if previous != nil {
			next, err := previous(hello)
			if err != nil {
				return nil, err
			}
			if next != nil {
				selected = next
			}
		}
		secured := selected.Clone()
		secured.GetConfigForClient = nil
		if selected != config {
			ConfigureTLS(secured)
		}
		for i := range secured.Certificates {
			if err := ValidateTLSCertificate(&secured.Certificates[i]); err != nil {
				return nil, fmt.Errorf("TLS serving key policy: %w", err)
			}
		}
		if getCertificate := secured.GetCertificate; getCertificate != nil {
			secured.GetCertificate = func(info *tls.ClientHelloInfo) (*tls.Certificate, error) {
				certificate, err := getCertificate(info)
				if err != nil || certificate == nil {
					return certificate, err
				}
				if err := ValidateTLSCertificate(certificate); err != nil {
					return nil, fmt.Errorf("TLS serving key policy: %w", err)
				}
				return certificate, nil
			}
		}
		return secured, nil
	}
}

// NewHTTPTransport returns an independent transport with the operator TLS policy.
func NewHTTPTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	ConfigureTLS(transport.TLSClientConfig)
	return transport
}
