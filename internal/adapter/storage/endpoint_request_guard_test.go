package storage

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStorageEndpointRequestResolver map[string][]netip.Addr

func (r fakeStorageEndpointRequestResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	if addrs, ok := r[host]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{
		Err:        "no such host",
		Name:       host,
		IsNotFound: true,
	}
}

type recordingStorageEndpointRequestResolver struct {
	network string
	addrs   []netip.Addr
}

func (r *recordingStorageEndpointRequestResolver) LookupNetIP(_ context.Context, network, _ string) ([]netip.Addr, error) {
	r.network = network
	return r.addrs, nil
}

func TestStorageEndpointRequestGuardDialsResolvedAddress(t *testing.T) {
	t.Parallel()

	var dialedAddress string
	resolver := &recordingStorageEndpointRequestResolver{
		addrs: []netip.Addr{netip.MustParseAddr("203.0.113.10")},
	}
	guard := storageEndpointRequestGuard{
		resolver: resolver,
		dialContext: func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialedAddress = address
			clientConn, serverConn := net.Pipe()
			t.Cleanup(func() {
				_ = clientConn.Close()
				_ = serverConn.Close()
			})
			return clientConn, nil
		},
	}

	conn, err := guard.guardedDialContext(context.Background(), "tcp4", "storage.example.test:443")
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, "ip4", resolver.network)
	assert.Equal(t, "203.0.113.10:443", dialedAddress)
}

func TestStorageEndpointRequestGuardAllowsPrivateClusterAddress(t *testing.T) {
	t.Parallel()

	var dialedAddress string
	guard := storageEndpointRequestGuard{
		resolver: fakeStorageEndpointRequestResolver{},
		dialContext: func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialedAddress = address
			clientConn, serverConn := net.Pipe()
			t.Cleanup(func() {
				_ = clientConn.Close()
				_ = serverConn.Close()
			})
			return clientConn, nil
		},
	}

	conn, err := guard.guardedDialContext(context.Background(), "tcp", "10.96.12.34:9000")
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, "10.96.12.34:9000", dialedAddress)
}

func TestStorageEndpointRequestGuardRejectsForbiddenDNSAtDialTime(t *testing.T) {
	t.Parallel()

	dialed := false
	guard := storageEndpointRequestGuard{
		resolver: fakeStorageEndpointRequestResolver{
			"storage.example.test": []netip.Addr{netip.MustParseAddr("169.254.169.254")},
		},
		dialContext: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, nil
		},
	}

	conn, err := guard.guardedDialContext(context.Background(), "tcp", "storage.example.test:443")
	require.Error(t, err)
	assert.Nil(t, conn)
	assert.False(t, dialed)
	assert.Contains(t, err.Error(), "resolves to forbidden address")
}

func TestStorageEndpointRequestGuardRejectsRedirectToForbiddenHost(t *testing.T) {
	t.Parallel()

	client, err := buildHTTPClient(nil, false, true)
	require.NoError(t, err)
	require.NotNil(t, client.CheckRedirect)

	req, err := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data", nil)
	require.NoError(t, err)

	err = client.CheckRedirect(req, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
}

func TestStorageEndpointRequestGuardIPv6(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		address string
		network string
		want    string
	}{
		{"private cluster address", "[fd00::123]:9000", "tcp6", "[fd00::123]:9000"},
		{"global address", "[2001:db8::123]:443", "tcp", "[2001:db8::123]:443"},
		{"mapped IPv4", "[::ffff:10.96.12.34]:9000", "tcp4", "10.96.12.34:9000"},
		{"incompatible family", "[fd00::123]:9000", "tcp4", ""},
		{"loopback", "[::1]:9000", "tcp6", ""},
		{"mapped loopback", "[::ffff:127.0.0.1]:9000", "tcp", ""},
		{"link local", "[fe80::123]:9000", "tcp6", ""},
		{"mapped metadata", "[::ffff:169.254.169.254]:9000", "tcp", ""},
		{"AWS metadata", "[fd00:ec2::254]:80", "tcp6", ""},
		{"AWS Pod Identity", "[fd00:ec2::23]:80", "tcp6", ""},
		{"Google metadata", "[fd20:ce::254]:80", "tcp6", ""},
		{"scoped Google metadata", "[fd20:ce::254%eth0]:80", "tcp6", ""},
		{"other private Google-prefix address", "[fd20:ce::55]:9000", "tcp6", "[fd20:ce::55]:9000"},
		{"scoped AWS metadata", "[fd00:ec2::254%eth0]:80", "tcp6", ""},
		{"other private AWS-prefix address", "[fd00:ec2::55]:9000", "tcp6", "[fd00:ec2::55]:9000"},
		{"unspecified", "[::]:9000", "tcp6", ""},
		{"multicast", "[ff02::1]:9000", "tcp6", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var dialed string
			guard := storageEndpointRequestGuard{
				dialContext: func(_ context.Context, _ string, address string) (net.Conn, error) {
					dialed = address
					client, server := net.Pipe()
					t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
					return client, nil
				},
			}
			_, err := guard.guardedDialContext(context.Background(), tt.network, tt.address)
			if tt.want == "" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, dialed)
		})
	}
}

func TestStorageEndpointRequestGuardRejectsIPv6IdentityDNSAndRedirects(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"fd00:ec2::254", "fd00:ec2::23", "fd20:ce::254"} {
		t.Run(address, func(t *testing.T) {
			t.Parallel()
			dialed := false
			guard := storageEndpointRequestGuard{
				resolver: fakeStorageEndpointRequestResolver{
					"storage.example.test": {netip.MustParseAddr("203.0.113.10"), netip.MustParseAddr(address)},
				},
				dialContext: func(context.Context, string, string) (net.Conn, error) {
					dialed = true
					return nil, nil
				},
			}
			_, err := guard.guardedDialContext(context.Background(), "tcp", "storage.example.test:443")
			require.ErrorContains(t, err, "resolves to forbidden address")
			assert.False(t, dialed, "mixed DNS answers must be rejected before any connection")
			for _, target := range []string{"https://storage.example.test", "http://[" + address + "]/credentials"} {
				req, err := http.NewRequest(http.MethodGet, target, nil)
				require.NoError(t, err)
				require.Error(t, guard.checkRedirect(req, nil), target)
			}
		})
	}
}
