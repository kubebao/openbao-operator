package openbao

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/kubebao/openbao-operator/internal/platform/cryptopolicy"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

// Client provides access to OpenBao's system API endpoints.
// It is used by the UpgradeManager to check node health and perform leader step-down.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client

	state *clientState
	auth  requestAuthorizer
}

// NewClient creates a new OpenBao API client with the given configuration.
// The client is configured to trust the provided CA certificate for TLS verification.
func NewClient(config portopenbao.ClientConfig) (*Client, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("baseURL is required")
	}

	if config.CACert == nil {
		config.CACert = []byte{}
	}

	connectionTimeout := config.ConnectionTimeout
	if connectionTimeout == 0 {
		connectionTimeout = portopenbao.DefaultConnectionTimeout
	}

	requestTimeout := config.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = portopenbao.DefaultRequestTimeout
	}

	// Parse the base URL to extract the hostname for server name verification
	parsedURL, err := url.Parse(config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid baseURL %q: %w", config.BaseURL, err)
	}

	// Configure TLS with the provided CA certificate
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	cryptopolicy.ConfigureTLS(tlsConfig)

	// Set ServerName explicitly when the operator needs to verify against a
	// stable TLS identity instead of the connection hostname (for example, ACME
	// clusters and direct pod connections during day-2 operations).
	if config.TLSServerName != "" {
		tlsConfig.ServerName = config.TLSServerName
	} else if parsedURL.Hostname() != "" {
		tlsConfig.ServerName = parsedURL.Hostname()
	}

	// If a per-cluster CA bundle is provided, trust only that bundle.
	// If empty, use system roots (default behavior when RootCAs is nil).
	if len(config.CACert) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(config.CACert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		if err := cryptopolicy.ValidatePEMCertificates(config.CACert); err != nil {
			return nil, fmt.Errorf("CA key policy: %w", err)
		}
		tlsConfig.RootCAs = pool
	}

	transport := &http.Transport{
		TLSClientConfig:     tlsConfig,
		TLSHandshakeTimeout: connectionTimeout,
		DisableKeepAlives:   false,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
	}

	var state *clientState

	return &Client{
		baseURL:    config.BaseURL,
		token:      config.Token,
		httpClient: httpClient,
		state:      state,
		auth:       newTokenAuthorizer(config.Token),
	}, nil
}

// newClientWithState creates a Client with explicit client state.
// This is used by ClientFactory when created via ClientManager.
func newClientWithState(config portopenbao.ClientConfig, state *clientState) (*Client, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("baseURL is required")
	}

	if config.CACert == nil {
		config.CACert = []byte{}
	}

	connectionTimeout := config.ConnectionTimeout
	if connectionTimeout == 0 {
		connectionTimeout = portopenbao.DefaultConnectionTimeout
	}

	requestTimeout := config.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = portopenbao.DefaultRequestTimeout
	}

	// Parse the base URL to extract the hostname for server name verification
	parsedURL, err := url.Parse(config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid baseURL %q: %w", config.BaseURL, err)
	}

	// Configure TLS with the provided CA certificate
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	cryptopolicy.ConfigureTLS(tlsConfig)

	if config.TLSServerName != "" {
		tlsConfig.ServerName = config.TLSServerName
	} else if parsedURL.Hostname() != "" {
		tlsConfig.ServerName = parsedURL.Hostname()
	}

	// If a per-cluster CA bundle is provided, trust only that bundle.
	if len(config.CACert) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(config.CACert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		if err := cryptopolicy.ValidatePEMCertificates(config.CACert); err != nil {
			return nil, fmt.Errorf("CA key policy: %w", err)
		}
		tlsConfig.RootCAs = pool
	}

	transport := &http.Transport{
		TLSClientConfig:     tlsConfig,
		TLSHandshakeTimeout: connectionTimeout,
		DisableKeepAlives:   false,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
	}

	return &Client{
		baseURL:    config.BaseURL,
		token:      config.Token,
		httpClient: httpClient,
		state:      state,
		auth:       newTokenAuthorizer(config.Token),
	}, nil
}
