package auth

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/rest"

	"github.com/kubebao/openbao-operator/internal/platform/cryptopolicy"
)

const defaultOIDCHTTPTimeout = 10 * time.Second

func issuerJWKSRediscoveryURL(ctx context.Context, issuerURL string) (string, bool) {
	if issuerURL == "" {
		return "", false
	}

	issuer, err := url.Parse(issuerURL)
	if err != nil || issuer.Scheme == "" || issuer.Host == "" {
		return "", false
	}

	wellKnownURL := strings.TrimRight(issuerURL, "/") + oidcWellKnownConfigurationPath
	transport := cryptopolicy.NewHTTPTransport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: defaultOIDCHTTPTimeout}
	oidcConfig, err := fetchOIDCWellKnown(ctx, client, wellKnownURL)
	if err != nil || oidcConfig == nil || oidcConfig.JWKSURI == "" {
		return "", false
	}

	return oidcConfig.JWKSURI, true
}

func clusterTransportJWKSConfig(cfg *rest.Config, jwksURL string) string {
	if cfg == nil || jwksURL == "" {
		return ""
	}

	jwks, err := url.Parse(jwksURL)
	if err != nil || jwks.Scheme == "" || jwks.Host == "" {
		return ""
	}
	if jwks.Scheme != "https" {
		return ""
	}

	if len(cfg.CAData) > 0 {
		return string(cfg.CAData)
	}
	if caFile := strings.TrimSpace(cfg.CAFile); caFile != "" {
		caPEM, err := os.ReadFile(caFile)
		if err == nil {
			return string(caPEM)
		}
	}
	caPEM, err := os.ReadFile(serviceAccountCAPath)
	if err != nil {
		return ""
	}
	return string(caPEM)
}

func dynamicOIDCDiscoveryConfig(baseURL, issuerURL, jwksURL, caPEM string) (string, string) {
	if caPEM != "" {
		if discoveryURL := canonicalClusterDiscoveryURL(baseURL); discoveryURL != "" && isStandardKubernetesJWKSURL(jwksURL) {
			return discoveryURL, caPEM
		}
	}
	if canUseIssuerDiscoveryURL(issuerURL, jwksURL) {
		return issuerURL, ""
	}
	return "", ""
}

func canUseIssuerDiscoveryURL(issuerURL, jwksURL string) bool {
	if issuerURL == "" || jwksURL == "" {
		return false
	}

	issuer, err := url.Parse(issuerURL)
	if err != nil || issuer.Scheme == "" || issuer.Host == "" {
		return false
	}
	jwks, err := url.Parse(jwksURL)
	if err != nil || jwks.Scheme == "" || jwks.Host == "" {
		return false
	}

	return strings.EqualFold(issuer.Scheme, jwks.Scheme) && strings.EqualFold(issuer.Host, jwks.Host)
}

func canonicalClusterDiscoveryURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: base.Scheme, Host: base.Host}).String()
}

func canonicalClusterJWKSURL(baseURL, jwksURL string) string {
	if baseURL == "" || jwksURL == "" {
		return jwksURL
	}

	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return jwksURL
	}
	jwks, err := url.Parse(jwksURL)
	if err != nil || jwks.Scheme == "" || jwks.Host == "" {
		return jwksURL
	}
	if !isStandardKubernetesJWKSURL(jwksURL) {
		return jwksURL
	}

	return base.ResolveReference(&url.URL{
		Path:     jwks.Path,
		RawQuery: jwks.RawQuery,
	}).String()
}

func kubernetesJWKSFallbackURL(baseURL, jwksURL string) (string, bool) {
	if baseURL == "" || jwksURL == "" {
		return "", false
	}

	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", false
	}
	jwks, err := url.Parse(jwksURL)
	if err != nil || jwks.Scheme == "" || jwks.Host == "" {
		return "", false
	}
	if jwks.Host == base.Host && jwks.Scheme == base.Scheme {
		return "", false
	}
	if !isStandardKubernetesJWKSURL(jwksURL) {
		return "", false
	}

	return base.ResolveReference(&url.URL{
		Path:     jwks.Path,
		RawQuery: jwks.RawQuery,
	}).String(), true
}

func isStandardKubernetesJWKSURL(jwksURL string) bool {
	if jwksURL == "" {
		return false
	}
	jwks, err := url.Parse(jwksURL)
	if err != nil {
		return false
	}
	switch jwks.Path {
	case kubernetesOIDCJWKSPath, legacyOIDCJWKSPath:
		return true
	default:
		return false
	}
}
