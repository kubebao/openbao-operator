package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const fixtureLeaderPath = "/v1/sys/leader"

func fixtureTestClient(t *testing.T, respond func(string) (int, string)) *OpenBao {
	t.Helper()
	return &OpenBao{base: &url.URL{Scheme: "https", Host: "fixture.test"}, token: "fixture",
		http: &http.Client{Transport: snapshotRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodGet, request.Method, "data verification must never replay a write")
			status, body := respond(request.URL.Path)
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}}
}

func TestFixtureDataRequiresLeaderValueAndStandbyConvergence(t *testing.T) {
	const value = `{"data":{"data":{"value":"acknowledged"}}}`
	leaderReads, standbyReads := 0, 0
	leader := fixtureTestClient(t, func(path string) (int, string) {
		if path == fixtureLeaderPath {
			return 200, `{"is_self":true}`
		}
		leaderReads++
		return 200, value
	})
	standby := fixtureTestClient(t, func(path string) (int, string) {
		if path == fixtureLeaderPath {
			return 200, `{"is_self":false}`
		}
		standbyReads++
		if standbyReads == 1 {
			return 404, `{}`
		}
		return 200, value
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, verifyFixtureData(ctx, standby, []*OpenBao{standby, leader}, "migration-data/data/unique", "acknowledged", time.Millisecond))
	require.Equal(t, 1, leaderReads)
	require.Equal(t, 2, standbyReads)
}

func TestFixtureDataPreservesFailures(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		leaderStatus, selectedStatus   int
		leaderBody, selectedBody, want string
	}{
		{name: "leader missing", leaderStatus: 404, selectedStatus: 200, want: "on leader: OpenBao returned HTTP 404"},
		{name: "leader wrong value", leaderStatus: 200, leaderBody: `{"data":{"data":{"value":"old"}}}`, selectedStatus: 200, want: "leader: fixture value does not match"},
		{name: "standby wrong value", leaderStatus: 200, selectedStatus: 200, selectedBody: `{"data":{"data":{"value":"old"}}}`, want: "fixture value does not match"},
		{name: "standby denial", leaderStatus: 200, selectedStatus: 403, want: "HTTP 403"},
		{name: "standby server error", leaderStatus: 200, selectedStatus: 500, want: "HTTP 500"},
		{name: "standby never converges", leaderStatus: 200, selectedStatus: 404, want: "context deadline exceeded"},
		{name: "malformed value", leaderStatus: 200, selectedStatus: 200, selectedBody: `{`, want: "decode fixture value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const value = `{"data":{"data":{"value":"acknowledged"}}}`
			leader := fixtureTestClient(t, func(path string) (int, string) {
				if path == fixtureLeaderPath {
					return 200, `{"is_self":true}`
				}
				if tc.leaderBody != "" {
					return tc.leaderStatus, tc.leaderBody
				}
				return tc.leaderStatus, value
			})
			selected := fixtureTestClient(t, func(string) (int, string) {
				if tc.selectedBody != "" {
					return tc.selectedStatus, tc.selectedBody
				}
				return tc.selectedStatus, value
			})
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			err := verifyFixtureData(ctx, selected, []*OpenBao{leader}, "migration-data/data/unique", "acknowledged", time.Millisecond)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestFixtureWriteNeverRetriesAmbiguousTransportFailure(t *testing.T) {
	calls := 0
	bao := &OpenBao{base: &url.URL{Scheme: "https", Host: "fixture.test"}, token: "fixture",
		http: &http.Client{Transport: snapshotRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodPost, request.Method)
			calls++
			return nil, io.ErrUnexpectedEOF
		})}}
	_, err := fixtureRequest(t.Context(), bao, http.MethodPost, "migration-data/data/unique", map[string]any{"data": map[string]string{"value": "once"}}, time.Millisecond)
	require.ErrorIs(t, err, errOpenBaoTransport)
	require.Equal(t, 1, calls)
}
