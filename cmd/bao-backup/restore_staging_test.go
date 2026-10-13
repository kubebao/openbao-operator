package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dc-tec/openbao-operator/internal/port/blobstore"
)

type restoreFlowStore struct {
	backupFlowBlobStore
	downloads int
	reader    io.ReadCloser
	err       error
}

func (s *restoreFlowStore) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	s.downloads++
	if s.err != nil {
		return nil, s.err
	}
	if s.reader != nil {
		return s.reader, nil
	}
	return s.backupFlowBlobStore.Download(ctx, key)
}

func TestDownloadRestoreSnapshot(t *testing.T) {
	t.Parallel()
	data := []byte("complete-snapshot")
	store := &restoreFlowStore{backupFlowBlobStore: backupFlowBlobStore{object: data}}
	dir := t.TempDir()
	staged, err := downloadRestoreSnapshot(context.Background(), store, "snapshot", dir, int64(len(data)))
	require.NoError(t, err)
	require.Equal(t, int64(len(data)), staged.size)
	require.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256(data)), staged.digest)
	info, err := staged.file.Stat()
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	body, err := io.ReadAll(staged.file)
	require.NoError(t, err)
	require.Equal(t, data, body, "the same descriptor is rewound before submission")
	require.NoError(t, staged.Close())
	assertRestoreScratchEmpty(t, dir)
}

func TestDownloadRestoreSnapshotRejectsInvalidSizeBeforeDownload(t *testing.T) {
	t.Parallel()
	for _, size := range []int64{-1, 0, 17} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			store := &restoreFlowStore{backupFlowBlobStore: backupFlowBlobStore{
				useHeadResult: true, headResult: &blobstore.ObjectInfo{Size: size},
			}}
			dir := t.TempDir()
			staged, err := downloadRestoreSnapshot(context.Background(), store, "snapshot", dir, 16)
			require.ErrorIs(t, err, errVerificationCategory)
			require.Nil(t, staged)
			require.Zero(t, store.downloads)
			assertRestoreScratchEmpty(t, dir)
		})
	}
}

func TestStageRestoreSnapshotRejectsIncompleteOrGrowingBody(t *testing.T) {
	t.Parallel()
	readErr := errors.New("download connection lost")
	for _, tt := range []struct {
		name   string
		reader io.Reader
		want   string
	}{
		{"truncated", strings.NewReader("short"), "size mismatch"},
		{"oversized", strings.NewReader("snapshot-extra"), "exceeds its declared size"},
		{"interrupted", io.MultiReader(strings.NewReader("part"), restoreErrorReader{readErr}), "connection lost"},
		{
			"error after final byte",
			io.MultiReader(strings.NewReader("snapshot"), restoreErrorReader{readErr}), "connection lost",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			staged, err := stageRestoreSnapshot(context.Background(), tt.reader, dir, 8)
			require.ErrorContains(t, err, tt.want)
			require.Nil(t, staged)
			assertRestoreScratchEmpty(t, dir)
		})
	}
}

func TestWriteRestoreSnapshotDiskFailures(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"write", "sync", "seek"} {
		t.Run(operation, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "snapshot-*")
			require.NoError(t, err)
			defer func() { _ = file.Close() }()
			fault := &restoreFaultFile{File: file, operation: operation}
			digest, err := writeRestoreSnapshot(context.Background(), fault, strings.NewReader("snapshot"), 8)
			require.ErrorIs(t, err, syscall.ENOSPC)
			require.Empty(t, digest)
		})
	}
}

func TestWriteRestoreSnapshotDoesNotStoreExtraBytes(t *testing.T) {
	t.Parallel()
	file, err := os.CreateTemp(t.TempDir(), "snapshot-*")
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	source := bytes.NewReader(bytes.Repeat([]byte{'x'}, 1024))
	_, err = writeRestoreSnapshot(context.Background(), file, source, 8)
	require.ErrorContains(t, err, "exceeds")
	info, err := file.Stat()
	require.NoError(t, err)
	require.Equal(t, int64(8), info.Size())
	require.Equal(t, 1024-9, source.Len(), "read at most one byte beyond the limit")
}

func TestStageRestoreSnapshotCancellation(t *testing.T) {
	t.Parallel()
	for _, beforeRead := range []bool{true, false} {
		t.Run(fmt.Sprint(beforeRead), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if beforeRead {
				cancel()
			}
			dir := t.TempDir()
			staged, err := stageRestoreSnapshot(ctx, &restoreCancellingReader{cancel: cancel}, dir, 8)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, staged)
			assertRestoreScratchEmpty(t, dir)
		})
	}
}

func TestDownloadRestoreSnapshotCloseFailureRemovesStaging(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("source close failed")
	store := &restoreFlowStore{
		backupFlowBlobStore: backupFlowBlobStore{object: []byte("snapshot")},
		reader:              &restoreCloseErrorReader{Reader: strings.NewReader("snapshot"), err: closeErr},
	}
	dir := t.TempDir()
	staged, err := downloadRestoreSnapshot(context.Background(), store, "snapshot", dir, 8)
	require.ErrorIs(t, err, closeErr)
	require.Nil(t, staged)
	assertRestoreScratchEmpty(t, dir)
}

type restoreFaultFile struct {
	*os.File
	operation string
}

func (f *restoreFaultFile) Write(p []byte) (int, error) {
	if f.operation == "write" {
		return 0, syscall.ENOSPC
	}
	return f.File.Write(p)
}

func (f *restoreFaultFile) Sync() error {
	if f.operation == "sync" {
		return syscall.ENOSPC
	}
	return f.File.Sync()
}

func (f *restoreFaultFile) Seek(offset int64, whence int) (int64, error) {
	if f.operation == "seek" {
		return 0, syscall.ENOSPC
	}
	return f.File.Seek(offset, whence)
}

type restoreErrorReader struct{ err error }

func (r restoreErrorReader) Read([]byte) (int, error) { return 0, r.err }

type restoreCancellingReader struct{ cancel context.CancelFunc }

func (r *restoreCancellingReader) Read(p []byte) (int, error) {
	r.cancel()
	return copy(p, "snapshot"), nil
}

type restoreCloseErrorReader struct {
	io.Reader
	err error
}

func (r *restoreCloseErrorReader) Close() error { return r.err }

func assertRestoreScratchEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "scratch files must be removed after success and failure")
}
