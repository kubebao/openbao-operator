package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/go-logr/logr"
	"github.com/kubebao/openbao-operator/internal/adapter/storage"
	"github.com/kubebao/openbao-operator/internal/port/blobstore"
	backupconfig "github.com/kubebao/openbao-operator/internal/service/backup"
)

func runRetention(ctx context.Context) error {
	cfg, err := backupconfig.LoadRetentionExecutorConfig()
	if err != nil {
		return categorizef(errConfigCategory, "load retention configuration: %w", err)
	}
	ctx, cancel := context.WithDeadline(ctx, cfg.Deadline)
	defer cancel()
	report := backupconfig.RetentionReport{SourceJobUID: cfg.SourceJobUID, Key: cfg.Policy.ProtectedKey}
	if ctx.Err() != nil {
		report.Reason = backupconfig.RetentionReasonDeadlineExceeded
		return writeRetentionReport(report, ctx.Err())
	}
	store, err := openRetentionStorageClient(ctx, cfg.Storage)
	if err != nil {
		report.Reason = backupconfig.RetentionReasonStorageAccessFailed
		return writeRetentionReport(report, err)
	}
	defer func() { _ = store.Close() }()
	report, err = executeRetention(ctx, cfg, store)
	return writeRetentionReport(report, err)
}

func executeRetention(
	ctx context.Context,
	cfg *backupconfig.RetentionExecutorConfig,
	store blobstore.BlobStore,
) (backupconfig.RetentionReport, error) {
	report := backupconfig.RetentionReport{SourceJobUID: cfg.SourceJobUID, Key: cfg.Policy.ProtectedKey}
	if err := ctx.Err(); err != nil {
		report.Reason = backupconfig.RetentionReasonDeadlineExceeded
		return report, err
	}
	result, err := backupconfig.ApplyRetention(ctx, logr.Discard(), store, cfg.Prefix, cfg.Policy)
	if err != nil {
		report.Reason = backupconfig.RetentionReasonStorageListFailed
		return report, err
	}
	if len(result.Errors) != 0 {
		report.Reason = backupconfig.RetentionReasonStorageDeleteFailed
		return report, errors.Join(result.Errors...)
	}
	if result.DeletedByCount > math.MaxInt32 || result.DeletedByAge > math.MaxInt32 {
		report.Reason = backupconfig.RetentionReasonOutcomeInvalid
		return report, fmt.Errorf("retention deletion counts exceed the receipt limit")
	}
	// ApplyRetention only reports counts after a completely successful batch.
	// #nosec G115 -- Both nonnegative counts are checked against MaxInt32 above.
	report.DeletedByCount, report.DeletedByAge = int32(result.DeletedByCount), int32(result.DeletedByAge)
	report.Succeeded, report.Reason = true, backupconfig.RetentionReasonCompleted
	return report, nil
}

func writeRetentionReport(report backupconfig.RetentionReport, outcomeErr error) error {
	data, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode retention outcome: %w", err)
	}
	if err := os.WriteFile("/dev/termination-log", data, 0600); err != nil {
		return fmt.Errorf("write retention outcome: %w", err)
	}
	if outcomeErr != nil {
		return categorizef(errStorageCategory, "retention failed: %w", outcomeErr)
	}
	return nil
}

func openRetentionStorageClient(ctx context.Context, cfg *backupconfig.ExecutorConfig) (blobstore.BlobStore, error) {
	storageConfig, err := buildRetentionStorageConfig(cfg)
	if err != nil {
		return nil, err
	}
	if err := backupconfig.ValidateStorageEndpointAccess(ctx, storageConfig); err != nil {
		return nil, err
	}
	return storage.OpenBlobStore(ctx, storageConfig)
}

func buildRetentionStorageConfig(cfg *backupconfig.ExecutorConfig) (storage.Config, error) {
	storageConfig, err := buildStorageConfig(cfg)
	if err != nil {
		return storage.Config{}, err
	}
	if storageConfig.S3 != nil {
		storageConfig.S3.EnsureExists = false
	}
	return storageConfig, nil
}
