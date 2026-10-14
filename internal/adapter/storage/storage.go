// Package storage provides cloud-agnostic object storage interfaces and implementations
// for backup operations in the OpenBao Operator.
//
// # Supported Providers
//
// Currently implemented:
//   - S3 / S3-compatible (AWS, MinIO, etc.) - see s3.go
//   - Google Cloud Storage (GCS) - see gcs.go
//   - Azure Blob Storage - see azure.go
//
// # Usage
//
// Use OpenBlobStore() for provider-agnostic storage access, or provider-specific
// functions like OpenS3Bucket() for fine-grained control.
package storage

import (
	"context"
	"fmt"

	"github.com/kubebao/openbao-operator/internal/port/blobstore"
)

// ProviderType identifies the storage provider.
type ProviderType string

const (
	// ProviderS3 is Amazon S3 or S3-compatible storage (MinIO, etc.).
	ProviderS3 ProviderType = "s3"
	// ProviderGCS is Google Cloud Storage.
	ProviderGCS ProviderType = "gcs"
	// ProviderAzure is Azure Blob Storage.
	ProviderAzure ProviderType = "azure"
)

// Config holds provider-agnostic storage configuration.
// Use provider-specific fields based on the Provider type.
type Config struct {
	// Provider identifies which storage backend to use.
	Provider ProviderType

	// Bucket is the bucket/container name. Required for all providers.
	Bucket string

	// EnsureExists optionally validates (and for some providers, creates) the bucket/container.
	EnsureExists bool

	// Endpoint is the storage service endpoint URL (optional for most providers).
	// For S3: Custom endpoint for MinIO/S3-compatible stores.
	// For GCS: Custom endpoint for fake-gcs-server testing.
	// For Azure: Complete Blob service URL, including the account path when using Azurite.
	Endpoint string

	// Region is required for S3, ignored for others.
	Region string

	// Credentials holds authentication credentials.
	Credentials *blobstore.Credentials

	// S3 contains S3-specific configuration.
	S3 *S3Options

	// GCS contains GCS-specific configuration.
	GCS *GCSOptions

	// Azure contains Azure-specific configuration.
	Azure *AzureOptions

	// ValidateEndpointRequests rejects storage HTTP requests and redirects to
	// local or metadata-adjacent destinations. Backup and restore paths enable
	// this after admission/preflight endpoint validation.
	ValidateEndpointRequests bool
}

// S3Options holds S3-specific options.
type S3Options struct {
	// UsePathStyle forces path-style addressing.
	UsePathStyle bool
	// InsecureSkipVerify allows skipping TLS verification.
	InsecureSkipVerify bool
	// EnsureExists checks if the bucket exists and tries to create it if not.
	EnsureExists bool
}

// GCSOptions holds GCS-specific configuration options.
type GCSOptions struct {
	// Project is the GCP project ID (optional if using ADC).
	Project string
	// CredentialsJSON is the service account key JSON (optional if using ADC).
	CredentialsJSON []byte
	// UseEmulator disables authentication and expects a custom Endpoint.
	UseEmulator bool
	// InsecureSkipVerify allows skipping TLS verification (useful for emulators).
	InsecureSkipVerify bool
	// CACert is an optional PEM-encoded CA certificate for custom TLS verification.
	CACert []byte
}

// AzureOptions holds Azure-specific configuration options.
type AzureOptions struct {
	// StorageAccount is the Azure storage account name.
	StorageAccount string
	// AccountKey is the storage account access key.
	AccountKey string
	// ConnectionString is an optional full connection string.
	ConnectionString string
	// UseManagedIdentity enables Azure Managed Identity / DefaultAzureCredential.
	UseManagedIdentity bool
	// ManagedIdentityClientID optionally selects a user-assigned managed identity.
	ManagedIdentityClientID string
	// InsecureSkipVerify allows skipping TLS verification (useful for emulators).
	InsecureSkipVerify bool
	// CACert is an optional PEM-encoded CA certificate for custom TLS verification.
	CACert []byte
}

