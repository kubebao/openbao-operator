package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/require"
)

type snapshotRoundTripFunc func(*http.Request) (*http.Response, error)

func (f snapshotRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestOpenBaoSnapshotFailureBoundaries(t *testing.T) {
	attempts, delay := openBaoRetryAttempts, openBaoRetryDelay
	openBaoRetryAttempts, openBaoRetryDelay = 3, 0
	t.Cleanup(func() { openBaoRetryAttempts, openBaoRetryDelay = attempts, delay })
	response := func(status int, body io.Reader) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(body)}, nil
	}
	for _, tc := range []struct {
		name        string
		respond     func(context.CancelFunc) (*http.Response, error)
		wantCalls   int
		wantError   string
		wantIs      error
		existing    bool
		cancelDelay bool
	}{
		{name: "transport retry limit", wantCalls: 3, wantError: "OpenBao request failed",
			respond: func(context.CancelFunc) (*http.Response, error) { return nil, io.EOF }},
		{name: "cancel retry wait", wantCalls: 1, wantIs: context.Canceled, cancelDelay: true,
			respond: func(cancel context.CancelFunc) (*http.Response, error) { cancel(); return nil, io.EOF }},
		{name: "HTTP denial", wantCalls: 1, wantError: "HTTP 403",
			respond: func(context.CancelFunc) (*http.Response, error) {
				return response(http.StatusForbidden, strings.NewReader("denied"))
			}},
		{name: "HTTP server error", wantCalls: 1, wantError: "HTTP 500",
			respond: func(context.CancelFunc) (*http.Response, error) {
				return response(http.StatusInternalServerError, strings.NewReader("failed"))
			}},
		{name: "partial download", wantCalls: 1, wantError: "raft snapshot download failed",
			respond: func(context.CancelFunc) (*http.Response, error) {
				body := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(io.ErrUnexpectedEOF))
				return response(http.StatusOK, body)
			}},
		{name: "existing destination", wantCalls: 1, wantIs: os.ErrExist, existing: true,
			respond: func(context.CancelFunc) (*http.Response, error) {
				return response(http.StatusOK, strings.NewReader("replacement"))
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			openBaoRetryDelay = 0
			if tc.cancelDelay {
				openBaoRetryDelay = time.Hour // Only cancellation can end this wait during the test.
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			bao := &OpenBao{base: &url.URL{Scheme: "https", Host: "example.test"}, token: "admin",
				stream: &http.Client{Transport: snapshotRoundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return tc.respond(cancel)
				})}}
			path := filepath.Join(t.TempDir(), "snapshot")
			if tc.existing {
				require.NoError(t, os.WriteFile(path, []byte("original"), 0o600))
			}
			err := bao.Snapshot(ctx, path)
			if tc.wantIs != nil {
				require.ErrorIs(t, err, tc.wantIs)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
			require.Equal(t, tc.wantCalls, calls)
			if tc.existing {
				data, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				require.Equal(t, "original", string(data))
			} else {
				_, statErr := os.Stat(path)
				require.ErrorIs(t, statErr, os.ErrNotExist, "failed downloads must leave no snapshot file")
			}
		})
	}
}
