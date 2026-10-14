package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/blobstore"
)

// loadStorageConfig loads backup storage endpoint, bucket, and path configuration.
func loadStorageConfig(cfg *ExecutorConfig) error {
	cfg.BackupProvider = blobstore.EffectiveProvider(strings.TrimSpace(os.Getenv(constants.EnvBackupProvider)))

	cfg.BackupEndpoint = strings.TrimSpace(os.Getenv(constants.EnvBackupEndpoint))
	cfg.BackupBucket = strings.TrimSpace(os.Getenv(constants.EnvBackupBucket))
	if cfg.BackupBucket == "" {
		return fmt.Errorf("%s environment variable is required", constants.EnvBackupBucket)
	}

	cfg.BackupPathPrefix = strings.TrimSpace(os.Getenv(constants.EnvBackupPathPrefix))
	cfg.BackupFilenamePrefix = strings.TrimSpace(os.Getenv(constants.EnvBackupFilenamePrefix))
	cfg.BackupKey = strings.TrimSpace(os.Getenv(constants.EnvBackupKey))
	caCert, err := loadStorageCACert(backupCredentialsPath())
	if err != nil {
		return fmt.Errorf("failed to load storage CA certificate: %w", err)
	}
	cfg.StorageCACert = caCert

	switch cfg.BackupProvider {
	case constants.StorageProviderS3:
		return loadS3StorageConfig(cfg)
	case constants.StorageProviderGCS:
		return loadGCSStorageConfig(cfg)
	case constants.StorageProviderAzure:
		return loadAzureStorageConfig(cfg)
	default:
		return nil
	}
}

func loadStorageCACert(credsPath string) ([]byte, error) {
	cleanCredsPath, err := validatePath("/", credsPath)
	if err != nil {
		return nil, fmt.Errorf("invalid credentials path: %w", err)
	}
	caCertPath, err := validatePath(cleanCredsPath, filepath.Join(cleanCredsPath, blobstore.SecretKeyCACert))
	if err != nil {
		return nil, fmt.Errorf("invalid CA certificate path: %w", err)
	}
	data, err := os.ReadFile(caCertPath) // #nosec G304 -- Path validated under the credentials directory.
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read storage CA certificate: %w", err)
	}
	return data, nil
}

func loadS3StorageConfig(cfg *ExecutorConfig) error {
	cfg.BackupRegion = blobstore.ResolveDestination(blobstore.Destination{
		Provider: cfg.BackupProvider,
		Region:   strings.TrimSpace(os.Getenv(constants.EnvBackupRegion)),
	}, "").Region

	usePathStyleStr := strings.TrimSpace(os.Getenv(constants.EnvBackupUsePathStyle))
	if usePathStyleStr != "" {
		usePathStyle, err := strconv.ParseBool(usePathStyleStr)
		if err != nil {
			return fmt.Errorf("invalid BACKUP_USE_PATH_STYLE value %q: %w", usePathStyleStr, err)
		}
		cfg.BackupUsePathStyle = usePathStyle
	}

	creds, err := loadStorageCredentials(backupCredentialsPath())
	if err != nil {
		return fmt.Errorf("failed to load storage credentials: %w", err)
	}
	cfg.StorageCredentials = creds
	region, err := loadStorageRegion(backupCredentialsPath())
	if err != nil {
		return fmt.Errorf("failed to load storage region: %w", err)
	}
	cfg.StorageRegion = region
	return nil
}

