package security

import (
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	ggcrremote "github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/sigstore/cosign/v3/pkg/cosign"
	"github.com/sigstore/cosign/v3/pkg/oci"
	ociremote "github.com/sigstore/cosign/v3/pkg/oci/remote"
	"github.com/sigstore/cosign/v3/pkg/signature"
	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/kubebao/openbao-operator/internal/platform/cryptopolicy"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/port/imageverify"
)

func (v *ImageVerifier) verifyImageSignature(
	ctx context.Context,
	digestRef string,
	config imageverify.VerifyConfig,
	trustedRoot root.TrustedMaterial,
) error {
	ref, err := name.ParseReference(digestRef)
	if err != nil {
		return fmt.Errorf("failed to parse digest reference: %w", err)
	}

	remoteOpts := []ociremote.Option{
		ociremote.WithRemoteOptions(ggcrremote.WithTransport(imageRegistryTransport)),
	}
	if len(config.ImagePullSecrets) > 0 {
		keychain, err := v.buildKeychain(ctx, config.ImagePullSecrets, config.Namespace)
		if err != nil {
			return fmt.Errorf("failed to build keychain for image pull secrets: %w", err)
		}
		if keychain != nil {
			remoteOpts = append(remoteOpts, ociremote.WithRemoteOptions(ggcrremote.WithAuthFromKeychain(keychain)))
		}
	}

	legacyCheckOpts, err := v.buildCheckOpts(config, remoteOpts, false, trustedRoot)
	if err != nil {
		return err
	}

	sigs, bundleVerified, legacyErr := cosign.VerifyImageSignatures(ctx, ref, legacyCheckOpts)
	if legacyErr == nil && len(sigs) > 0 {
		v.logger.V(1).Info("Image signature verification completed",
			"digest", digestRef,
			"signatures", len(sigs),
			"bundleVerified", bundleVerified,
			"rekorVerified", !legacyCheckOpts.IgnoreTlog,
			"format", "legacy")
		return nil
	}
	if legacyErr == nil {
		legacyErr = fmt.Errorf("no signatures found for image %q", digestRef)
	}

	if !shouldAttemptBundleFallback(legacyErr) {
		if operatorerrors.IsTransientConnection(legacyErr) {
			return operatorerrors.WrapTransientConnection(fmt.Errorf("image signature verification failed: %w", legacyErr))
		}
		return fmt.Errorf("image signature verification failed: %w", legacyErr)
	}

	if err := v.verifyImageSignatureWithBundles(ctx, ref, digestRef, config, remoteOpts, trustedRoot); err != nil {
		return fmt.Errorf("image signature verification failed (legacy+bundle): legacy=%v; bundle=%w", legacyErr, err)
	}

	return nil
}

func (v *ImageVerifier) buildCheckOpts(
	config imageverify.VerifyConfig,
	remoteOpts []ociremote.Option,
	newBundleFormat bool,
	trustedRoot root.TrustedMaterial,
) (*cosign.CheckOpts, error) {
	co := &cosign.CheckOpts{
		RegistryClientOpts: remoteOpts,
		NewBundleFormat:    newBundleFormat,
		ClaimVerifier:      cosign.SimpleClaimVerifier,
	}

	if config.PublicKey != "" {
		verifier, err := signature.LoadPublicKeyRaw([]byte(config.PublicKey), crypto.SHA256)
		if err != nil {
			return nil, fmt.Errorf("failed to load public key: %w", err)
		}
		publicKey, err := verifier.PublicKey()
		if err != nil {
			return nil, fmt.Errorf("read image verification public key: %w", err)
		}
		if err := cryptopolicy.ValidatePublicKey(publicKey); err != nil {
			return nil, fmt.Errorf("image verification key policy: %w", err)
		}
		co.SigVerifier = verifier
		co.IgnoreTlog = config.IgnoreTlog
		if !config.IgnoreTlog {
			if trustedRoot == nil {
				return nil, fmt.Errorf("trusted root material is required for transparency log verification")
			}
			co.TrustedMaterial = trustedRoot
		}
	} else {
		if trustedRoot == nil {
			return nil, fmt.Errorf("trusted root material is required for keyless verification")
		}
		co.TrustedMaterial = trustedRoot
		identity := cosign.Identity{}
		if hasStrictKeylessConfig(config) {
			identity.Issuer = config.Issuer
			identity.Subject = config.Subject
		} else {
			identity.IssuerRegExp = config.IssuerRegExp
			identity.SubjectRegExp = config.SubjectRegExp
		}
		co.Identities = []cosign.Identity{identity}
		co.IgnoreTlog = false
	}

	if newBundleFormat {
		co.ClaimVerifier = cosign.IntotoSubjectClaimVerifier
	}

	return co, nil
}

