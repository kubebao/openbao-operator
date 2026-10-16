// Package storage provides cloud-agnostic object storage interfaces and implementations
// for backup operations in the OpenBao Operator.
//
// This file contains AWS S3 / S3-compatible storage implementation using Go CDK.
// For Azure Blob Storage, see azure.go (when available).
// For Google Cloud Storage, see gcs.go (when available).
package storage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"gocloud.dev/blob"
	"gocloud.dev/blob/s3blob"
	"gocloud.dev/gcerrors"

	"github.com/kubebao/openbao-operator/internal/platform/cryptopolicy"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/port/blobstore"
)

const (
	// DefaultUploadTimeout is the default timeout for upload operations.
	DefaultUploadTimeout = 30 * time.Minute
)

var (
	s3EnsureExistsMaxAttempts = 12
	s3EnsureExistsRetryDelay  = 2 * time.Second
)

// Bucket wraps a Go CDK blob.Bucket with a simplified interface.
// It implements the common operations needed for backup/restore functionality.
type Bucket struct {
	bucket *blob.Bucket
}

// NewBucket creates a Bucket wrapper around a Go CDK blob.Bucket.
func NewBucket(bucket *blob.Bucket) *Bucket {
	return &Bucket{bucket: bucket}
}

// Upload stores the contents of body as an object with the given key.
// For large objects, Go CDK automatically handles multipart uploads.
// If reading body fails, the write is aborted and no object is created.
func (b *Bucket) Upload(ctx context.Context, key string, body io.Reader) error {
	// Go CDK commits the bytes written so far when Close is called. Cancelling the
	// writer context before Close is the only way to abort a partial upload.
	writeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	w, err := b.bucket.NewWriter(writeCtx, key, nil)
	if err != nil {
		return err
	}
	if _, copyErr := io.Copy(w, body); copyErr != nil {
		cancel()
		_ = w.Close()
		return copyErr
	}
	return w.Close()
}

// Delete removes the object with the given key.
// Returns nil if the object does not exist.
func (b *Bucket) Delete(ctx context.Context, key string) error {
	err := b.bucket.Delete(ctx, key)
	if err != nil && gcerrors.Code(err) == gcerrors.NotFound {
		return nil
	}
	return err
}

// DeleteBatch removes multiple objects at once.
// This is a convenience method that calls Delete for each key.
func (b *Bucket) DeleteBatch(ctx context.Context, keys []string) error {
	for _, key := range keys {
		if err := b.Delete(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// List returns metadata for all objects matching the given prefix.
// Results are sorted by key name ascending.
func (b *Bucket) List(ctx context.Context, prefix string) ([]blobstore.ObjectInfo, error) {
	var result []blobstore.ObjectInfo
	iter := b.bucket.List(&blob.ListOptions{Prefix: prefix})
	for {
		obj, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		result = append(result, blobstore.ObjectInfo{
			Key:          obj.Key,
			Size:         obj.Size,
			LastModified: obj.ModTime,
		})
	}

	// Sort by key ascending
	sort.Slice(result, func(i, j int) bool {
		return result[i].Key < result[j].Key
	})

	return result, nil
}

// Head retrieves metadata for a single object without downloading its contents.
// Returns nil and no error if the object does not exist.
func (b *Bucket) Head(ctx context.Context, key string) (*blobstore.ObjectInfo, error) {
	attrs, err := b.bucket.Attributes(ctx, key)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			return nil, nil
		}
		return nil, err
	}
	return &blobstore.ObjectInfo{
		Key:          key,
		Size:         attrs.Size,
		LastModified: attrs.ModTime,
		ETag:         attrs.ETag,
	}, nil
}

// Download retrieves an object and returns a reader for its contents.
// The caller is responsible for closing the returned ReadCloser.
// Returns an error if the object does not exist.
func (b *Bucket) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	return b.bucket.NewReader(ctx, key, nil)
}

// Close closes the underlying bucket and releases any resources.
func (b *Bucket) Close() error {
	return b.bucket.Close()
}

// ============================================================================
// AWS S3 / S3-Compatible Implementation
// ============================================================================

// S3ClientConfig holds configuration for creating a new S3-compatible storage client.
type S3ClientConfig struct {
	// Endpoint is the S3-compatible endpoint URL (e.g., "https://s3.amazonaws.com" or "https://minio.example.com").
	Endpoint string
	// Bucket is the target bucket name.
	Bucket string
	// Region is the AWS region (e.g., "us-east-1"). Required for AWS S3.
	Region string
	// AccessKeyID is the access key for authentication. If empty, the default credential chain is used.
	AccessKeyID string
	// SecretAccessKey is the secret key for authentication.
	SecretAccessKey string
	// SessionToken is an optional session token for temporary credentials.
	SessionToken string
	// CACert is an optional PEM-encoded CA certificate for custom TLS verification.
	CACert []byte
	// UsePathStyle forces path-style addressing (required for MinIO and some S3-compatible stores).
	UsePathStyle bool
	// InsecureSkipVerify allows skipping TLS verification (useful for MinIO/LocalStack with self-signed certs).
	InsecureSkipVerify bool
	// EnsureExists checks if the bucket exists and tries to create it if not.
	EnsureExists bool
	// ValidateEndpointRequests rejects request-time redirects or DNS results to local or metadata-adjacent destinations.
	ValidateEndpointRequests bool
}