func loadStorageRegion(credsPath string) (string, error) {
	cleanCredsPath, err := validatePath("/", credsPath)
	if err != nil {
		return "", fmt.Errorf("invalid credentials path: %w", err)
	}
	regionPath, err := validatePath(cleanCredsPath, filepath.Join(cleanCredsPath, blobstore.SecretKeyRegion))
	if err != nil {
		return "", fmt.Errorf("invalid region path: %w", err)
	}
	data, err := os.ReadFile(regionPath) // #nosec G304 -- Path validated under the credentials directory.
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read storage region: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func loadGCSStorageConfig(cfg *ExecutorConfig) error {
	cfg.GCSProject = strings.TrimSpace(os.Getenv(constants.EnvBackupGCSProject))

	gcsCredsPath := filepath.Clean(filepath.Join(backupCredentialsPath(), "credentials.json"))
	if data, err := os.ReadFile(gcsCredsPath); err == nil { // #nosec G304
		cfg.GCSCredentialsJSON = data
	}

	endpoint := strings.TrimSpace(os.Getenv(constants.EnvBackupEndpoint))
	if endpoint == "" {
		endpoint = cfg.BackupEndpoint
	}
	if strings.Contains(strings.ToLower(endpoint), "fake-gcs-server") || strings.HasPrefix(strings.ToLower(endpoint), "http://") {
		cfg.GCSUseEmulator = true
	}

	return nil
}

func loadAzureStorageConfig(cfg *ExecutorConfig) error {
	cfg.AzureStorageAccount = strings.TrimSpace(os.Getenv(constants.EnvBackupAzureStorageAccount))
	cfg.AzureContainer = strings.TrimSpace(os.Getenv(constants.EnvBackupAzureContainer))

	credsPath := backupCredentialsPath()

	accountKeyPath := filepath.Clean(filepath.Join(credsPath, "accountKey"))
	if data, err := os.ReadFile(accountKeyPath); err == nil { // #nosec G304
		cfg.AzureAccountKey = strings.TrimSpace(string(data))
	}

	connStringPath := filepath.Clean(filepath.Join(credsPath, "connectionString"))
	if data, err := os.ReadFile(connStringPath); err == nil { // #nosec G304
		cfg.AzureConnectionString = strings.TrimSpace(string(data))
	}

	return nil
}

func backupCredentialsPath() string {
	credsPath := constants.PathBackupCredentials
	if envPath := strings.TrimSpace(os.Getenv(constants.EnvBackupCredentialsPath)); envPath != "" {
		credsPath = envPath
	}
	return credsPath
}

// validatePath ensures a file path is within the base directory and doesn't contain path traversal.
func validatePath(baseDir, filePath string) (string, error) {
	cleanBase := filepath.Clean(baseDir)
	cleanPath := filepath.Clean(filePath)

	relPath, err := filepath.Rel(cleanBase, cleanPath)
	if err != nil {
		return "", fmt.Errorf("invalid path %q relative to base %q: %w", filePath, baseDir, err)
	}
	if strings.HasPrefix(relPath, "..") {
		return "", fmt.Errorf("path %q attempts to escape base directory %q", filePath, baseDir)
	}

	return cleanPath, nil
}

// loadStorageCredentials loads S3 credentials from the mounted directory.
func loadStorageCredentials(credsPath string) (*blobstore.Credentials, error) {
	creds := &blobstore.Credentials{}

	cleanCredsPath, err := validatePath("/", credsPath)
	if err != nil {
		return nil, fmt.Errorf("invalid credentials path: %w", err)
	}
	if _, err := os.Stat(cleanCredsPath); os.IsNotExist(err) {
		return nil, nil
	}

	accessKeyPath := filepath.Join(cleanCredsPath, "accessKeyId")
	validatedAccessKeyPath, err := validatePath(cleanCredsPath, accessKeyPath)
	if err != nil {
		return nil, fmt.Errorf("invalid access key path: %w", err)
	}
	if data, err := os.ReadFile(validatedAccessKeyPath); err == nil { // #nosec G304 -- Path validated under base directory
		creds.AccessKeyID = strings.TrimSpace(string(data))
	}

	secretKeyPath := filepath.Join(cleanCredsPath, "secretAccessKey")
	validatedSecretKeyPath, err := validatePath(cleanCredsPath, secretKeyPath)
	if err != nil {
		return nil, fmt.Errorf("invalid secret key path: %w", err)
	}
	if data, err := os.ReadFile(validatedSecretKeyPath); err == nil { // #nosec G304 -- Path validated under base directory
		creds.SecretAccessKey = strings.TrimSpace(string(data))
	}

	if (creds.AccessKeyID != "" && creds.SecretAccessKey == "") ||
		(creds.AccessKeyID == "" && creds.SecretAccessKey != "") {
		return nil, fmt.Errorf("both accessKeyId and secretAccessKey must be provided if using static credentials")
	}

	sessionTokenPath := filepath.Join(cleanCredsPath, "sessionToken")
	validatedSessionTokenPath, err := validatePath(cleanCredsPath, sessionTokenPath)
	if err != nil {
		return nil, fmt.Errorf("invalid session token path: %w", err)
	}
	if data, err := os.ReadFile(validatedSessionTokenPath); err == nil { // #nosec G304 -- Path validated under base directory
		creds.SessionToken = strings.TrimSpace(string(data))
	}

	region, err := loadStorageRegion(cleanCredsPath)
	if err != nil {
		return nil, err
	}
	creds.Region = region

	caCertPath := filepath.Join(cleanCredsPath, "caCert")
	validatedCACertPath, err := validatePath(cleanCredsPath, caCertPath)
	if err != nil {
		return nil, fmt.Errorf("invalid CA cert path: %w", err)
	}
	if data, err := os.ReadFile(validatedCACertPath); err == nil { // #nosec G304 -- Path validated under base directory
		creds.CACert = data
	}

	if creds.AccessKeyID == "" && creds.SecretAccessKey == "" {
		return nil, nil
	}

	return creds, nil
}
