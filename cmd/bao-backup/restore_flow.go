package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dc-tec/openbao-operator/internal/platform/constants"
	"github.com/dc-tec/openbao-operator/internal/port/blobstore"
	portopenbao "github.com/dc-tec/openbao-operator/internal/port/openbao"
	backupconfig "github.com/dc-tec/openbao-operator/internal/service/backup"
)

type restoreSettings struct {
	key          string
	bucket       string
	endpoint     string
	region       string
	usePathStyle bool
	force        bool
}

// runRestore executes the restore operation.
func runRestore(ctx context.Context) error {
	flag.Parse()

	fmt.Println("Starting restore operation...")

	cfg, err := backupconfig.LoadExecutorConfig()
	if err != nil {
		return categorizef(errConfigCategory, "failed to load configuration: %w", err)
	}
	fmt.Printf("Configuration loaded - cluster=%s, namespace=%s, replicas=%d\n",
		cfg.ClusterName, cfg.ClusterNamespace, cfg.ClusterReplicas)

	settings, err := resolveRestoreSettings(cfg)
	if err != nil {
		return categorize(errConfigCategory, err)
	}
	fmt.Printf("Restore key: %s\n", settings.key)

	deadline, err := restorePreparationDeadline()
	if err != nil {
		return categorize(errConfigCategory, err)
	}
	prepareCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	restoreCfg := buildRestoreExecutorConfig(cfg, settings)
	storageClient, err := openStorageClient(prepareCtx, &restoreCfg)
	if err != nil {
		return categorizef(errStorageCategory, "failed to create storage client: %w", err)
	}
	defer func() { _ = storageClient.Close() }()

	return executeRestore(ctx, prepareCtx, storageClient, settings, constants.PathRestoreScratch,
		func(ctx context.Context) (portopenbao.ClusterActions, func(), error) {
			leaderURL, err := findRestoreLeader(ctx, cfg)
			if err != nil {
				return nil, nil, categorizef(errLeaderCategory, "failed to find leader: %w", err)
			}
			token, err := authenticate(ctx, cfg, leaderURL)
			if err != nil {
				return nil, nil, categorizef(errAuthCategory, "failed to authenticate: %w", err)
			}
			return openClusterClient(cfg, "restore", leaderURL, token)
		})
}

// executeRestore completes staging and client preparation before sending any bytes
// to the destructive endpoint. The caller supplies the persisted preparation deadline.
func executeRestore(
	ctx, prepareCtx context.Context,
	storageClient blobstore.BlobStore,
	settings restoreSettings,
	scratchDir string,
	connect func(context.Context) (portopenbao.ClusterActions, func(), error),
) (err error) {
	staged, err := downloadRestoreSnapshot(prepareCtx, storageClient, settings.key, scratchDir,
		constants.DefaultRestoreSnapshotLimitBytes)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, staged.Close()) }()
	fmt.Printf("Snapshot staged: size=%d, digest=%s\n", staged.size, staged.digest)

	baoClient, closeClient, err := connect(prepareCtx)
	if err != nil {
		return err
	}
	defer closeClient()
	if err := prepareCtx.Err(); err != nil {
		return categorizef(errSnapshotCategory, "restore preparation expired: %w", err)
	}

	fmt.Println("Submitting staged snapshot to cluster...")
	// The HTTP transport owns its request body, but staging owns this descriptor.
	// Hide Close so transport cleanup cannot close the file before our cleanup.
	reader := struct{ io.Reader }{staged.file}
	if err := baoClient.Restore(ctx, reader, portopenbao.RestoreOptions{Force: settings.force}); err != nil {
		return categorizef(errSnapshotCategory, "failed to restore snapshot: %w", err)
	}
	_, _ = fmt.Fprintf(os.Stdout, "Snapshot restore request accepted from: %s\n", settings.key)
	return nil
}

func restorePreparationDeadline() (time.Time, error) {
	value := os.Getenv(constants.EnvRestorePreparationDeadline)
	deadline, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must contain an absolute RFC3339 deadline: %w",
			constants.EnvRestorePreparationDeadline, err)
	}
	if !time.Now().Before(deadline) {
		return time.Time{}, fmt.Errorf("restore preparation deadline has expired")
	}
	return deadline, nil
}

func findRestoreLeader(ctx context.Context, cfg *backupconfig.ExecutorConfig) (string, error) {
	fmt.Println("Finding cluster leader...")
	leaderCtx, leaderCancel := context.WithTimeout(ctx, 60*time.Second)
	defer leaderCancel()

	return findLeader(leaderCtx, cfg)
}

func buildRestoreExecutorConfig(
	cfg *backupconfig.ExecutorConfig,
	settings restoreSettings,
) backupconfig.ExecutorConfig {
	restoreCfg := *cfg
	restoreCfg.BackupBucket = settings.bucket
	restoreCfg.BackupEndpoint = settings.endpoint
	restoreCfg.BackupRegion = settings.region
	restoreCfg.BackupUsePathStyle = settings.usePathStyle

	if restoreCfg.BackupProvider == constants.StorageProviderGCS {
		endpointLower := strings.ToLower(settings.endpoint)
		if strings.Contains(endpointLower, "fake-gcs-server") || strings.HasPrefix(endpointLower, "http://") {
			restoreCfg.GCSUseEmulator = true
		}
	}

	if restoreCfg.StorageCredentials == nil {
		restoreCfg.StorageCredentials = &blobstore.Credentials{
			Region: settings.region,
		}
	} else if restoreCfg.StorageCredentials.Region == "" {
		restoreCfg.StorageCredentials.Region = settings.region
	}

	return restoreCfg
}

func resolveRestoreSettings(cfg *backupconfig.ExecutorConfig) (restoreSettings, error) {
	if cfg == nil {
		return restoreSettings{}, fmt.Errorf("restore configuration is required")
	}

	settings := restoreSettings{
		key: strings.TrimSpace(os.Getenv(constants.EnvRestoreKey)),
	}
	if settings.key == "" {
		return restoreSettings{}, fmt.Errorf("%s environment variable is required", constants.EnvRestoreKey)
	}

	settings.bucket = strings.TrimSpace(os.Getenv(constants.EnvRestoreBucket))
	if settings.bucket == "" {
		settings.bucket = cfg.BackupBucket
	}

	settings.endpoint = strings.TrimSpace(os.Getenv(constants.EnvRestoreEndpoint))
	if settings.endpoint == "" {
		settings.endpoint = cfg.BackupEndpoint
	}

	settings.region = strings.TrimSpace(os.Getenv(constants.EnvRestoreRegion))
	if settings.region == "" {
		settings.region = cfg.BackupRegion
	}

	settings.usePathStyle = strings.EqualFold(strings.TrimSpace(os.Getenv(constants.EnvRestoreUsePathStyle)), "true")
	forceValue := strings.TrimSpace(os.Getenv(constants.EnvRestoreForce))
	if forceValue != "" {
		force, err := strconv.ParseBool(forceValue)
		if err != nil {
			return restoreSettings{}, fmt.Errorf("invalid %s value %q: %w", constants.EnvRestoreForce, forceValue, err)
		}
		settings.force = force
	}

	return settings, nil
}
