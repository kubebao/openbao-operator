package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/kubebao/openbao-operator/internal/adapter/storage"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/blobstore"
	backupconfig "github.com/kubebao/openbao-operator/internal/service/backup"
)

func buildStorageConfig(cfg *backupconfig.ExecutorConfig) (storage.Config, error) {
	if cfg == nil {
		return storage.Config{}, fmt.Errorf("storage configuration is required")
	}

	destination := blobstore.ResolveDestination(blobstore.Destination{
		Provider: cfg.BackupProvider,
		Endpoint: cfg.BackupEndpoint,
		Bucket:   cfg.BackupBucket,
		Region:   cfg.BackupRegion,
	}, cfg.AzureContainer)

	storageConfig := storage.Config{
		Provider:                 storage.ProviderType(destination.Provider),
		Bucket:                   destination.Bucket,
		Endpoint:                 destination.Endpoint,
		ValidateEndpointRequests: true,
	}

	switch destination.Provider {
	case constants.StorageProviderS3:
		storageConfig.Region = destination.Region
		if cfg.StorageCredentials != nil {
			credentials := *cfg.StorageCredentials
			if len(cfg.StorageCACert) > 0 {
				credentials.CACert = cfg.StorageCACert
			}
			storageConfig.Credentials = &credentials
		} else {
			storageConfig.Credentials = &blobstore.Credentials{
				Region: destination.Region,
				CACert: cfg.StorageCACert,
			}
		}
		storageConfig.S3 = &storage.S3Options{
			UsePathStyle:       cfg.BackupUsePathStyle,
			InsecureSkipVerify: cfg.InsecureSkipVerify,
			EnsureExists:       true,
		}

		if !cfg.InsecureSkipVerify && strings.HasPrefix(strings.ToLower(cfg.BackupEndpoint), "http://") {
			storageConfig.S3.InsecureSkipVerify = true
		}

	case constants.StorageProviderGCS:
		storageConfig.GCS = &storage.GCSOptions{
			Project:            cfg.GCSProject,
			CredentialsJSON:    cfg.GCSCredentialsJSON,
			UseEmulator:        cfg.GCSUseEmulator,
			InsecureSkipVerify: cfg.InsecureSkipVerify,
			CACert:             cfg.StorageCACert,
		}
		if !cfg.InsecureSkipVerify && cfg.GCSUseEmulator &&
			strings.HasPrefix(strings.ToLower(cfg.BackupEndpoint), "http://") {
			storageConfig.GCS.InsecureSkipVerify = true
		}

	case constants.StorageProviderAzure:
		storageConfig.Azure = &storage.AzureOptions{
			StorageAccount:     cfg.AzureStorageAccount,
			AccountKey:         cfg.AzureAccountKey,
			ConnectionString:   cfg.AzureConnectionString,
			UseManagedIdentity: cfg.AzureAccountKey == "" && cfg.AzureConnectionString == "",
			InsecureSkipVerify: cfg.InsecureSkipVerify,
			CACert:             cfg.StorageCACert,
		}
	default:
		return storage.Config{}, fmt.Errorf("unknown storage provider: %q", cfg.BackupProvider)
	}

	return storageConfig, nil
}

func openStorageClient(ctx context.Context, cfg *backupconfig.ExecutorConfig) (blobstore.BlobStore, error) {
	storageConfig, err := buildStorageConfig(cfg)
	if err != nil {
		return nil, err
	}

	if err := backupconfig.ValidateStorageEndpointAccess(ctx, storageConfig); err != nil {
		return nil, err
	}

	return storage.OpenBlobStore(ctx, storageConfig)
}