// OpenBlobStore opens a storage backend based on the provider configuration.
// This is the preferred entry point for provider-agnostic storage access.
func OpenBlobStore(ctx context.Context, cfg Config) (blobstore.BlobStore, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("bucket is required")
	}

	switch cfg.Provider {
	case ProviderS3, "": // Default to S3
		return openS3(ctx, cfg)
	case ProviderGCS:
		return openGCS(ctx, cfg)
	case ProviderAzure:
		return openAzure(ctx, cfg)
	default:
		return nil, fmt.Errorf("unknown storage provider: %q", cfg.Provider)
	}
}

// openS3 opens an S3-compatible bucket using the unified Config.
func openS3(ctx context.Context, cfg Config) (blobstore.BlobStore, error) {
	var accessKeyID, secretAccessKey, sessionToken string
	var caCert []byte
	var usePathStyle, insecureSkipVerify, ensureExists bool

	if cfg.Credentials != nil {
		accessKeyID = cfg.Credentials.AccessKeyID
		secretAccessKey = cfg.Credentials.SecretAccessKey
		sessionToken = cfg.Credentials.SessionToken
		caCert = cfg.Credentials.CACert
	}

	if cfg.S3 != nil {
		usePathStyle = cfg.S3.UsePathStyle
		insecureSkipVerify = cfg.S3.InsecureSkipVerify
		ensureExists = cfg.S3.EnsureExists
	}

	return OpenS3Bucket(ctx, S3ClientConfig{
		Endpoint:                 cfg.Endpoint,
		Bucket:                   cfg.Bucket,
		Region:                   cfg.Region,
		AccessKeyID:              accessKeyID,
		SecretAccessKey:          secretAccessKey,
		SessionToken:             sessionToken,
		CACert:                   caCert,
		UsePathStyle:             usePathStyle,
		InsecureSkipVerify:       insecureSkipVerify,
		EnsureExists:             ensureExists,
		ValidateEndpointRequests: cfg.ValidateEndpointRequests,
	})
}

// openGCS opens a GCS bucket using the unified Config.
func openGCS(ctx context.Context, cfg Config) (blobstore.BlobStore, error) {
	gcsCfg := GCSClientConfig{
		Bucket:                   cfg.Bucket,
		Endpoint:                 cfg.Endpoint,
		EnsureExists:             cfg.EnsureExists,
		ValidateEndpointRequests: cfg.ValidateEndpointRequests,
	}

	if cfg.GCS != nil {
		gcsCfg.Project = cfg.GCS.Project
		gcsCfg.CredentialsJSON = cfg.GCS.CredentialsJSON
		gcsCfg.UseEmulator = cfg.GCS.UseEmulator
		gcsCfg.InsecureSkipVerify = cfg.GCS.InsecureSkipVerify
		gcsCfg.CACert = cfg.GCS.CACert
	}

	return OpenGCSBucket(ctx, gcsCfg)
}

// openAzure opens an Azure blob container using the unified Config.
func openAzure(ctx context.Context, cfg Config) (blobstore.BlobStore, error) {
	azureCfg := AzureClientConfig{
		Container:                cfg.Bucket,
		Endpoint:                 cfg.Endpoint,
		EnsureExists:             cfg.EnsureExists,
		ValidateEndpointRequests: cfg.ValidateEndpointRequests,
	}

	if cfg.Azure != nil {
		azureCfg.StorageAccount = cfg.Azure.StorageAccount
		azureCfg.AccountKey = cfg.Azure.AccountKey
		azureCfg.ConnectionString = cfg.Azure.ConnectionString
		azureCfg.UseManagedIdentity = cfg.Azure.UseManagedIdentity
		azureCfg.ManagedIdentityClientID = cfg.Azure.ManagedIdentityClientID
		azureCfg.InsecureSkipVerify = cfg.Azure.InsecureSkipVerify
		azureCfg.CACert = cfg.Azure.CACert
	}

	return OpenAzureContainer(ctx, azureCfg)
}
