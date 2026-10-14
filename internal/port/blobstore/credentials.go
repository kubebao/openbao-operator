package blobstore

import (
	"strings"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// EffectiveS3Region resolves the credential override before the target and default.
func EffectiveS3Region(targetRegion, credentialRegion string) string {
	if region := strings.TrimSpace(credentialRegion); region != "" {
		return region
	}
	if region := strings.TrimSpace(targetRegion); region != "" {
		return region
	}
	return constants.DefaultS3Region
}

// Secret key names expected in storage credentials Secrets.
const (
	SecretKeyAccessKeyID     = "accessKeyId"
	SecretKeySecretAccessKey = "secretAccessKey"
	SecretKeySessionToken    = "sessionToken"
	SecretKeyRegion          = "region"
	SecretKeyCACert          = "caCert"
)

// Credentials is the shared contract for object-storage credentials.
// It is intentionally placed in a port package so helper adapters can
// decode credentials without importing concrete storage implementations.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Region          string
	CACert          []byte
}
