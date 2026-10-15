package helpers

import "net/netip"

// HostCIDR returns a single-host network for a Kubernetes-assigned IP address.
// Invalid fixture addresses panic so they cannot silently broaden a test policy.
func HostCIDR(address string) string {
	ip := netip.MustParseAddr(address).Unmap()
	return netip.PrefixFrom(ip, ip.BitLen()).String()
}
