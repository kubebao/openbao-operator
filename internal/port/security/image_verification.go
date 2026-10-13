package security

import (
	"context"
	"fmt"
	"strings"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/port/imageverify"
	"github.com/go-logr/logr"
	"github.com/google/go-containerregistry/pkg/name"
)

const (
	defaultGitHubOIDCIssuer       = "https://token.actions.githubusercontent.com"
	defaultGitHubOIDCIssuerRegExp = "^https://token\\.actions\\.githubusercontent\\.com$"

	openBaoReleaseSubjectRegExp = "^https://github\\.com/openbao/openbao/\\.github/workflows/release(-images)?\\.yml@refs/tags/v?[0-9A-Za-z][0-9A-Za-z._+-]*$"
	operatorSubjectRegExp       = "^https://github\\.com/kubebao/openbao-operator/\\.github/workflows/(release\\.yml@refs/tags/.+|publish-edge\\.yml@refs/heads/main|publish-nightly\\.yml@refs/heads/main|reusable-build\\.yml@(refs/heads/main|refs/tags/.+))$"
	legacyOperatorSubjectRegExp = "^https://github\\.com/dc-tec/openbao-operator/\\.github/workflows/(release\\.yml@refs/tags/.+|publish-edge\\.yml@refs/heads/main|publish-nightly\\.yml@refs/heads/main|reusable-build\\.yml@(refs/heads/main|refs/tags/.+))$"

	operatorInitOfficialRepository    = "ghcr.io/kubebao/openbao-init"
	operatorBackupOfficialRepository  = "ghcr.io/kubebao/openbao-backup"
	operatorUpgradeOfficialRepository = "ghcr.io/kubebao/openbao-upgrade"
)

var openBaoOfficialRepositories = map[string]struct{}{
	"openbao/openbao":         {},
	"ghcr.io/openbao/openbao": {},
	"quay.io/openbao/openbao": {},
}

// VerifyImageForCluster verifies an image reference using the cluster's ImageVerification configuration.
// It returns an image digest reference (e.g., "repo@sha256:...") when verification is enabled and succeeds.
// When image verification is disabled, it returns an empty digest and a nil error.
func VerifyImageForCluster(ctx context.Context, logger logr.Logger, verifier imageverify.Verifier, cluster *openbaov1alpha1.OpenBaoCluster, imageRef string) (string, error) {
	if cluster == nil {
		return "", fmt.Errorf("cluster is required")
	}
	verificationConfig, enabled := effectiveMainImageVerificationConfig(cluster)
	if !enabled {
		return "", nil
	}
	if imageRef == "" {
		return "", fmt.Errorf("image reference is required")
	}
	if verifier == nil {
		return "", fmt.Errorf("image verifier is required")
	}

	config := imageverify.VerifyConfig{
		PublicKey:        strings.TrimSpace(verificationConfig.PublicKey),
		Issuer:           strings.TrimSpace(verificationConfig.Issuer),
		Subject:          strings.TrimSpace(verificationConfig.Subject),
		IssuerRegExp:     strings.TrimSpace(verificationConfig.IssuerRegExp),
		SubjectRegExp:    strings.TrimSpace(verificationConfig.SubjectRegExp),
		IgnoreTlog:       verificationConfig.IgnoreTlog,
		ImagePullSecrets: verificationConfig.ImagePullSecrets,
		Namespace:        cluster.Namespace,
	}

	applyOfficialKeylessDefaults(&config, imageRef, false)

	if !hasKeylessConfig(config) && config.PublicKey == "" {
		return "", fmt.Errorf("image verification is enabled but neither public key nor keyless configuration (issuer/subject or issuerRegExp/subjectRegExp) is provided")
	}

	digest, err := verifier.Verify(ctx, imageRef, config)
	if err != nil {
		return "", err
	}

	return digest, nil
}

// VerifyOperatorImageForCluster verifies an operator-managed helper image (init container,
// backup/upgrade/restore executors) using the cluster's OperatorImageVerification config.
// Unlike VerifyImageForCluster, this function does NOT fall back to ImageVerification.
// If OperatorImageVerification is omitted, helper verification is disabled in Development
// and implicitly enabled in Hardened.
func VerifyOperatorImageForCluster(ctx context.Context, logger logr.Logger, verifier imageverify.Verifier, cluster *openbaov1alpha1.OpenBaoCluster, imageRef string) (string, error) {
	if cluster == nil {
		return "", fmt.Errorf("cluster is required")
	}
	if imageRef == "" {
		return "", fmt.Errorf("image reference is required")
	}

	verificationConfig, enabled := effectiveOperatorImageVerificationConfig(cluster)
	if !enabled {
		return "", nil
	}

	if verifier == nil {
		return "", fmt.Errorf("image verifier is required")
	}

	config := imageverify.VerifyConfig{
		PublicKey:        strings.TrimSpace(verificationConfig.PublicKey),
		Issuer:           strings.TrimSpace(verificationConfig.Issuer),
		Subject:          strings.TrimSpace(verificationConfig.Subject),
		IssuerRegExp:     strings.TrimSpace(verificationConfig.IssuerRegExp),
		SubjectRegExp:    strings.TrimSpace(verificationConfig.SubjectRegExp),
		IgnoreTlog:       verificationConfig.IgnoreTlog,
		ImagePullSecrets: verificationConfig.ImagePullSecrets,
		Namespace:        cluster.Namespace,
	}

	applyOfficialKeylessDefaults(&config, imageRef, true)

	if !hasKeylessConfig(config) && config.PublicKey == "" {
		return "", fmt.Errorf("operator image verification is enabled but neither public key nor keyless configuration (issuer/subject or issuerRegExp/subjectRegExp) is provided")
	}

	digest, err := verifier.Verify(ctx, imageRef, config)
	if err != nil {
		return "", err
	}

	return digest, nil
}

