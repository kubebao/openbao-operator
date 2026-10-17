package security

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/sigstore/cosign/v3/pkg/cosign"
	"github.com/sigstore/cosign/v3/pkg/oci"
	"github.com/sigstore/cosign/v3/pkg/oci/static"
	"github.com/stretchr/testify/require"

	"github.com/kubebao/openbao-operator/internal/port/imageverify"
)

func TestImageVerifierClaimVerification(t *testing.T) {
	_, publicKey := newImageClaimSigningKey(t)
	trustedRoot, err := parseTrustedRootWithPolicy(embeddedTrustedRootJSON)
	require.NoError(t, err)
	digest, err := v1.NewHash(strings.Split(testImageDigest, "@")[1])
	require.NoError(t, err)
	otherDigest := "sha256:" + strings.Repeat("1", 64)

	for _, mode := range []struct {
		name     string
		config   imageverify.VerifyConfig
		identity cosign.Identity
	}{
		{
			name:   "static key",
			config: imageverify.VerifyConfig{PublicKey: publicKey, IgnoreTlog: true},
		},
		{
			name:   "static key with transparency log",
			config: imageverify.VerifyConfig{PublicKey: publicKey},
		},
		{
			name: "keyless exact identity",
			config: imageverify.VerifyConfig{
				Issuer: testOIDCIssuer, Subject: testOIDCSubject, IgnoreTlog: true,
			},
			identity: cosign.Identity{Issuer: testOIDCIssuer, Subject: testOIDCSubject},
		},
		{
			name: "keyless regular expression identity",
			config: imageverify.VerifyConfig{
				IssuerRegExp: "^https://issuer.example$", SubjectRegExp: "^https://signer.example/.+$", IgnoreTlog: true,
			},
			identity: cosign.Identity{
				IssuerRegExp: "^https://issuer.example$", SubjectRegExp: "^https://signer.example/.+$",
			},
		},
	} {
		t.Run(mode.name, func(t *testing.T) {
			for _, format := range []struct {
				name   string
				bundle bool
			}{
				{name: "legacy"},
				{name: "bundle", bundle: true},
			} {
				t.Run(format.name, func(t *testing.T) {
					opts, err := (&ImageVerifier{}).buildCheckOpts(mode.config, nil, format.bundle, trustedRoot)
					require.NoError(t, err)
					require.NotNil(t, opts.ClaimVerifier)
					require.Equal(t, format.bundle, opts.NewBundleFormat)
					if mode.config.PublicKey != "" {
						require.NotNil(t, opts.SigVerifier)
						require.Equal(t, mode.config.IgnoreTlog, opts.IgnoreTlog)
						if !mode.config.IgnoreTlog {
							require.Equal(t, trustedRoot, opts.TrustedMaterial)
						}
					} else {
						require.Nil(t, opts.SigVerifier)
						require.Equal(t, trustedRoot, opts.TrustedMaterial)
						require.Equal(t, []cosign.Identity{mode.identity}, opts.Identities)
						require.False(t, opts.IgnoreTlog, "keyless verification must retain transparency log checks")
					}

					claim := simpleImageClaim
					otherFormat := bundleImageClaim
					if format.bundle {
						claim, otherFormat = bundleImageClaim, simpleImageClaim
					}
					for _, tt := range []struct {
						name    string
						payload []byte
						wantErr bool
					}{
						{name: "matching digest", payload: claim(digest.String())},
						{name: "different digest", payload: claim(otherDigest), wantErr: true},
						{name: "missing digest", payload: claim(""), wantErr: true},
						{name: "missing claim", payload: []byte("{}"), wantErr: true},
						{name: "null claim", payload: []byte("null"), wantErr: true},
						{name: "malformed claim", payload: []byte("{"), wantErr: true},
						{name: "other format", payload: otherFormat(digest.String()), wantErr: true},
					} {
						t.Run(tt.name, func(t *testing.T) {
							sig, err := static.NewSignature(tt.payload, "")
							require.NoError(t, err)
							err = opts.ClaimVerifier(sig, digest, nil)
							if tt.wantErr {
								require.Error(t, err)
							} else {
								require.NoError(t, err)
							}
						})
					}
				})
			}
		})
	}
}

func TestImageVerifierClaimsRequireTrustedRoot(t *testing.T) {
	_, publicKey := newImageClaimSigningKey(t)
	for _, tt := range []struct {
		name   string
		config imageverify.VerifyConfig
	}{
		{name: "static key with transparency log", config: imageverify.VerifyConfig{PublicKey: publicKey}},
		{name: "keyless", config: imageverify.VerifyConfig{Issuer: testOIDCIssuer, Subject: testOIDCSubject}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, bundle := range []bool{false, true} {
				opts, err := (&ImageVerifier{}).buildCheckOpts(tt.config, nil, bundle, nil)
				require.ErrorContains(t, err, "trusted root material is required")
				require.Nil(t, opts)
			}
		})
	}
}

