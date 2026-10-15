package storage

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type storageEndpointRequestResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type storageEndpointRequestGuard struct {
	resolver    storageEndpointRequestResolver
	dialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

var (
	storageAmbiguousNumericHostPattern = regexp.MustCompile(`(?i)^(0x[0-9a-f]+|[0-9]+)(\.(0x[0-9a-f]+|[0-9]+)){0,3}$`)
	// These cloud identity endpoints use ULA addresses rather than IPv6 link-local addresses.
	// https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/configuring-IMDS-new-instances.html
	// https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html
	// https://docs.cloud.google.com/compute/docs/metadata/overview
	awsInstanceMetadataIPv6    = netip.MustParseAddr("fd00:ec2::254")
	awsPodIdentityIPv6         = netip.MustParseAddr("fd00:ec2::23")
	googleInstanceMetadataIPv6 = netip.MustParseAddr("fd20:ce::254")
)

func applyStorageEndpointRequestGuard(client *http.Client, transport *http.Transport, enabled bool) {
	if !enabled {
		return
	}
	guard := newStorageEndpointRequestGuard()
	transport.DialContext = guard.guardedDialContext
	client.CheckRedirect = guard.checkRedirect
}

func newStorageEndpointRequestGuard() storageEndpointRequestGuard {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return storageEndpointRequestGuard{
		resolver:    net.DefaultResolver,
		dialContext: dialer.DialContext,
	}
}

func (g storageEndpointRequestGuard) guardedDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("storage request destination %q is invalid: %w", address, err)
	}

	addrs, err := g.resolveAllowedAddrs(ctx, network, host)
	if err != nil {
		return nil, err
	}

	dialErrs := make([]error, 0, len(addrs))
	for _, addr := range addrs {
		if !networkAllowsEndpointAddress(network, addr) {
			continue
		}
		conn, err := g.dialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		dialErrs = append(dialErrs, err)
	}

	if len(dialErrs) == 0 {
		return nil, fmt.Errorf("storage request destination %q has no addresses compatible with network %q", address, network)
	}
	return nil, fmt.Errorf("storage request destination %q could not be reached: %w", address, errors.Join(dialErrs...))
}

func (g storageEndpointRequestGuard) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return g.validateURL(req.Context(), req.URL)
}

func (g storageEndpointRequestGuard) validateURL(ctx context.Context, u *url.URL) error {
	if u == nil {
		return fmt.Errorf("storage request redirect target is missing")
	}
	host := normalizeStorageEndpointRequestHost(u.Hostname())
	if host == "" {
		return fmt.Errorf("storage request redirect target must include a host")
	}
	_, err := g.resolveAllowedAddrs(ctx, "ip", host)
	return err
}

func (g storageEndpointRequestGuard) resolveAllowedAddrs(ctx context.Context, network, host string) ([]netip.Addr, error) {
	host = normalizeStorageEndpointRequestHost(host)
	if host == "" {
		return nil, fmt.Errorf("storage request destination must include a host")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, fmt.Errorf("storage request destination host %q is not allowed", host)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if isForbiddenStorageEndpointRequestAddress(addr) {
			return nil, fmt.Errorf("storage request destination host %q is not allowed", host)
		}
		return []netip.Addr{addr}, nil
	}
	if storageAmbiguousNumericHostPattern.MatchString(host) {
		return nil, fmt.Errorf("storage request destination host %q uses ambiguous numeric IP encoding", host)
	}
	if strings.Contains(host, ":") {
		return nil, fmt.Errorf("storage request destination host %q uses ambiguous IP encoding", host)
	}
	if g.resolver == nil {
		return nil, fmt.Errorf("storage request destination resolver is required")
	}

	addrs, err := g.resolver.LookupNetIP(ctx, resolverNetworkForEndpointDial(network), host)
	if err != nil {
		return nil, fmt.Errorf("storage request destination host %q could not be resolved: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("storage request destination host %q did not resolve to any IP addresses", host)
	}
	for _, addr := range addrs {
		if isForbiddenStorageEndpointRequestAddress(addr) {
			return nil, fmt.Errorf("storage request destination host %q resolves to forbidden address %s", host, addr)
		}
	}
	return addrs, nil
}

func normalizeStorageEndpointRequestHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	return host
}

func isForbiddenStorageEndpointRequestAddress(addr netip.Addr) bool {
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

func networkAllowsEndpointAddress(network string, addr netip.Addr) bool {
	addr = addr.Unmap()
	switch {
	case strings.HasSuffix(network, "4"):
		return addr.Is4()
	case strings.HasSuffix(network, "6"):
		return addr.Is6()
	default:
		return true
	}
}

func resolverNetworkForEndpointDial(network string) string {
	switch {
	case strings.HasSuffix(network, "4"):
		return "ip4"
	case strings.HasSuffix(network, "6"):
		return "ip6"
	default:
		return "ip"
	}
}
