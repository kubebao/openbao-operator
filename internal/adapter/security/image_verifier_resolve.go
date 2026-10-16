package security

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	ggcrremote "github.com/google/go-containerregistry/pkg/v1/remote"

	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/port/imageverify"
)

// resolveDigestWithCache resolves an image reference (tag or digest) to a digest reference.
func (v *ImageVerifier) resolveDigestWithCache(ctx context.Context, imageRef string, config imageverify.VerifyConfig) (string, error) {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return "", fmt.Errorf("failed to parse image reference: %w", err)
	}
	if d, ok := ref.(name.Digest); ok {
		return d.String(), nil
	}

	if cached, ok := v.tagCache.get(imageRef); ok {
		v.logger.V(1).Info("Tag resolution cache hit", "imageRef", imageRef, "digest", cached)
		return cached, nil
	}

	digest, err := v.resolveDigest(ctx, imageRef, config)
	if err != nil {
		return "", err
	}

	v.tagCache.set(imageRef, digest)
	v.logger.V(1).Info("Tag resolution cached", "imageRef", imageRef, "digest", digest)
	return digest, nil
}

// resolveDigest resolves an image reference (tag or digest) to a digest reference via network.
func (v *ImageVerifier) resolveDigest(ctx context.Context, imageRef string, config imageverify.VerifyConfig) (string, error) {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return "", fmt.Errorf("failed to parse image reference: %w", err)
	}
	if d, ok := ref.(name.Digest); ok {
		return d.String(), nil
	}

	ggcrOpts := []ggcrremote.Option{ggcrremote.WithTransport(imageRegistryTransport)}
	if len(config.ImagePullSecrets) > 0 && v.reader != nil {
		keychain, err := v.buildKeychain(ctx, config.ImagePullSecrets, config.Namespace)
		if err != nil {
			return "", fmt.Errorf("failed to build keychain for image pull secrets: %w", err)
		}
		if keychain != nil {
			ggcrOpts = append(ggcrOpts, ggcrremote.WithAuthFromKeychain(keychain))
		}
	}

	desc, err := ggcrremote.Head(ref, ggcrOpts...)
	if err != nil {
		if operatorerrors.IsTransientConnection(err) {
			return "", operatorerrors.WrapTransientConnection(fmt.Errorf("failed to resolve image digest: %w", err))
		}
		return "", fmt.Errorf("failed to resolve image digest: %w", err)
	}

	digestRef, err := name.NewDigest(fmt.Sprintf("%s@%s", ref.Context().Name(), desc.Digest.String()))
	if err != nil {
		return "", fmt.Errorf("failed to create digest reference: %w", err)
	}

	return digestRef.String(), nil
}

// cacheKey generates a cache key from the complete image digest and verification policy.
func (v *ImageVerifier) cacheKey(
	digest string,
	config imageverify.VerifyConfig,
	trustedRootIdentity string,
) (string, error) {
	identity := struct {
		Digest              string
		Policy              imageverify.VerifyConfig
		TrustedRootIdentity string
	}{
		Digest:              digest,
		Policy:              config,
		TrustedRootIdentity: trustedRootIdentity,
	}
	encodedIdentity, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("marshal verification cache identity: %w", err)
	}

	identityHash := sha256.Sum256(encodedIdentity)
	return fmt.Sprintf("sha256:%x", identityHash), nil
}
