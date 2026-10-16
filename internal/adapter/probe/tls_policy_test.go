package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestACMEFallbackRejectsRedirects(t *testing.T) {
	for _, destination := range []string{
		"https://non-loopback.invalid/health",
		"http://non-loopback.invalid/health",
		"/other-local-path",
	} {
		t.Run(destination, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path == "/health" {
					http.Redirect(w, r, destination, http.StatusTemporaryRedirect)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			prober, err := NewProber(ProberConfig{
				Addr: server.URL, ServerName: "acme.example.test", AllowInsecureFallback: true,
				StartupPath: "/health",
			})
			require.NoError(t, err)
			require.Error(t, prober.CheckStartup(context.Background()))
			require.Equal(t, int32(1), requests.Load(), "a redirect must not issue another request")
		})
	}
}

func TestACMEFallbackAllowsLocalAcquisition(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	prober, err := NewProber(ProberConfig{
		Addr: server.URL, ServerName: "acme.example.test", AllowInsecureFallback: true,
		StartupPath: "/health",
	})
	require.NoError(t, err)
	require.NoError(t, prober.CheckStartup(context.Background()))
}

func TestProbeDialGuard(t *testing.T) {
	for _, tt := range []struct {
		address string
		bad     bool
	}{
		{address: "127.0.0.1:8200"},
		{address: "127.0.0.2:8200"},
		{address: "[::1]:8200"},
		{address: "192.0.2.1:8200", bad: true},
		{address: "[2001:db8::1]:8200", bad: true},
		{address: "localhost:8200", bad: true},
		{address: "invalid", bad: true},
	} {
		t.Run(tt.address, func(t *testing.T) {
			err := restrictLoopbackDial(context.Background(), "tcp", tt.address, nil)
			if tt.bad {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestACMEFallbackTransportGuardsActualDial(t *testing.T) {
	client, err := newHTTPClient("", "acme.example.test", time.Second, true)
	require.NoError(t, err)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.DialContext)
	// ControlContext rejects this address before connecting the socket.
	connection, err := transport.DialContext(t.Context(), "tcp", "192.0.2.1:8200")
	require.Nil(t, connection)
	require.ErrorContains(t, err, "requires a loopback dial address")
}
