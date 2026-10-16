package kubernetestls

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/rest"
)

func TestKubernetesHandshakePolicy(t *testing.T) {
	strong, strongCA := serverCertificate(t, 2048)
	weak, weakCA := serverCertificate(t, 1024)
	for _, tt := range []struct {
		name        string
		version     uint16
		cipher      uint16
		certificate tls.Certificate
		ca          []byte
		bad         bool
	}{
		{name: "TLS 1.2 AEAD", version: tls.VersionTLS12,
			cipher: tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, certificate: strong, ca: strongCA},
		{name: "TLS 1.3", version: tls.VersionTLS13, certificate: strong, ca: strongCA},
		{name: "CBC-only", version: tls.VersionTLS12,
			cipher: tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA, certificate: strong, ca: strongCA, bad: true},
		{name: "weak trusted peer", version: tls.VersionTLS13, certificate: weak, ca: weakCA, bad: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("Kubernetes bearer authentication was not preserved")
				}
				w.WriteHeader(http.StatusOK)
			}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{tt.certificate},
				MinVersion: tt.version, MaxVersion: tt.version}
			if tt.cipher != 0 {
				server.TLS.CipherSuites = []uint16{tt.cipher}
			}
			server.StartTLS()
			defer server.Close()
			config := &rest.Config{Host: server.URL, BearerToken: "test-token",
				TLSClientConfig: rest.TLSClientConfig{CAData: tt.ca}, Timeout: 5 * time.Second}
			client, err := rest.HTTPClientFor(WithPolicy(config))
			require.NoError(t, err)
			defer utilnet.CloseIdleConnectionsFor(client.Transport)
			response, err := client.Get(server.URL)
			if tt.bad {
				require.Error(t, err)
				require.Zero(t, requests.Load(), "credentials must not reach a rejected TLS peer")
				return
			}
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, int32(1), requests.Load())
		})
	}
}

func TestKubernetesPolicyDoesNotMutateCachedTransport(t *testing.T) {
	_, ca := serverCertificate(t, 2048)
	config := &rest.Config{Host: "https://api.example.test", TLSClientConfig: rest.TLSClientConfig{CAData: ca}}
	original, err := rest.TransportFor(config)
	require.NoError(t, err)
	first, err := rest.TransportFor(WithPolicy(config))
	require.NoError(t, err)
	second, err := rest.TransportFor(WithPolicy(config))
	require.NoError(t, err)
	originalTLS, err := utilnet.TLSClientConfig(original)
	require.NoError(t, err)
	firstTLS, err := utilnet.TLSClientConfig(first)
	require.NoError(t, err)
	secondTLS, err := utilnet.TLSClientConfig(second)
	require.NoError(t, err)
	require.NotSame(t, originalTLS, firstTLS)
	require.NotSame(t, firstTLS, secondTLS)
	require.Empty(t, originalTLS.CipherSuites)
	require.Nil(t, originalTLS.VerifyConnection)
	require.NotEmpty(t, firstTLS.CipherSuites)
	require.NotNil(t, firstTLS.VerifyConnection)
	require.Nil(t, config.Dial, "the caller's REST config must remain unchanged")
}

func TestKubernetesClientCertificatePolicy(t *testing.T) {
	serverCert, serverCA := serverCertificate(t, 2048)
	for _, bits := range []int{1024, 2048} {
		t.Run(strconv.Itoa(bits), func(t *testing.T) {
			clientCert, clientCA := serverCertificate(t, bits, x509.ExtKeyUsageClientAuth)
			roots := x509.NewCertPool()
			require.True(t, roots.AppendCertsFromPEM(clientCA))
			var requests atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if len(r.TLS.PeerCertificates) == 0 {
					t.Error("Kubernetes client certificate authentication was not preserved")
				}
				w.WriteHeader(http.StatusOK)
			}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}, MinVersion: tls.VersionTLS12,
				ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
			server.StartTLS()
			defer server.Close()
			keyDER, err := x509.MarshalPKCS8PrivateKey(clientCert.PrivateKey)
			require.NoError(t, err)
			config := &rest.Config{Host: server.URL, Timeout: 5 * time.Second,
				TLSClientConfig: rest.TLSClientConfig{CAData: serverCA, CertData: clientCA,
					KeyData: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})}}
			client, err := rest.HTTPClientFor(WithPolicy(config))
			require.NoError(t, err)
			defer utilnet.CloseIdleConnectionsFor(client.Transport)
			response, err := client.Get(server.URL)
			if bits == 1024 {
				require.ErrorContains(t, err, "at least 2048 bits")
				require.Zero(t, requests.Load())
				return
			}
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, int32(1), requests.Load())
		})
	}
}

func serverCertificate(t *testing.T, bits int, usages ...x509.ExtKeyUsage) (tls.Certificate, []byte) {
	t.Helper()
	if len(usages) == 0 {
		usages = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	key, err := rsa.GenerateKey(rand.Reader, bits)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test API server"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: usages}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certificate, err := tls.X509KeyPair(ca,
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	require.NoError(t, err)
	return certificate, ca
}
