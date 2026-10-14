package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kubebao/openbao-operator/internal/port/blobstore"
	backupconfig "github.com/kubebao/openbao-operator/internal/service/backup"
	"github.com/stretchr/testify/require"
)

type retentionFlowStore struct {
	backupFlowBlobStore
	objects                []blobstore.ObjectInfo
	listCalls, deleteCalls int
	deleteErr              error
}

func (s *retentionFlowStore) List(context.Context, string) ([]blobstore.ObjectInfo, error) {
	s.listCalls++
	return s.objects, nil
}
func (s *retentionFlowStore) DeleteBatch(context.Context, []string) error {
	s.deleteCalls++
	return s.deleteErr
}

func TestRetentionWorkerDeadlineAndDeletionFailures(t *testing.T) {
	prefix := "backups/test/bao/"
	for _, scenario := range []string{"expired deadline", "partial batch", "success"} {
		t.Run(scenario, func(t *testing.T) {
			store := &retentionFlowStore{objects: []blobstore.ObjectInfo{
				{Key: prefix + "2026-10-14T10-00-00Z-aaaaaaaa.snap"},
				{Key: prefix + "2026-10-13T10-00-00Z-bbbbbbbb.snap"},
			}}
			cfg := &backupconfig.RetentionExecutorConfig{SourceJobUID: "source-uid", Prefix: prefix,
				Policy: backupconfig.RetentionPolicy{MaxCount: 1, ProtectedKey: store.objects[0].Key}}
			deadline := time.Now().Add(time.Hour)
			if scenario == "expired deadline" {
				deadline = time.Now().Add(-time.Hour)
			}
			if scenario == "partial batch" {
				store.deleteErr = errors.New("first delete succeeded, second delete failed")
			}
			ctx, cancel := context.WithDeadline(t.Context(), deadline)
			defer cancel()
			report, err := executeRetention(ctx, cfg, store)
			if scenario == "success" {
				require.NoError(t, err)
				require.True(t, report.Succeeded)
				require.EqualValues(t, 1, report.DeletedByCount)
			} else {
				require.Error(t, err)
				require.False(t, report.Succeeded)
				require.Zero(t, report.DeletedByCount)
			}
			if scenario == "expired deadline" {
				require.Zero(t, store.listCalls)
				require.Zero(t, store.deleteCalls)
			} else {
				require.Equal(t, 1, store.listCalls)
				require.Equal(t, 1, store.deleteCalls)
			}
		})
	}
}

func TestRetentionStorageConfigurationDoesNotCreateBuckets(t *testing.T) {
	cfg, err := buildRetentionStorageConfig(&backupconfig.ExecutorConfig{
		BackupProvider: "s3",
		BackupEndpoint: "https://s3.example",
		BackupBucket:   "snapshots",
	})
	require.NoError(t, err)
	require.False(t, cfg.EnsureExists)
	require.NotNil(t, cfg.S3)
	require.False(t, cfg.S3.EnsureExists)
	require.True(t, cfg.ValidateEndpointRequests)
}
