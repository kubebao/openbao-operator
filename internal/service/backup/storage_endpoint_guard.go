package backup

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"

	"github.com/kubebao/openbao-operator/internal/adapter/storage"
)

type endpointResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

var defaultStorageEndpointResolver endpointResolver = net.DefaultResolver

var (
	ambiguousNumericHostPattern = regexp.MustCompile(`(?i)^(0x[0-9a-f]+|[0-9]+)(\.(0x[0-9a-f]+|[0-9]+)){0,3}$`)
	// These cloud identity endpoints use ULA addresses rather than IPv6 link-local addresses.
	// https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/configuring-IMDS-new-instances.html
	// https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html
	// https://docs.cloud.google.com/compute/docs/metadata/overview
	awsInstanceMetadataIPv6    = netip.MustParseAddr("fd00:ec2::254")
	awsPodIdentityIPv6         = netip.MustParseAddr("fd00:ec2::23")
	googleInstanceMetadataIPv6 = netip.MustParseAddr("fd20:ce::254")
)

// ValidateStorageEndpointAccess rejects storage endpoints that resolve to local or metadata-adjacent addresses.
func ValidateStorageEndpointAccess(ctx context.Context, cfg storage.Config) error {
	return validateStorageEndpointAccessWithResolver(ctx, cfg, defaultStorageEndpointResolver)
}

func validateStorageEndpointAccessWithResolver(ctx context.Context, cfg storage.Config, resolver endpointResolver) error {
	if err := validateEndpointURLAccess(ctx, resolver, "storage endpoint", cfg.Endpoint); err != nil {
		return err
	}
	if s3VirtualHostedEndpointShouldBeChecked(cfg) {
		endpoint, err := s3VirtualHostedEndpoint(cfg.Endpoint, cfg.Bucket)
		if err != nil {
			return err
		}
		if err := validateEndpointURLAccess(ctx, resolver, "s3 virtual-hosted endpoint", endpoint); err != nil {
			return err
		}
	}

	if cfg.Azure != nil {
		for _, endpoint := range azureConnectionStringEndpointCandidates(cfg.Azure.ConnectionString) {
			if err := validateEndpointURLAccess(ctx, resolver, endpoint.description, endpoint.url); err != nil {
				return err
			}
		}
	}

	return nil
}

func s3VirtualHostedEndpointShouldBeChecked(cfg storage.Config) bool {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return false
	}
	if cfg.Provider != "" && cfg.Provider != storage.ProviderS3 {
		return false
	}
	if cfg.S3 != nil && cfg.S3.UsePathStyle {
		return false
	}
	return true
}

func s3VirtualHostedEndpoint(endpoint, bucket string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if err != nil {
			return "", fmt.Errorf("storage endpoint must be an absolute URL with host: %w", err)
		}
		return "", fmt.Errorf("storage endpoint must be an absolute URL with host")
	}

	host := normalizeEndpointHost(parsed.Hostname())
	if host == "" || net.ParseIP(host) != nil || strings.Contains(host, ":") {
		return "", nil
	}

	parsed.User = nil
	virtualHost := bucket + "." + host
	if port := parsed.Port(); port != "" {
		virtualHost = net.JoinHostPort(virtualHost, port)
	}
	parsed.Host = virtualHost
	return parsed.String(), nil
}

func validateEndpointURLAccess(ctx context.Context, resolver endpointResolver, description, rawEndpoint string) error {
	rawEndpoint = strings.TrimSpace(rawEndpoint)
	if rawEndpoint == "" {
		return nil
	}

	parsed, err := url.Parse(rawEndpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if err != nil {
			return fmt.Errorf("%s must be an absolute URL with host: %w", description, err)
		}
		return fmt.Errorf("%s must be an absolute URL with host", description)
	}

	host := normalizeEndpointHost(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("%s must include a host", description)
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("%s host %q is not allowed", description, host)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if isForbiddenEndpointAddress(addr) {
			return fmt.Errorf("%s host %q is not allowed", description, host)
		}
		return nil
	}
	if ambiguousNumericHostPattern.MatchString(host) {
		return fmt.Errorf("%s host %q uses ambiguous numeric IP encoding", description, host)
	}
	if strings.Contains(host, ":") {
		return fmt.Errorf("%s host %q uses ambiguous IP encoding", description, host)
	}

	if resolver == nil {
		return fmt.Errorf("%s resolver is required", description)
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("%s host %q could not be resolved: %w", description, host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%s host %q did not resolve to any IP addresses", description, host)
	}
	for _, addr := range addrs {
		if isForbiddenEndpointAddress(addr) {
			return fmt.Errorf("%s host %q resolves to forbidden address %s", description, host, addr)
		}
	}

	return nil
}

func normalizeEndpointHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	return host
}

func isForbiddenEndpointAddress(addr netip.Addr) bool {
	addr = addr.Unmap().WithZone("")
	return !addr.IsValid() ||
		addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsUnspecified() ||
		addr.IsMulticast() ||
		addr == awsInstanceMetadataIPv6 ||
		addr == awsPodIdentityIPv6 ||
		addr == googleInstanceMetadataIPv6
}

type storageEndpointCandidate struct {
	description string
	url         string
}

func azureConnectionStringEndpointCandidates(connectionString string) []storageEndpointCandidate {
	connectionString = strings.TrimSpace(connectionString)
	if connectionString == "" {
		return nil
	}

	values := parseAzureConnectionString(connectionString)
	if blobEndpoint := values["blobendpoint"]; blobEndpoint != "" {
		return []storageEndpointCandidate{{
			description: "azure connection string BlobEndpoint",
			url:         blobEndpoint,
		}}
	}

	accountName := values["accountname"]
	endpointSuffix := strings.TrimPrefix(values["endpointsuffix"], ".")
	if accountName == "" || endpointSuffix == "" {
		return nil
	}

	protocol := values["defaultendpointsprotocol"]
	if protocol == "" {
		protocol = "https"
	}

	return []storageEndpointCandidate{{
		description: "azure connection string EndpointSuffix",
		url:         fmt.Sprintf("%s://%s.blob.%s", protocol, accountName, endpointSuffix),
	}}
}

func parseAzureConnectionString(connectionString string) map[string]string {
	values := make(map[string]string)
	for _, part := range strings.Split(connectionString, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return values
}
