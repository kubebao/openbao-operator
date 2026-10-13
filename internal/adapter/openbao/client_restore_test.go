package openbao

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	portopenbao "github.com/dc-tec/openbao-operator/internal/port/openbao"
)

const restoreTestSnapshot = "encrypted-raft-snapshot"

func TestClientRestoreRejectsRedirects(t *testing.T) {
	t.Parallel()

	for _, force := range []bool{false, true} {
		for _, statusCode := range []int{301, 302, 303, 307, 308} {
			t.Run(fmt.Sprintf("force=%t/status=%d", force, statusCode), func(t *testing.T) {
				t.Parallel()

				var submitted, redirected atomic.Int32
				destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					redirected.Add(1)
					w.WriteHeader(http.StatusNoContent)
				}))
				t.Cleanup(destination.Close)

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					submitted.Add(1)
					assertRestoreRequest(t, r, force)
					w.Header().Set("Location", destination.URL+r.URL.Path)
					w.WriteHeader(statusCode)
				}))
				t.Cleanup(server.Close)

				client, err := NewClient(portopenbao.ClientConfig{BaseURL: server.URL, Token: "restore-token"})
				require.NoError(t, err)
				// bytes.Reader normally gives net/http a replayable body for 307/308.
				err = client.Restore(t.Context(), bytes.NewReader([]byte(restoreTestSnapshot)),
					portopenbao.RestoreOptions{Force: force})

				assert.True(t, portopenbao.IsStatus(err, statusCode), "expected redirect rejection, got %v", err)
				assert.Equal(t, int32(1), submitted.Load())
				assert.Zero(t, redirected.Load(), "redirect must not receive snapshot bytes or credentials")
			})
		}
	}
}

func TestClientRestoreDoesNotRetryResponses(t *testing.T) {
	t.Parallel()

	for _, force := range []bool{false, true} {
		for _, statusCode := range []int{200, 204, 400, 403, 429, 500, 503} {
			t.Run(fmt.Sprintf("force=%t/status=%d", force, statusCode), func(t *testing.T) {
				t.Parallel()

				var submitted atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					submitted.Add(1)
					assertRestoreRequest(t, r, force)
					w.WriteHeader(statusCode)
				}))
				t.Cleanup(server.Close)

				client, err := NewClient(portopenbao.ClientConfig{BaseURL: server.URL, Token: "restore-token"})
				require.NoError(t, err)
				err = client.Restore(t.Context(), strings.NewReader(restoreTestSnapshot),
					portopenbao.RestoreOptions{Force: force})
				if statusCode == http.StatusOK || statusCode == http.StatusNoContent {
					assert.NoError(t, err)
				} else {
					assert.True(t, portopenbao.IsStatus(err, statusCode), "expected HTTP error, got %v", err)
				}
				assert.Equal(t, int32(1), submitted.Load())
			})
		}
	}
}

func TestClientRestoreDoesNotRetryLostResponse(t *testing.T) {
	t.Parallel()

	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%t", force), func(t *testing.T) {
			t.Parallel()

			var submitted atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				submitted.Add(1)
				assertRestoreRequest(t, r, force)
				conn, _, err := http.NewResponseController(w).Hijack()
				if !assert.NoError(t, err) {
					return
				}
				assert.NoError(t, conn.Close())
			}))
			t.Cleanup(server.Close)

			client, err := NewClient(portopenbao.ClientConfig{BaseURL: server.URL, Token: "restore-token"})
			require.NoError(t, err)
			err = client.Restore(t.Context(), strings.NewReader(restoreTestSnapshot),
				portopenbao.RestoreOptions{Force: force})
			assert.Error(t, err, "a lost response cannot establish acceptance")
			assert.Equal(t, int32(1), submitted.Load())
		})
	}
}

func TestClientRestoreDoesNotRetryAfterCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	var submitted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		submitted.Add(1)
		assertRestoreRequest(t, r, false)
		cancel()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(portopenbao.ClientConfig{BaseURL: server.URL, Token: "restore-token"})
	require.NoError(t, err)
	err = client.Restore(ctx, strings.NewReader(restoreTestSnapshot), portopenbao.RestoreOptions{})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(1), submitted.Load())
}

func TestClientRestoreDisablesBodyReplay(t *testing.T) {
	t.Parallel()

	client, err := NewClient(portopenbao.ClientConfig{BaseURL: "https://openbao.invalid", Token: "restore-token"})
	require.NoError(t, err)
	client.httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		assert.Nil(t, req.GetBody, "restore must not offer the transport a replayable body")
		assert.Equal(t, int64(len(restoreTestSnapshot)), req.ContentLength)
		assertRestoreRequest(t, req, false)
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})
	err = client.Restore(t.Context(), strings.NewReader(restoreTestSnapshot), portopenbao.RestoreOptions{})
	assert.NoError(t, err)
}

func assertRestoreRequest(t *testing.T, req *http.Request, force bool) {
	t.Helper()

	path := apiPathRaftSnapshot
	if force {
		path = apiPathRaftSnapshotForceRestore
	}
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, path, req.URL.Path)
	assert.Equal(t, "restore-token", req.Header.Get("X-Vault-Token"))
	assert.Equal(t, "application/octet-stream", req.Header.Get("Content-Type"))
	assert.Equal(t, "true", req.Header.Get("X-Vault-No-Request-Forwarding"))
	body, err := io.ReadAll(req.Body)
	assert.NoError(t, err)
	assert.Equal(t, restoreTestSnapshot, string(body))
}
