// Package storage provides cloud-agnostic object storage interfaces and implementations
// for backup operations in the OpenBao Operator.
//
// This file contains Azure Blob Storage implementation using Go CDK.
package storage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"gocloud.dev/blob/azureblob"

	"github.com/kubebao/openbao-operator/internal/port/blobstore"
)

// AzureClientConfig holds configuration for Azure Blob Storage.
type AzureClientConfig struct {
	// StorageAccount is the Azure storage account name.
	StorageAccount string
	// Container is the blob container name.
	Container string
	// Endpoint is an optional complete Blob service URL.
	// Azurite URLs must include the storage account path, for example http://azurite:10000/devstoreaccount1.
	Endpoint string
	// AccountKey is the storage account access key (optional if using managed identity).
	AccountKey string
	// ConnectionString is an optional full connection string (takes precedence over AccountKey).
	ConnectionString string
	// UseManagedIdentity enables Azure Managed Identity / DefaultAzureCredential.
	UseManagedIdentity bool
	// ManagedIdentityClientID optionally selects a user-assigned managed identity.
	ManagedIdentityClientID string
	// InsecureSkipVerify allows skipping TLS verification (useful for emulators).
	InsecureSkipVerify bool
	// CACert is an optional PEM-encoded CA certificate for custom TLS verification.
	CACert []byte
	// EnsureExists optionally creates the container if it does not exist.
	EnsureExists bool
	// ValidateEndpointRequests rejects request-time redirects or DNS results to local or metadata-adjacent destinations.
	ValidateEndpointRequests bool
}

// OpenAzureContainer opens an Azure blob container using Go CDK.
// It returns a BlobStore interface that provides standardized blob operations.
func OpenAzureContainer(ctx context.Context, cfg AzureClientConfig) (blobstore.BlobStore, error) {
	if cfg.Container == "" {
		return nil, fmt.Errorf("container is required")
	}
	if cfg.StorageAccount == "" && cfg.ConnectionString == "" {
		if cfg.UseManagedIdentity {
			return nil, fmt.Errorf("storage account is required when using managed identity")
		}
		return nil, fmt.Errorf("storage account or connection string is required")
	}

	httpClient, err := buildAzureHTTPClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build Azure HTTP client: %w", err)
	}
	clientOpts := &azblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: httpClient,
		},
	}

	var containerClient *container.Client

	if cfg.ConnectionString != "" {
		// Use connection string (includes account key)
		serviceClient, err := azblob.NewClientFromConnectionString(cfg.ConnectionString, clientOpts)
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure client from connection string: %w", err)
		}
		containerClient = serviceClient.ServiceClient().NewContainerClient(cfg.Container)
	} else if cfg.AccountKey != "" {
		// Use shared key credential
		var serviceURL string
		if cfg.Endpoint != "" {
			serviceURL = cfg.Endpoint
		} else {
			serviceURL = fmt.Sprintf("https://%s.blob.core.windows.net", cfg.StorageAccount)
		}

		cred, err := azblob.NewSharedKeyCredential(cfg.StorageAccount, cfg.AccountKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure shared key credential: %w", err)
		}

		serviceClient, err := azblob.NewClientWithSharedKeyCredential(serviceURL, cred, clientOpts)
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure client: %w", err)
		}
		containerClient = serviceClient.ServiceClient().NewContainerClient(cfg.Container)
	} else if cfg.UseManagedIdentity {
		// Managed identity / DefaultAzureCredential
		if cfg.StorageAccount == "" {
			return nil, fmt.Errorf("storage account is required when using managed identity")
		}
		var serviceURL string
		if cfg.Endpoint != "" {
			serviceURL = cfg.Endpoint
		} else {
			serviceURL = fmt.Sprintf("https://%s.blob.core.windows.net", cfg.StorageAccount)
		}

		var cred azcore.TokenCredential
		credentialHTTPClient, err := buildAzureHTTPClient(AzureClientConfig{
			InsecureSkipVerify: cfg.InsecureSkipVerify,
			CACert:             cfg.CACert,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to build Azure credential HTTP client: %w", err)
		}
		credentialClientOptions := azcore.ClientOptions{
			Transport: credentialHTTPClient,
		}
		if cfg.ManagedIdentityClientID != "" {
			cred, err = azidentity.NewManagedIdentityCredential(&azidentity.ManagedIdentityCredentialOptions{
				ID:            azidentity.ClientID(cfg.ManagedIdentityClientID),
				ClientOptions: credentialClientOptions,
			})
		} else {
			cred, err = azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{
				ClientOptions: credentialClientOptions,
			})
		}
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure managed identity credential: %w", err)
		}

		serviceClient, err := azblob.NewClient(serviceURL, cred, clientOpts)
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure client: %w", err)
		}
		containerClient = serviceClient.ServiceClient().NewContainerClient(cfg.Container)
	} else {
		// Use default Azure credential (managed identity, environment, etc.)
		return nil, fmt.Errorf("azure credentials not provided")
	}

	// Open container using Go CDK azureblob driver
	bucket, err := azureblob.OpenBucket(ctx, containerClient, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open Azure container: %w", err)
	}

	wrapped := NewBucket(bucket)

	if cfg.EnsureExists {
		if err := ensureAzureContainer(ctx, containerClient); err != nil {
			_ = wrapped.Close()
			return nil, err
		}
	}

	return wrapped, nil
}

func buildAzureHTTPClient(cfg AzureClientConfig) (*http.Client, error) {
	transport := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   false,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
	}

	certPool, err := x509.SystemCertPool()
	if err != nil || certPool == nil {
		certPool = x509.NewCertPool()
	}

	if len(cfg.CACert) > 0 {
		if !certPool.AppendCertsFromPEM(cfg.CACert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
	}

	transport.TLSClientConfig = &tls.Config{
		RootCAs:            certPool,
		InsecureSkipVerify: cfg.InsecureSkipVerify, // #nosec G402 -- Intentional for emulator support
		MinVersion:         tls.VersionTLS12,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   DefaultUploadTimeout,
	}
	applyStorageEndpointRequestGuard(client, transport, cfg.ValidateEndpointRequests)
	return client, nil
}

func ensureAzureContainer(ctx context.Context, c *container.Client) error {
	_, err := c.Create(ctx, nil)
	if err == nil {
		return nil
	}

	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		if respErr.StatusCode == http.StatusConflict {
			return nil
		}
	}
	return err
}