func shouldAttemptBundleFallback(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), noSignaturesFoundErrorFragment)
}

func (v *ImageVerifier) verifyImageSignatureWithBundles(
	ctx context.Context,
	ref name.Reference,
	digestRef string,
	config imageverify.VerifyConfig,
	remoteOpts []ociremote.Option,
	trustedRoot root.TrustedMaterial,
) error {
	bundleCheckOpts, err := v.buildCheckOpts(config, remoteOpts, true, trustedRoot)
	if err != nil {
		return err
	}

	attestations, bundleVerified, err := cosign.VerifyImageAttestations(ctx, ref, bundleCheckOpts)
	if err != nil {
		return err
	}
	if len(attestations) == 0 {
		return fmt.Errorf("no bundle attestations found for image %q", digestRef)
	}

	signatureCount, observedPredicates, err := countBundleSignaturePredicates(attestations)
	if err != nil {
		return fmt.Errorf("failed to inspect bundle attestations: %w", err)
	}
	if signatureCount == 0 {
		return fmt.Errorf(
			"no signature bundle attestations found for image %q (expected predicate %q, observed=%s)",
			digestRef,
			cosignSignaturePredicateTypeV1,
			strings.Join(observedPredicates, ","),
		)
	}

	v.logger.V(1).Info("Image signature verification completed",
		"digest", digestRef,
		"attestations", len(attestations),
		"signatureAttestations", signatureCount,
		"bundleVerified", bundleVerified,
		"rekorVerified", !bundleCheckOpts.IgnoreTlog,
		"format", "bundle")

	return nil
}

type dsseEnvelope struct {
	PayloadType string `json:"payloadType"`
	Payload     string `json:"payload"`
}

type inTotoStatement struct {
	PredicateType string `json:"predicateType"`
}

func countBundleSignaturePredicates(attestations []oci.Signature) (int, []string, error) {
	signatureCount := 0
	observedPredicates := make([]string, 0, len(attestations))
	for _, attestation := range attestations {
		predicateType, err := bundlePredicateType(attestation)
		if err != nil {
			return 0, nil, err
		}
		observedPredicates = append(observedPredicates, predicateType)
		if predicateType == cosignSignaturePredicateTypeV1 {
			signatureCount++
		}
	}

	return signatureCount, observedPredicates, nil
}

func bundlePredicateType(attestation oci.Signature) (string, error) {
	payload, err := attestation.Payload()
	if err != nil {
		return "", fmt.Errorf("failed to read attestation payload: %w", err)
	}

	envelope := dsseEnvelope{}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", fmt.Errorf("failed to parse DSSE envelope: %w", err)
	}
	if strings.TrimSpace(envelope.PayloadType) != dsseInTotoPayloadType {
		return "", fmt.Errorf("unexpected DSSE payload type %q", envelope.PayloadType)
	}
	if strings.TrimSpace(envelope.Payload) == "" {
		return "", fmt.Errorf("DSSE envelope payload is empty")
	}

	statementBytes, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return "", fmt.Errorf("failed to decode DSSE payload: %w", err)
	}

	statement := inTotoStatement{}
	if err := json.Unmarshal(statementBytes, &statement); err != nil {
		return "", fmt.Errorf("failed to parse in-toto statement payload: %w", err)
	}

	predicateType := strings.TrimSpace(statement.PredicateType)
	if predicateType == "" {
		return "", fmt.Errorf("in-toto statement predicateType is empty")
	}

	return predicateType, nil
}
