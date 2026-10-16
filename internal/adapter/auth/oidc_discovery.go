package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"k8s.io/client-go/rest"

	"github.com/kubebao/openbao-operator/internal/platform/kubernetestls"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
)

type oidcWellKnownDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// DiscoverConfig fetches the Kubernetes OIDC issuer configuration from the Kubernetes API server.
func DiscoverConfig(ctx context.Context, cfg *rest.Config, baseURL string) (*portauth.OIDCConfig, error) {
	if baseURL == "" {
		baseURL = defaultKubernetesOIDCDiscoveryBaseURL
	}
	wellKnownURL := baseURL + oidcWellKnownConfigurationPath

	transport, err := rest.TransportFor(kubernetestls.WithPolicy(cfg))
	if err != nil {
		return nil, fmt.Errorf("failed to create transport: %w", err)
	}

	httpClient := &http.Client{Transport: transport, Timeout: defaultOIDCHTTPTimeout}
	oidcConfig, err := fetchOIDCWellKnown(ctx, httpClient, wellKnownURL)
	if err != nil {
		return nil, err
	}
	if oidcConfig.Issuer == "" {
		return nil, fmt.Errorf("%w: OIDC config missing issuer", portauth.ErrDiscoveryContentInvalid)
	}

	return buildDiscoveredOIDCConfig(ctx, cfg, baseURL, oidcConfig)
}

func buildDiscoveredOIDCConfig(
	ctx context.Context,
	cfg *rest.Config,
	baseURL string,
	oidcConfig *oidcWellKnownDocument,
) (*portauth.OIDCConfig, error) {
	issuerURL := oidcConfig.Issuer
	var (
		jwksKeys       []string
		effective      string
		jwksCAPEM      string
		discoveryURL   string
		discoveryCAPEM string
	)

	if oidcConfig.JWKSURI != "" {
		keys, err := FetchJWKSKeys(ctx, cfg, oidcConfig.JWKSURI)
		if err == nil {
			jwksKeys = keys
			jwksCAPEM = clusterTransportJWKSConfig(cfg, oidcConfig.JWKSURI)
			discoveryURL, discoveryCAPEM = dynamicOIDCDiscoveryConfig(baseURL, issuerURL, oidcConfig.JWKSURI, jwksCAPEM)
			effective = canonicalClusterJWKSURL(baseURL, oidcConfig.JWKSURI)
			if discoveryURL != "" {
				effective = ""
				jwksCAPEM = ""
			}
		}
		if err != nil {
			if fallbackURL, ok := issuerJWKSRediscoveryURL(ctx, issuerURL); ok {
				keys, err = fetchJWKSKeysPublic(ctx, fallbackURL)
				if err == nil {
					jwksKeys = keys
					discoveryURL = issuerURL
					discoveryCAPEM = ""
					effective = ""
					jwksCAPEM = ""
				}
			}
		}
		if err != nil {
			if fallbackURL, ok := kubernetesJWKSFallbackURL(baseURL, oidcConfig.JWKSURI); ok {
				keys, err = FetchJWKSKeys(ctx, cfg, fallbackURL)
				if err == nil {
					jwksKeys = keys
					discoveryURL = canonicalClusterDiscoveryURL(baseURL)
					discoveryCAPEM = clusterTransportJWKSConfig(cfg, fallbackURL)
					effective = ""
					jwksCAPEM = ""
				}
			}
		}
		if err != nil {
			return &portauth.OIDCConfig{
				IssuerURL:          issuerURL,
				OIDCDiscoveryURL:   "",
				OIDCDiscoveryCAPEM: "",
				JWKSURL:            "",
				JWKSCAPEM:          "",
				JWKSKeys:           nil,
			}, fmt.Errorf("failed to fetch JWKS keys: %w", err)
		}
	}

	return &portauth.OIDCConfig{
		IssuerURL:          issuerURL,
		OIDCDiscoveryURL:   discoveryURL,
		OIDCDiscoveryCAPEM: discoveryCAPEM,
		JWKSURL:            effective,
		JWKSCAPEM:          jwksCAPEM,
		JWKSKeys:           jwksKeys,
	}, nil
}

func fetchOIDCWellKnown(ctx context.Context, httpClient *http.Client, wellKnownURL string) (*oidcWellKnownDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnownURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC discovery request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OIDC well-known endpoint: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			_ = err
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPStatusError{URL: wellKnownURL, StatusCode: resp.StatusCode}
	}

	var oidcConfig oidcWellKnownDocument
	if err := json.NewDecoder(resp.Body).Decode(&oidcConfig); err != nil {
		return nil, fmt.Errorf("%w: failed to parse OIDC discovery document: %w", portauth.ErrDiscoveryContentInvalid, err)
	}

	return &oidcConfig, nil
}
