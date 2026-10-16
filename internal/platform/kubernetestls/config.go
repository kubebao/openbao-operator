// Package kubernetestls applies the operator TLS policy to client-go transports.
package kubernetestls

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"

	"github.com/kubebao/openbao-operator/internal/platform/cryptopolicy"
)

// WithPolicy copies config and secures each transport before its first use.
// A dial holder gives each client-go transport a private cache key. This avoids
// mutating shared transports and retains client-go's CA and client-key rotation.
func WithPolicy(config *rest.Config) *rest.Config {
	secured := rest.CopyConfig(config)
	if secured.Dial == nil {
		secured.Dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	if secured.Transport != nil {
		if supplied, ok := secured.Transport.(*http.Transport); ok {
			secured.Transport = supplied.Clone()
		} else {
			secured.Transport = failedTransport{err: fmt.Errorf("custom Kubernetes transport must be an HTTP transport")}
		}
	}
	secured.WrapTransport = transport.Wrappers(secureTransport, secured.WrapTransport)
	return secured
}

func secureTransport(roundTripper http.RoundTripper) http.RoundTripper {
	base := roundTripper
	for {
		wrapper, ok := base.(utilnet.RoundTripperWrapper)
		if !ok {
			break
		}
		base = wrapper.WrappedRoundTripper()
	}
	httpTransport, ok := base.(*http.Transport)
	if !ok {
		return failedTransport{err: fmt.Errorf("unsupported Kubernetes transport %T: TLS configuration is unavailable", base)}
	}
	if httpTransport.TLSClientConfig == nil {
		httpTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	config := httpTransport.TLSClientConfig
	cryptopolicy.ConfigureTLS(config)
	for i := range config.Certificates {
		if err := cryptopolicy.ValidateTLSCertificate(&config.Certificates[i]); err != nil {
			return failedTransport{err: fmt.Errorf("client certificate key policy: %w", err)}
		}
	}
	if getCertificate := config.GetClientCertificate; getCertificate != nil {
		config.GetClientCertificate = func(info *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			certificate, err := getCertificate(info)
			if err != nil || certificate == nil || len(certificate.Certificate) == 0 {
				return certificate, err
			}
			if err := cryptopolicy.ValidateTLSCertificate(certificate); err != nil {
				return nil, fmt.Errorf("client certificate key policy: %w", err)
			}
			return certificate, nil
		}
	}
	return roundTripper
}

type failedTransport struct{ err error }

func (t failedTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }
