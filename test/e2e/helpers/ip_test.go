package helpers

import "testing"

func TestHostCIDR(t *testing.T) {
	for address, want := range map[string]string{
		"192.0.2.1":        "192.0.2.1/32",
		"2001:db8::1":      "2001:db8::1/128",
		"::ffff:192.0.2.1": "192.0.2.1/32",
	} {
		if got := HostCIDR(address); got != want {
			t.Errorf("HostCIDR(%q) = %q, want %q", address, got, want)
		}
	}
}
