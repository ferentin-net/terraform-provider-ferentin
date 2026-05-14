package provider

import "testing"

// TestIsPrivateOrUnresolvableHost mirrors the platform's
// SsrfNetworkValidator's pattern set — every case here represents a host
// the platform's SSRF guard rejects under deployment_mode = "public", so
// the validator on the resource needs to flag the same set at plan time.
//
// Cases the validator MUST flag are tagged `wantPrivate: true`. Cases
// the validator MUST pass through (publicly-resolvable hosts) are
// `wantPrivate: false`. The point of the test is to keep the two sides
// honest about what counts as "needs edge_routed."
func TestIsPrivateOrUnresolvableHost(t *testing.T) {
	cases := []struct {
		host        string
		wantPrivate bool
		why         string
	}{
		// Reserved hostname suffixes — platform's
		// isReservedHostnameSuffix list.
		{"mac.sahuhaus.local", true, "reserved .local suffix"},
		{"my-svc.internal", true, "reserved .internal suffix"},
		{"intranet.corp", true, "reserved .corp suffix"},
		{"deep.nested.host.local", true, "suffix match drills through subdomains"},

		// RFC 2606 reserved test TLDs — don't resolve in the wild.
		{"mcp.salesforce.example.com", true, "RFC 2606 .example.com"},
		{"foo.example.org", true, "RFC 2606 .example.org"},
		{"bar.example.net", true, "RFC 2606 .example.net"},
		{"whatever.invalid", true, "RFC 2606 .invalid"},

		// IP literals — RFC1918 / loopback / link-local.
		{"10.0.0.5", true, "RFC1918 / 10.0.0.0/8"},
		{"172.20.5.6", true, "RFC1918 / 172.16.0.0/12"},
		{"192.168.1.100", true, "RFC1918 / 192.168.0.0/16"},
		{"127.0.0.1", true, "IPv4 loopback"},
		{"::1", true, "IPv6 loopback"},
		{"169.254.169.254", true, "link-local (AWS/Azure metadata IP)"},
		{"0.0.0.0", true, "unspecified address"},

		// Bare hostnames the platform's BLOCKED_HOSTS set covers.
		{"localhost", true, "bare localhost"},
		{"localhost.localdomain", true, "localhost variant"},

		// Publicly-resolvable hosts — must pass through.
		{"api.salesforce.com", false, "real SaaS host"},
		{"mcp.example.io", false, ".io is not reserved"},
		{"github.com", false, "real public host"},
		{"8.8.8.8", false, "public IPv4 (Google DNS)"},
		{"2001:4860:4860::8888", false, "public IPv6"},

		// Edge cases.
		{"", false, "empty host returns false (caller pre-filters)"},
		{"  SOME.LOCAL  ", true, "case-insensitive + leading/trailing whitespace"},
	}

	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			got := isPrivateOrUnresolvableHost(tc.host)
			if got != tc.wantPrivate {
				t.Errorf("isPrivateOrUnresolvableHost(%q) = %v, want %v (%s)",
					tc.host, got, tc.wantPrivate, tc.why)
			}
		})
	}
}
