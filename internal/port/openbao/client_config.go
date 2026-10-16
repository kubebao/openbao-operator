package openbao

import (
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultConnectionTimeout is the default timeout for establishing connections.
	DefaultConnectionTimeout = 5 * time.Second
	// DefaultRequestTimeout is the default timeout for individual API requests.
	DefaultRequestTimeout = 10 * time.Second
	// DefaultSnapshotTimeout is the default timeout for snapshot operations.
	DefaultSnapshotTimeout = 30 * time.Minute
)

const (
	// JWTAuthStrategyInline authenticates each JWT-backed request with OpenBao inline auth.
	JWTAuthStrategyInline = "inline"
	// JWTAuthStrategyStandard performs the standard JWT login flow and sends X-Vault-Token.
	JWTAuthStrategyStandard = "standard"
	// DefaultJWTAuthStrategy is the default strategy for supported OpenBao versions.
	DefaultJWTAuthStrategy = JWTAuthStrategyInline
)

// NormalizeJWTAuthStrategy validates and normalizes a JWT auth strategy value.
func NormalizeJWTAuthStrategy(strategy string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(strategy))
	switch normalized {
	case "":
		return DefaultJWTAuthStrategy, nil
	case JWTAuthStrategyInline, JWTAuthStrategyStandard:
		return normalized, nil
	default:
		return "", fmt.Errorf("invalid OpenBao JWT auth strategy %q: expected %q or %q", strategy, JWTAuthStrategyInline, JWTAuthStrategyStandard)
	}
}

// NormalizeJWTAuthStrategyOrDefault returns a valid strategy, falling back to
// the default when the provided value is empty or invalid. Callers that accept
// user input should use NormalizeJWTAuthStrategy and surface validation errors.
func NormalizeJWTAuthStrategyOrDefault(strategy string) string {
	normalized, err := NormalizeJWTAuthStrategy(strategy)
	if err != nil {
		return DefaultJWTAuthStrategy
	}
	return normalized
}

// ClientConfig describes connectivity and auth options for OpenBao API clients.
type ClientConfig struct {
	// ClusterKey is an optional per-cluster identifier used to share client state
	// (rate limiting and circuit breakers) across multiple Client instances.
	//
	// ClientManager keys must identify one cluster lifetime, such as a Kubernetes UID.
	// NewClient does not share state; use a ClientManager factory for shared state.
	ClusterKey string

	// BaseURL is the OpenBao API URL (e.g., "https://pod-0.cluster.ns.svc:8200").
	BaseURL string
	// Token is the authentication token for OpenBao API calls.
	Token string
	// JWTAuthStrategy controls how JWT-backed clients authenticate to OpenBao.
	// Empty defaults to inline authentication.
	JWTAuthStrategy string
	// CACert is the PEM-encoded CA certificate for TLS verification.
	// If empty, the system certificate pool is used.
	CACert []byte
	// TLSServerName overrides the hostname used for TLS certificate verification.
	// When empty, the hostname from BaseURL is used.
	TLSServerName string
	// ConnectionTimeout is the timeout for establishing connections.
	// Defaults to DefaultConnectionTimeout if zero.
	ConnectionTimeout time.Duration
	// RequestTimeout is the timeout for individual requests.
	// Defaults to DefaultRequestTimeout if zero.
	RequestTimeout time.Duration

	// SmartClientDisabled disables rate limiting and circuit breaker behavior.
	// By default, smart client features are enabled with conservative defaults.
	SmartClientDisabled bool
	// RateLimitQPS is the per-cluster rate limit applied to OpenBao API calls.
	// Defaults to 2.0 if zero or negative.
	RateLimitQPS float64
	// RateLimitBurst is the per-cluster burst size for the rate limiter.
	// Defaults to 4 if zero or negative.
	RateLimitBurst int
	// CircuitBreakerFailureThreshold is the number of consecutive failures before opening the circuit.
	// Defaults to 50 if zero or negative.
	CircuitBreakerFailureThreshold int
	// CircuitBreakerOpenDuration is the amount of time the circuit stays open before probing again.
	// Defaults to 30s if zero.
	CircuitBreakerOpenDuration time.Duration
}
