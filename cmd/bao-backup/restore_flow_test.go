package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dc-tec/openbao-operator/internal/platform/constants"
	openbaotest "github.com/dc-tec/openbao-operator/internal/platform/testutil/openbao"
	"github.com/dc-tec/openbao-operator/internal/port/blobstore"
	portopenbao "github.com/dc-tec/openbao-operator/internal/port/openbao"
	backupconfig "github.com/dc-tec/openbao-operator/internal/service/backup"
)

func TestExecuteRestoreStagesBeforeSingleHTTPRequest(t *testing.T) {
	t.Parallel()
	for _, result := range []string{"accepted", "rejected", "response lost"} {
		t.Run(result, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			bodies := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != testBackupForceRestorePath {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read snapshot request: %v", err)
				}
				bodies <- string(body)
				switch result {
				case "accepted":
					w.WriteHeader(http.StatusNoContent)
				case "rejected":
					http.Error(w, "rejected", http.StatusInternalServerError)
				case "response lost":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Errorf("hijack connection: %v", err)
						return
					}
					_ = conn.Close()
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			store := &restoreFlowStore{backupFlowBlobStore: backupFlowBlobStore{object: []byte("snapshot")}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			err := executeRestore(ctx, ctx, store, restoreSettings{key: "snapshot", force: true}, dir,
				func(context.Context) (portopenbao.ClusterActions, func(), error) {
					entries, err := os.ReadDir(dir)
					require.NoError(t, err)
					require.Len(t, entries, 1, "authentication must follow completed staging")
					info, err := entries[0].Info()
					require.NoError(t, err)
					require.Equal(t, int64(8), info.Size())
					require.Zero(t, requests.Load())
					return openClusterClient(&backupconfig.ExecutorConfig{}, "restore", server.URL, "test-token")
				})
			if result == "accepted" {
				require.NoError(t, err, "the HTTP transport must not close the staging-owned descriptor")
			} else {
				require.ErrorIs(t, err, errSnapshotCategory)
			}
			require.Equal(t, int32(1), requests.Load())
			require.Equal(t, "snapshot", <-bodies)
			require.Equal(t, 1, store.downloads)
			assertRestoreScratchEmpty(t, dir)
		})
	}
}

func TestExecuteRestorePreparationFailureCannotSubmit(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		size int64
		body io.ReadCloser
	}{
		{"oversized metadata", constants.DefaultRestoreSnapshotLimitBytes + 1, nil},
		{"truncated body", 8, io.NopCloser(strings.NewReader("short"))},
		{"growing body", 8, io.NopCloser(strings.NewReader("snapshot-extra"))},
		{"download failure", 8, io.NopCloser(restoreErrorReader{errors.New("connection lost")})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := &restoreFlowStore{
				backupFlowBlobStore: backupFlowBlobStore{useHeadResult: true, headResult: &blobstore.ObjectInfo{Size: tt.size}},
				reader:              tt.body,
			}
			err := executeRestore(context.Background(), context.Background(), store, restoreSettings{key: "snapshot"}, dir,
				func(context.Context) (portopenbao.ClusterActions, func(), error) {
					t.Fatal("failed preparation must not connect to OpenBao or submit a snapshot")
					return nil, nil, nil
				})
			require.Error(t, err)
			assertRestoreScratchEmpty(t, dir)
		})
	}
}

func TestExecuteRestoreScratchCreationFailureCannotSubmit(t *testing.T) {
	t.Parallel()
	store := &restoreFlowStore{backupFlowBlobStore: backupFlowBlobStore{object: []byte("snapshot")}}
	dir := t.TempDir()
	err := executeRestore(context.Background(), context.Background(), store, restoreSettings{key: "snapshot"},
		filepath.Join(dir, "missing"), func(context.Context) (portopenbao.ClusterActions, func(), error) {
			t.Fatal("scratch failure must not connect to OpenBao")
			return nil, nil, nil
		})
	require.ErrorIs(t, err, os.ErrNotExist)
	assertRestoreScratchEmpty(t, dir)
}

func TestExecuteRestoreAuthenticationFailureAndExpiryCannotSubmit(t *testing.T) {
	t.Parallel()
	for _, expires := range []bool{false, true} {
		t.Run(map[bool]string{false: "authentication failure", true: "preparation expired"}[expires], func(t *testing.T) {
			dir := t.TempDir()
			store := &restoreFlowStore{backupFlowBlobStore: backupFlowBlobStore{object: []byte("snapshot")}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clientClosed := false
			err := executeRestore(context.Background(), ctx, store, restoreSettings{key: "snapshot"}, dir,
				func(context.Context) (portopenbao.ClusterActions, func(), error) {
					if !expires {
						return nil, nil, categorizef(errAuthCategory, "login rejected")
					}
					cancel()
					return &openbaotest.MockClusterActions{RestoreFunc: func(
						context.Context, io.Reader, portopenbao.RestoreOptions,
					) error {
						t.Fatal("expired preparation must not submit a snapshot")
						return nil
					}}, func() { clientClosed = true }, nil
				})
			if expires {
				require.ErrorIs(t, err, context.Canceled)
				require.True(t, clientClosed)
			} else {
				require.ErrorIs(t, err, errAuthCategory)
			}
			assertRestoreScratchEmpty(t, dir)
		})
	}
}

func TestRestorePreparationDeadline(t *testing.T) {
	for _, value := range []string{"", "invalid", "2000-01-01T00:00:00Z"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(constants.EnvRestorePreparationDeadline, value)
			_, err := restorePreparationDeadline()
			require.Error(t, err)
		})
	}
	t.Run("preserves supplied absolute deadline", func(t *testing.T) {
		deadline := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
		t.Setenv(constants.EnvRestorePreparationDeadline, deadline.Format(time.RFC3339))
		got, err := restorePreparationDeadline()
		require.NoError(t, err)
		require.Equal(t, deadline, got)
	})
}
