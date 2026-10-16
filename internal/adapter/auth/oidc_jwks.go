package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"

	"k8s.io/client-go/rest"

	"github.com/kubebao/openbao-operator/internal/platform/cryptopolicy"
	"github.com/kubebao/openbao-operator/internal/platform/kubernetestls"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
)

type jwksDocument struct {
	Keys []jwkKey `json:"keys"`
}

type jwkKey struct {
	Kty string `json:"kty"`

	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`

	N string `json:"n,omitempty"`
	E string `json:"e,omitempty"`

	X5c []string `json:"x5c,omitempty"`
}

// FetchJWKSKeys fetches and parses JWKS keys from the provided JWKS URI.
func FetchJWKSKeys(ctx context.Context, cfg *rest.Config, jwksURL string) ([]string, error) {
	if jwksURL == "" {
		return nil, fmt.Errorf("jwks URL is required")
	}

	transport, err := rest.TransportFor(kubernetestls.WithPolicy(cfg))
	if err != nil {
		return nil, fmt.Errorf("failed to create transport: %w", err)
	}

	httpClient := &http.Client{Transport: transport, Timeout: defaultOIDCHTTPTimeout}
	return fetchJWKSKeysWithClient(ctx, httpClient, jwksURL)
}

func fetchJWKSKeysPublic(ctx context.Context, jwksURL string) ([]string, error) {
	transport := cryptopolicy.NewHTTPTransport()
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: defaultOIDCHTTPTimeout}
	return fetchJWKSKeysWithClient(ctx, httpClient, jwksURL)
}

func fetchJWKSKeysWithClient(ctx context.Context, httpClient *http.Client, jwksURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create jwks request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch jwks endpoint: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			_ = err
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPStatusError{URL: jwksURL, StatusCode: resp.StatusCode}
	}

	var jwks jwksDocument
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("%w: failed to parse jwks document: %w", portauth.ErrDiscoveryContentInvalid, err)
	}

	keys, err := pemPublicKeysFromJWKS(jwks)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to extract public keys from jwks: %w", portauth.ErrDiscoveryContentInvalid, err)
	}

	return keys, nil
}

func pemPublicKeysFromJWKS(jwks jwksDocument) ([]string, error) {
	pemKeys := make([]string, 0, len(jwks.Keys))
	seen := make(map[string]struct{}, len(jwks.Keys))

	for _, key := range jwks.Keys {
		pemKey, err := pemPublicKeyFromJWK(key)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[pemKey]; ok {
			continue
		}
		seen[pemKey] = struct{}{}
		pemKeys = append(pemKeys, pemKey)
	}

	if len(pemKeys) == 0 {
		return nil, fmt.Errorf("no public keys found in jwks")
	}
	return pemKeys, nil
}

func pemPublicKeyFromJWK(key jwkKey) (string, error) {
	if len(key.X5c) > 0 {
		return pemPublicKeyFromX5C(key.X5c[0])
	}

	switch key.Kty {
	case "RSA":
		return pemPublicKeyFromRSA(key)
	case "EC":
		return pemPublicKeyFromEC(key)
	default:
		return "", fmt.Errorf("unsupported jwk key type %q", key.Kty)
	}
}

func pemPublicKeyFromX5C(encodedCert string) (string, error) {
	certDER, err := base64.StdEncoding.DecodeString(encodedCert)
	if err != nil {
		return "", fmt.Errorf("failed to decode jwk x5c certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return "", fmt.Errorf("failed to parse jwk x5c certificate: %w", err)
	}
	if err := cryptopolicy.ValidatePublicKey(cert.PublicKey); err != nil {
		return "", fmt.Errorf("jwk x5c key policy: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return "", fmt.Errorf("failed to marshal jwk x5c public key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: publicKeyPEMBlockType, Bytes: pubDER})), nil
}

func pemPublicKeyFromRSA(key jwkKey) (string, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
	if err != nil {
		return "", fmt.Errorf("failed to decode rsa modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
	if err != nil {
		return "", fmt.Errorf("failed to decode rsa exponent: %w", err)
	}
	if len(eBytes) == 0 {
		return "", fmt.Errorf("rsa exponent is empty")
	}

	exponent := 0
	for _, b := range eBytes {
		exponent = exponent<<8 | int(b)
	}

	pubKey := &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: exponent,
	}
	if err := cryptopolicy.ValidatePublicKey(pubKey); err != nil {
		return "", fmt.Errorf("jwk RSA key policy: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		return "", fmt.Errorf("failed to marshal rsa public key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: publicKeyPEMBlockType, Bytes: pubDER})), nil
}

func pemPublicKeyFromEC(key jwkKey) (string, error) {
	var curve elliptic.Curve
	switch key.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return "", fmt.Errorf("unsupported ec curve %q", key.Crv)
	}

	xBytes, err := base64.RawURLEncoding.DecodeString(key.X)
	if err != nil {
		return "", fmt.Errorf("failed to decode ec x coordinate: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(key.Y)
	if err != nil {
		return "", fmt.Errorf("failed to decode ec y coordinate: %w", err)
	}

	//nolint:staticcheck // JWK supplies public coordinates; x509 validates the point before encoding.
	pubKey := &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}
	if err := cryptopolicy.ValidatePublicKey(pubKey); err != nil {
		return "", fmt.Errorf("jwk EC key policy: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		return "", fmt.Errorf("failed to marshal ec public key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: publicKeyPEMBlockType, Bytes: pubDER})), nil
}
