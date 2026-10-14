package backup

import (
	"fmt"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/blobstore"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

// ExecutorConfig holds the backup executor configuration.
type ExecutorConfig struct {
	// Cluster information
	ClusterNamespace string
	ClusterName      string
	StatefulSetName  string // StatefulSet name for pod discovery (may include revision for Blue/Green)
	ClusterReplicas  int32

	// Storage provider configuration
	BackupProvider       string // s3, gcs, azure
	BackupEndpoint       string
	BackupBucket         string
	BackupPathPrefix     string
	BackupFilenamePrefix string // Added to support pre-upgrade prefixes
	BackupKey            string // Deterministic key provided by controller

	// S3-specific configuration
	BackupUsePathStyle bool
	BackupRegion       string
	// StorageRegion is the mounted S3 credential region override, before defaults.
	StorageRegion string

	// GCS-specific configuration
	GCSProject string

	// GCSUseEmulator forces unauthenticated emulator mode (no OAuth, custom endpoint).
	GCSUseEmulator bool

	// Azure-specific configuration
	AzureStorageAccount string
	AzureContainer      string

	// Authentication
	AuthMethod      string
	JWTAuthRole     string
	JWTAuthStrategy string
	OpenBaoToken    string
	JWTToken        string

	// TLS
	TLSCACert          []byte
	TLSServerName      string
	InsecureSkipVerify bool

	// Storage credentials (provider-agnostic)
	StorageCredentials *blobstore.Credentials
	// StorageCACert adds storage endpoint trust independently of authentication.
	StorageCACert []byte

	// GCS-specific credentials
	GCSCredentialsJSON []byte

	// Azure-specific credentials
	AzureAccountKey       string
	AzureConnectionString string

	// Upload configuration
	PartSize    int64
	Concurrency int32

	// Smart Client Limits
	RateLimitQPS                   float64
	RateLimitBurst                 int
	CircuitBreakerFailureThreshold int
	CircuitBreakerOpenDuration     string // Duration string format (e.g. "30s")
}

// Validate validates the configuration and returns an error if invalid.
func (c *ExecutorConfig) Validate() error {
	if c.ClusterNamespace == "" {
		return fmt.Errorf("cluster namespace is required")
	}
	if c.ClusterName == "" {
		return fmt.Errorf("cluster name is required")
	}
	if c.ClusterReplicas <= 0 {
		return fmt.Errorf("cluster replicas must be greater than 0")
	}
	if err := validateStorageConfiguration(c); err != nil {
		return err
	}

	switch c.AuthMethod {
	case constants.BackupAuthMethodJWT:
		if c.JWTAuthRole == "" {
			return fmt.Errorf("JWT auth role is required when using JWT authentication")
		}
		if c.JWTToken == "" {
			return fmt.Errorf("JWT token is required when using JWT authentication")
		}
		jwtAuthStrategy, err := portopenbao.NormalizeJWTAuthStrategy(c.JWTAuthStrategy)
		if err != nil {
			return err
		}
		c.JWTAuthStrategy = jwtAuthStrategy
	case constants.BackupAuthMethodToken:
		if c.OpenBaoToken == "" {
			return fmt.Errorf("OpenBao token is required when using token authentication")
		}
	default:
		return fmt.Errorf("invalid auth method: %q", c.AuthMethod)
	}
	return nil
}

func validateStorageConfiguration(c *ExecutorConfig) error {
	if c.BackupBucket == "" {
		return fmt.Errorf("backup bucket is required")
	}

	// Provider-specific validation
	switch c.BackupProvider {
	case constants.StorageProviderS3, "":
		// S3 requires endpoint and region
		if c.BackupEndpoint == "" {
			return fmt.Errorf("backup endpoint is required for S3 provider")
		}
	case constants.StorageProviderGCS:
		// GCS doesn't require endpoint (uses default googleapis.com)
	case constants.StorageProviderAzure:
		// Azure requires storage account
		if c.AzureStorageAccount == "" && c.AzureConnectionString == "" {
			return fmt.Errorf("azure storage account or connection string is required")
		}
	default:
		return fmt.Errorf("invalid backup provider: %q", c.BackupProvider)
	}

	return nil
}

// LoadExecutorConfig loads configuration from environment variables and mounted files.
func LoadExecutorConfig() (*ExecutorConfig, error) {
	cfg := &ExecutorConfig{}

	if err := loadClusterConfig(cfg); err != nil {
		return nil, err
	}
	if err := loadStorageConfig(cfg); err != nil {
		return nil, err
	}
	if err := loadTLSConfig(cfg); err != nil {
		return nil, err
	}
	if err := loadAuthConfig(cfg); err != nil {
		return nil, err
	}
	if err := loadUploadConfig(cfg); err != nil {
		return nil, err
	}
	if err := loadClientConfig(cfg); err != nil {
		return nil, err
	}

	// Validate the configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}
