package controller

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMetricsTLSCipherPolicy(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	certificates := fixture.TLS.Certificates
	fixture.Close()
	for _, http2 := range []bool{false, true} {
		for _, aead := range []bool{false, true} {
			t.Run("HTTP2="+strconv.FormatBool(http2)+"/AEAD="+strconv.FormatBool(aead), func(t *testing.T) {
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				server.EnableHTTP2 = true
				server.TLS = &tls.Config{Certificates: certificates, MinVersion: tls.VersionTLS12,
					NextProtos: []string{"h2", "http/1.1"}}
				options := buildMetricsServerOptions(runConfig{secureMetrics: true, enableHTTP2: http2})
				for _, option := range options.TLSOpts {
					option(server.TLS)
				}
				server.StartTLS()
				defer server.Close()
				transport := server.Client().Transport.(*http.Transport).Clone()
				defer transport.CloseIdleConnections()
				transport.ForceAttemptHTTP2 = true
				transport.TLSClientConfig.MinVersion = tls.VersionTLS12
				transport.TLSClientConfig.MaxVersion = tls.VersionTLS12
				cipher := tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA
				if aead {
					cipher = tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
				}
				transport.TLSClientConfig.CipherSuites = []uint16{cipher}
				client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
				response, err := client.Get(server.URL)
				if !aead {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.Equal(t, cipher, response.TLS.CipherSuite)
				if http2 {
					require.Equal(t, 2, response.ProtoMajor)
				} else {
					require.Equal(t, 1, response.ProtoMajor)
				}
			})
		}
	}
}
