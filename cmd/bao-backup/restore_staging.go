package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/dc-tec/openbao-operator/internal/port/blobstore"
)

// stagedRestoreSnapshot owns the original file descriptor until submission ends.
// Its digest identifies the bytes read; a key-only download does not prove source
// provenance or bind these bytes to a trusted backup manifest.
type stagedRestoreSnapshot struct {
	file   *os.File
	digest string
	size   int64
}

func (s *stagedRestoreSnapshot) Close() error {
	return errors.Join(s.file.Close(), os.Remove(s.file.Name()))
}

func downloadRestoreSnapshot(
	ctx context.Context,
	storageClient blobstore.BlobStore,
	key, scratchDir string,
	maxBytes int64,
) (*stagedRestoreSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, categorize(errStorageCategory, err)
	}
	info, err := storageClient.Head(ctx, key)
	if err != nil {
		return nil, categorizef(errVerificationCategory, "failed to read snapshot metadata: %w", err)
	}
	if info == nil || info.Size <= 0 || info.Size > maxBytes {
		return nil, categorizef(errVerificationCategory, "snapshot must have a positive size of at most %d bytes", maxBytes)
	}

	// HEAD is only a size check. This legacy interface cannot select an immutable
	// object version; do not treat matching lengths as content verification.
	reader, err := storageClient.Download(ctx, key)
	if err != nil {
		return nil, categorizef(errStorageCategory, "failed to download snapshot: %w", err)
	}
	staged, stageErr := stageRestoreSnapshot(ctx, reader, scratchDir, info.Size)
	closeErr := reader.Close()
	if err := errors.Join(stageErr, closeErr); err != nil {
		if staged != nil {
			err = errors.Join(err, staged.Close())
		}
		return nil, categorizef(errStorageCategory, "failed to stage snapshot: %w", err)
	}
	return staged, nil
}

func stageRestoreSnapshot(
	ctx context.Context, reader io.Reader, scratchDir string, size int64,
) (_ *stagedRestoreSnapshot, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// CreateTemp uses exclusive creation and mode 0600. Never reopen a path or
	// resume an existing partial download, including after a process restart.
	file, err := os.CreateTemp(scratchDir, "snapshot-*")
	if err != nil {
		return nil, fmt.Errorf("create snapshot scratch file: %w", err)
	}
	staged := &stagedRestoreSnapshot{file: file, size: size}
	defer func() {
		if err != nil {
			err = errors.Join(err, staged.Close())
		}
	}()

	staged.digest, err = writeRestoreSnapshot(ctx, file, reader, size)
	if err != nil {
		return nil, err
	}
	return staged, nil
}

type restoreScratchFile interface {
	io.Writer
	io.Seeker
	Sync() error
}

func writeRestoreSnapshot(ctx context.Context, file restoreScratchFile, reader io.Reader, size int64) (string, error) {
	hash := sha256.New()
	source := restoreContextReader{ctx: ctx, reader: reader}
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(source, size))
	if err != nil {
		return "", fmt.Errorf("write snapshot scratch file: %w", err)
	}
	if n != size {
		return "", fmt.Errorf("snapshot size mismatch: expected %d bytes, received %d", size, n)
	}
	// Probe beyond the declared length without storing an extra byte. The file
	// never grows beyond the previously checked limit, even if metadata lied.
	var extra [1]byte
	next, err := io.ReadFull(source, extra[:])
	if next != 0 {
		return "", fmt.Errorf("snapshot exceeds its declared size of %d bytes", size)
	}
	if !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("finish snapshot download: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync snapshot scratch file: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind snapshot scratch file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

type restoreContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r restoreContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