// IsMainImageVerificationEnabled reports whether main image verification should
// run for the cluster. Hardened profile defaults to verification enabled when
// the block is omitted, while explicit enabled=false keeps verification off.
func IsMainImageVerificationEnabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	_, enabled := effectiveMainImageVerificationConfig(cluster)
	return enabled
}

// IsOperatorImageVerificationEnabled reports whether operator helper image
// verification should run for the cluster. Hardened profile defaults to
// verification enabled when the block is omitted, while explicit enabled=false
// keeps verification off.
func IsOperatorImageVerificationEnabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	_, enabled := effectiveOperatorImageVerificationConfig(cluster)
	return enabled
}

func hasStrictKeylessConfig(config imageverify.VerifyConfig) bool {
	return strings.TrimSpace(config.Issuer) != "" && strings.TrimSpace(config.Subject) != ""
}

func hasRegexKeylessConfig(config imageverify.VerifyConfig) bool {
	return strings.TrimSpace(config.IssuerRegExp) != "" && strings.TrimSpace(config.SubjectRegExp) != ""
}

func hasKeylessConfig(config imageverify.VerifyConfig) bool {
	return hasStrictKeylessConfig(config) || hasRegexKeylessConfig(config)
}

func applyOfficialKeylessDefaults(config *imageverify.VerifyConfig, imageRef string, isOperatorImage bool) {
	if config == nil {
		return
	}
	if strings.TrimSpace(config.PublicKey) != "" {
		return
	}
	if hasKeylessConfig(*config) {
		return
	}

	repository, ok := imageRepository(imageRef)
	if !ok {
		return
	}

	subjectRegExp := defaultSubjectRegExpForImage(repository, isOperatorImage)
	if subjectRegExp == "" {
		return
	}

	config.IssuerRegExp = defaultGitHubOIDCIssuerRegExp
	config.SubjectRegExp = subjectRegExp
}

func imageRepository(imageRef string) (string, bool) {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return "", false
	}

	repository := normalizeRepository(ref.Context().Name())
	if repository == "" {
		return "", false
	}

	return repository, true
}

func normalizeRepository(repository string) string {
	repository = strings.TrimSpace(repository)
	repository = strings.TrimPrefix(repository, "index.docker.io/")
	repository = strings.TrimPrefix(repository, "docker.io/")
	return repository
}

func defaultSubjectRegExpForImage(repository string, isOperatorImage bool) string {
	if !isOperatorImage {
		if _, ok := openBaoOfficialRepositories[repository]; !ok {
			return ""
		}
		return openBaoReleaseSubjectRegExp
	}

	switch repository {
	case operatorInitOfficialRepository, operatorBackupOfficialRepository, operatorUpgradeOfficialRepository:
		return operatorSubjectRegExp
	case "ghcr.io/dc-tec/openbao-init", "ghcr.io/dc-tec/openbao-backup", "ghcr.io/dc-tec/openbao-upgrade":
		// Historical artifacts retain their original repository and signing identity.
		return legacyOperatorSubjectRegExp
	default:
		return ""
	}
}

func effectiveMainImageVerificationConfig(cluster *openbaov1alpha1.OpenBaoCluster) (*openbaov1alpha1.ImageVerificationConfig, bool) {
	if cluster == nil {
		return nil, false
	}

	if cluster.Spec.ImageVerification != nil {
		if !cluster.Spec.ImageVerification.Enabled {
			return nil, false
		}
		return cluster.Spec.ImageVerification, true
	}

	if cluster.Spec.Profile == openbaov1alpha1.ProfileHardened {
		return &openbaov1alpha1.ImageVerificationConfig{Enabled: true}, true
	}

	return nil, false
}

func effectiveOperatorImageVerificationConfig(cluster *openbaov1alpha1.OpenBaoCluster) (*openbaov1alpha1.ImageVerificationConfig, bool) {
	if cluster == nil {
		return nil, false
	}

	if cluster.Spec.OperatorImageVerification != nil {
		if !cluster.Spec.OperatorImageVerification.Enabled {
			return nil, false
		}
		return cluster.Spec.OperatorImageVerification, true
	}

	if cluster.Spec.Profile == openbaov1alpha1.ProfileHardened {
		return &openbaov1alpha1.ImageVerificationConfig{Enabled: true}, true
	}

	return nil, false
}