// OpenS3Bucket opens an S3-compatible bucket using Go CDK.
// It returns a BlobStore interface that provides standardized blob operations.
func OpenS3Bucket(ctx context.Context, cfg S3ClientConfig) (blobstore.BlobStore, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("bucket is required")
	}

	// Build AWS config
	awsCfg, err := buildAWSConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Create S3 client with custom endpoint configuration
	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.UsePathStyle
	})

	// Open bucket using Go CDK s3blob driver
	bucket, err := s3blob.OpenBucketV2(ctx, s3Client, cfg.Bucket, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open S3 bucket: %w", err)
	}

	wrapped := NewBucket(bucket)

	if cfg.EnsureExists {
		if err := ensureS3Bucket(ctx, s3Client, cfg.Bucket, cfg.Region); err != nil {
			_ = wrapped.Close()
			return nil, fmt.Errorf("failed to ensure bucket exists: %w", err)
		}
	}

	return wrapped, nil
}

func ensureS3Bucket(ctx context.Context, client *s3.Client, bucketName, region string) error {
	createInput := &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	}

	if region != "us-east-1" && region != "" {
		createInput.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		}
	}

	var lastErr error
	for attempt := 1; attempt <= s3EnsureExistsMaxAttempts; attempt++ {
		_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
			Bucket: aws.String(bucketName),
		})
		if err == nil {
			return nil
		}

		_, err = client.CreateBucket(ctx, createInput)
		if err == nil {
			return nil
		}
		if s3BucketAlreadyExists(err) {
			return nil
		}
		lastErr = err
		if !shouldRetryS3EnsureExists(err) {
			return err
		}

		if attempt == s3EnsureExistsMaxAttempts {
			break
		}

		timer := time.NewTimer(s3EnsureExistsRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	return lastErr
}

func shouldRetryS3EnsureExists(err error) bool {
	if err == nil {
		return false
	}

	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return false
	}

	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return false
	}

	var certInvalidErr x509.CertificateInvalidError
	if errors.As(err, &certInvalidErr) {
		return false
	}

	var sendErr *smithyhttp.RequestSendError
	if errors.As(err, &sendErr) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	var responseErr *smithyhttp.ResponseError
	if errors.As(err, &responseErr) {
		statusCode := responseErr.HTTPStatusCode()
		return statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorFault() == smithy.FaultServer
	}

	return false
}

func s3BucketAlreadyExists(err error) bool {
	if err == nil {
		return false
	}

	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}

	switch apiErr.ErrorCode() {
	case "BucketAlreadyExists", "BucketAlreadyOwnedByYou":
		return true
	default:
		return false
	}
}

// buildAWSConfig constructs AWS SDK config with credentials and custom TLS settings.
func buildAWSConfig(ctx context.Context, cfg S3ClientConfig) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error

	// Set region
	if cfg.Region == "" {
		return aws.Config{}, fmt.Errorf("region is required for S3 client")
	}
	opts = append(opts, config.WithRegion(cfg.Region))

	// Configure credentials if provided
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		staticCreds := credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID,
			cfg.SecretAccessKey,
			cfg.SessionToken,
		)
		opts = append(opts, config.WithCredentialsProvider(staticCreds))
	}

	// Configure an HTTP client for config loading and credential discovery. This
	// path must not apply the storage endpoint guard because ambient credential
	// providers may legitimately use their own metadata or identity endpoints.
	httpClient, err := buildHTTPClient(cfg.CACert, cfg.InsecureSkipVerify, false)
	if err != nil {
		if operatorerrors.IsTransientConnection(err) {
			return aws.Config{}, operatorerrors.WrapTransientConnection(fmt.Errorf("failed to create HTTP client: %w", err))
		}
		return aws.Config{}, fmt.Errorf("failed to create HTTP client: %w", err)
	}
	opts = append(opts, config.WithHTTPClient(httpClient))

	// Load AWS config
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		if operatorerrors.IsTransientConnection(err) {
			return aws.Config{}, operatorerrors.WrapTransientConnection(fmt.Errorf("failed to load AWS config: %w", err))
		}
		return aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}

	storageHTTPClient, err := buildHTTPClient(cfg.CACert, cfg.InsecureSkipVerify, cfg.ValidateEndpointRequests)
	if err != nil {
		if operatorerrors.IsTransientConnection(err) {
			return aws.Config{}, operatorerrors.WrapTransientConnection(fmt.Errorf("failed to create storage HTTP client: %w", err))
		}
		return aws.Config{}, fmt.Errorf("failed to create storage HTTP client: %w", err)
	}
	awsCfg.HTTPClient = storageHTTPClient

	return awsCfg, nil
}

// buildHTTPClient creates an HTTP client with optional custom CA certificate.
func buildHTTPClient(caCert []byte, insecureSkipVerify, validateEndpointRequests bool) (*http.Client, error) {
	transport := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   false,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
	}

	// Start from the system cert pool when available so that custom CAs are
	// additive instead of replacing the system roots.
	certPool, err := x509.SystemCertPool()
	if err != nil || certPool == nil {
		certPool = x509.NewCertPool()
	}

	if len(caCert) > 0 {
		if !certPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		if err := cryptopolicy.ValidatePEMCertificates(caCert); err != nil {
			return nil, fmt.Errorf("CA key policy: %w", err)
		}
	}

	transport.TLSClientConfig = &tls.Config{
		RootCAs:            certPool,
		InsecureSkipVerify: insecureSkipVerify, // #nosec G402 -- Intentional for emulator support
		MinVersion:         tls.VersionTLS12,
	}

	cryptopolicy.ConfigureTLS(transport.TLSClientConfig)
	client := &http.Client{
		Transport: transport,
		Timeout:   DefaultUploadTimeout,
	}
	applyStorageEndpointRequestGuard(client, transport, validateEndpointRequests)
	return client, nil
}
