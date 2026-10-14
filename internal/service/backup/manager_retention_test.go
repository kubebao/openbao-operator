package backup

import (
	"context"
	"io"

	"github.com/kubebao/openbao-operator/internal/port/blobstore"
)

type fakeBlobStore struct {
	objects    []blobstore.ObjectInfo
	deleted    []string
	closeCount int
	listCount  int
}

func (f *fakeBlobStore) Upload(_ context.Context, _ string, _ io.Reader) error {
	return nil
}

func (f *fakeBlobStore) Download(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, nil
}

func (f *fakeBlobStore) Delete(_ context.Context, _ string) error {
	return nil
}

func (f *fakeBlobStore) DeleteBatch(_ context.Context, keys []string) error {
	f.deleted = append(f.deleted, keys...)
	return nil
}

func (f *fakeBlobStore) List(_ context.Context, _ string) ([]blobstore.ObjectInfo, error) {
	f.listCount++
	return f.objects, nil
}

func (f *fakeBlobStore) Head(_ context.Context, _ string) (*blobstore.ObjectInfo, error) {
	return nil, nil
}

func (f *fakeBlobStore) Close() error {
	f.closeCount++
	return nil
}