func TestImageVerifierVerifyLegacySignatureFromRegistry(t *testing.T) {
	key, publicKey := newImageClaimSigningKey(t)
	_, otherPublicKey := newImageClaimSigningKey(t)
	digest, err := v1.NewHash(strings.Split(testImageDigest, "@")[1])
	require.NoError(t, err)
	for _, tt := range []struct {
		name        string
		payload     []byte
		publicKey   string
		wantErrPart string
	}{
		{name: "matching digest", payload: simpleImageClaim(digest.String())},
		{
			name: "signed claim for another image", payload: simpleImageClaim("sha256:" + strings.Repeat("1", 64)),
			wantErrPart: "invalid or missing digest in claim",
		},
		{name: "signed claim without digest", payload: []byte("{}"), wantErrPart: "invalid or missing digest in claim"},
		{name: "signed malformed claim", payload: []byte("{"), wantErrPart: "unexpected end of JSON input"},
		{
			name: "matching digest with untrusted signer", payload: simpleImageClaim(digest.String()),
			publicKey: otherPublicKey, wantErrPart: "invalid signature",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payloadHash := sha256.Sum256(tt.payload)
			signature, err := ecdsa.SignASN1(rand.Reader, key, payloadHash[:])
			require.NoError(t, err)
			require.True(t, ecdsa.VerifyASN1(&key.PublicKey, payloadHash[:], signature), "the signature must be valid")
			sig, err := static.NewSignature(tt.payload, base64.StdEncoding.EncodeToString(signature))
			require.NoError(t, err)
			digestRef := serveLegacyImageSignature(t, sig, digest)
			verifier := NewImageVerifier(logr.Discard(), nil, nil)
			verificationKey := publicKey
			if tt.publicKey != "" {
				verificationKey = tt.publicKey
			}
			result, err := verifier.Verify(context.Background(), digestRef, imageverify.VerifyConfig{
				PublicKey: verificationKey, IgnoreTlog: true,
			})
			if tt.wantErrPart != "" {
				require.ErrorContains(t, err, tt.wantErrPart)
				require.Empty(t, result)
				return
			}
			require.NoError(t, err)
			require.Equal(t, digestRef, result)
		})
	}
}

func newImageClaimSigningKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func simpleImageClaim(digest string) []byte {
	return []byte(fmt.Sprintf(`{"critical":{"image":{"docker-manifest-digest":%q}}}`, digest))
}

func bundleImageClaim(digest string) []byte {
	statement := fmt.Sprintf(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"test/image","digest":{"sha256":%q}}],"predicateType":%q,"predicate":{}}`,
		strings.TrimPrefix(digest, "sha256:"), cosignSignaturePredicateTypeV1)
	return []byte(fmt.Sprintf(`{"payloadType":%q,"payload":%q}`, dsseInTotoPayloadType,
		base64.StdEncoding.EncodeToString([]byte(statement))))
}

// serveLegacyImageSignature exposes a Cosign signature image through the registry read API.
// The signature tag is derived from the requested image, independently of the signed claim.
func serveLegacyImageSignature(t *testing.T, sig oci.Signature, imageDigest v1.Hash) string {
	t.Helper()
	annotations, err := sig.Annotations()
	require.NoError(t, err)
	image, err := mutate.Append(empty.Image, mutate.Addendum{Layer: sig, Annotations: annotations})
	require.NoError(t, err)
	manifest, err := image.RawManifest()
	require.NoError(t, err)
	manifestDigest, err := image.Digest()
	require.NoError(t, err)
	mediaType, err := image.MediaType()
	require.NoError(t, err)
	payload, err := sig.Payload()
	require.NoError(t, err)
	payloadDigest, err := sig.Digest()
	require.NoError(t, err)
	config, err := image.RawConfigFile()
	require.NoError(t, err)
	configDigest, err := image.ConfigName()
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/test/image/manifests/" + imageDigest.Algorithm + "-" + imageDigest.Hex + ".sig":
			w.Header().Set("Content-Type", string(mediaType))
			w.Header().Set("Docker-Content-Digest", manifestDigest.String())
			_, _ = w.Write(manifest)
		case "/v2/test/image/blobs/" + payloadDigest.String():
			_, _ = w.Write(payload)
		case "/v2/test/image/blobs/" + configDigest.String():
			_, _ = w.Write(config)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://") + "/test/image@" + imageDigest.String()
}
