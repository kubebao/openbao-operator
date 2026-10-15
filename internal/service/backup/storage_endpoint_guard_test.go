package backup

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kubebao/openbao-operator/internal/adapter/storage"
)

func TestMain(m *testing.M) {
	defaultStorageEndpointResolver = permissiveEndpointResolver{}
	os.Exit(m.Run())
}

type permissiveEndpointResolver struct{}

func (permissiveEndpointResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
}

type fakeEndpointResolver map[string][]netip.Addr

func (r fakeEndpointResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	if addrs, ok := r[host]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{
		Err:         "no such host",
		Name:        host,
		IsNotFound:  true,
		IsTemporary: false,
	}
}

func TestValidateStorageEndpointAccessRejectsUnsafeEndpointHosts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		endpoint string
		wantErr  string
	}{
		{
			name:     "userinfo before link local host",
			endpoint: "https://s3.example.test@169.254.169.254/latest/meta-data",
			wantErr:  "not allowed",
		},
		{
			name:     "ipv4 mapped link local",
			endpoint: "https://[::ffff:169.254.169.254]/latest/meta-data",
			wantErr:  "not allowed",
		},
		{
			name:     "AWS IPv6 metadata",
			endpoint: "http://[fd00:ec2::254]/latest/meta-data",
			wantErr:  "not allowed",
		},
		{
			name:     "AWS IPv6 Pod Identity",
			endpoint: "http://[fd00:ec2::23]/v1/credentials",
			wantErr:  "not allowed",
		},
		{
			name:     "Google IPv6 metadata",
			endpoint: "http://[fd20:ce::254]/computeMetadata/v1",
			wantErr:  "not allowed",
		},
		{
			name:     "scoped expanded AWS IPv6 metadata",
			endpoint: "http://[fd00:0ec2:0000:0000:0000:0000:0000:0254%25eth0]/latest/meta-data",
			wantErr:  "not allowed",
		},
		{
			name:     "single integer loopback",
			endpoint: "http://2130706433",
			wantErr:  "ambiguous numeric IP encoding",
		},
		{
			name:     "hex loopback",
			endpoint: "http://0x7f000001",
			wantErr:  "ambiguous numeric IP encoding",
		},
		{
			name:     "localhost name",
			endpoint: "http://localhost:9000",
			wantErr:  "not allowed",
		},
		{
			name:     "subdomain localhost name",
			endpoint: "http://object.localhost:9000",
			wantErr:  "not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateStorageEndpointAccessWithResolver(
				context.Background(),
				storage.Config{Endpoint: tt.endpoint},
				fakeEndpointResolver{},
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateStorageEndpointAccessChecksDNSAnswers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		endpoint string
		addrs    []netip.Addr
		wantErr  string
	}{
		{
			name:     "dns resolves to link local",
			endpoint: "https://object-store.example.test",
			addrs:    []netip.Addr{netip.MustParseAddr("169.254.169.254")},
			wantErr:  "resolves to forbidden address",
		},
		{
			name:     "dns resolves to loopback",
			endpoint: "https://object-store.example.test",
			addrs:    []netip.Addr{netip.MustParseAddr("::1")},
			wantErr:  "resolves to forbidden address",
		},
		{
			name:     "dns resolves to AWS IPv6 metadata",
			endpoint: "https://object-store.example.test",
			addrs:    []netip.Addr{netip.MustParseAddr("fd00:ec2::254")},
			wantErr:  "resolves to forbidden address",
		},
		{
			name:     "mixed DNS includes AWS IPv6 Pod Identity",
			endpoint: "https://object-store.example.test",
			addrs:    []netip.Addr{netip.MustParseAddr("203.0.113.10"), netip.MustParseAddr("fd00:ec2::23")},
			wantErr:  "resolves to forbidden address",
		},
		{
			name:     "mixed DNS includes Google IPv6 metadata",
			endpoint: "https://object-store.example.test",
			addrs:    []netip.Addr{netip.MustParseAddr("203.0.113.10"), netip.MustParseAddr("fd20:ce::254")},
			wantErr:  "resolves to forbidden address",
		},
		{
			name:     "other literal in Google ULA prefix",
			endpoint: "http://[fd20:ce::55]:9000",
		},
		{
			name:     "dns resolves to private IPv6 cluster address",
			endpoint: "http://minio.team-a.svc.cluster.local:9000",
			addrs:    []netip.Addr{netip.MustParseAddr("fd00::123")},
		},
		{
			name:     "other literal in AWS ULA prefix",
			endpoint: "http://[fd00:ec2::55]:9000",
		},
		{
			name:     "dns resolves to public address",
			endpoint: "https://object-store.example.test",
			addrs:    []netip.Addr{netip.MustParseAddr("203.0.113.10")},
		},
		{
			name:     "dns resolves to private in cluster address",
			endpoint: "http://minio.team-a.svc.cluster.local:9000",
			addrs:    []netip.Addr{netip.MustParseAddr("10.96.12.34")},
		},
		{
			name:     "private literal endpoint",
			endpoint: "https://10.0.0.15:9000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolver := fakeEndpointResolver{}
			if len(tt.addrs) > 0 {
				parsed, err := urlHost(tt.endpoint)
				require.NoError(t, err)
				resolver[parsed] = tt.addrs
			}

			err := validateStorageEndpointAccessWithResolver(
				context.Background(),
				storage.Config{Endpoint: tt.endpoint},
				resolver,
			)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateStorageEndpointAccessChecksS3VirtualHostedEndpoint(t *testing.T) {
	t.Parallel()

	resolver := fakeEndpointResolver{
		"storage.example.test":               []netip.Addr{netip.MustParseAddr("203.0.113.10")},
		"tenant-bucket.storage.example.test": []netip.Addr{netip.MustParseAddr("169.254.169.254")},
	}

	err := validateStorageEndpointAccessWithResolver(
		context.Background(),
		storage.Config{
			Provider: storage.ProviderS3,
			Bucket:   "tenant-bucket",
			Endpoint: "https://storage.example.test",
			S3:       &storage.S3Options{},
		},
		resolver,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "s3 virtual-hosted endpoint")
	assert.Contains(t, err.Error(), "resolves to forbidden address")

	err = validateStorageEndpointAccessWithResolver(
		context.Background(),
		storage.Config{
			Provider: storage.ProviderS3,
			Bucket:   "tenant-bucket",
			Endpoint: "https://storage.example.test",
			S3: &storage.S3Options{
				UsePathStyle: true,
			},
		},
		resolver,
	)
	require.NoError(t, err)
}

func TestValidateStorageEndpointAccessChecksAzureConnectionStringEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		connectionString string
		resolver         fakeEndpointResolver
		wantErr          string
	}{
		{
			name:             "blob endpoint link local literal",
			connectionString: "DefaultEndpointsProtocol=http;AccountName=tenant;AccountKey=key;BlobEndpoint=http://169.254.169.254/devstoreaccount1",
			wantErr:          "azure connection string BlobEndpoint",
		},
		{
			name:             "blob endpoint dns resolves to link local",
			connectionString: "DefaultEndpointsProtocol=https;AccountName=tenant;AccountKey=key;BlobEndpoint=https://blob.example.test/devstoreaccount1",
			resolver: fakeEndpointResolver{
				"blob.example.test": []netip.Addr{netip.MustParseAddr("169.254.169.254")},
			},
			wantErr: "resolves to forbidden address",
		},
		{
			name:             "custom suffix dns resolves to link local",
			connectionString: "DefaultEndpointsProtocol=https;AccountName=tenant;AccountKey=key;EndpointSuffix=metadata.example.test",
			resolver: fakeEndpointResolver{
				"tenant.blob.metadata.example.test": []netip.Addr{netip.MustParseAddr("169.254.169.254")},
			},
			wantErr: "azure connection string EndpointSuffix",
		},
		{
			name:             "blob endpoint dns resolves to public address",
			connectionString: "DefaultEndpointsProtocol=https;AccountName=tenant;AccountKey=key;BlobEndpoint=https://blob.example.test/devstoreaccount1",
			resolver: fakeEndpointResolver{
				"blob.example.test": []netip.Addr{netip.MustParseAddr("203.0.113.10")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateStorageEndpointAccessWithResolver(
				context.Background(),
				storage.Config{
					Azure: &storage.AzureOptions{
						ConnectionString: tt.connectionString,
					},
				},
				tt.resolver,
			)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func urlHost(endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	return normalizeEndpointHost(parsed.Hostname()), nil
}
