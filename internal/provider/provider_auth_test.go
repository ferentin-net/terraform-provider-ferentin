package provider

import (
	"strings"
	"testing"
)

// The derived auth_url is only ever used by the client_credentials grant, and
// the platform routes those mints PER TENANT. A derivation that stops at
// `https://auth.<domain>` posts to the global /token and the authorization
// server rejects it with "Tenant could not be determined. Use a tenant-specific
// endpoint for this grant type." — so the default was guaranteed to fail for
// the one auth mode it exists to serve. Found by the first live acceptance run.
func TestDeriveAuthURL(t *testing.T) {
	const tenant = "e3dab301-3c99-4654-9043-8cda1fef8190"

	for _, tc := range []struct {
		name     string
		endpoint string
		tenantID string
		want     string
	}{
		{
			name:     "production",
			endpoint: "https://api.ferentin.net",
			tenantID: tenant,
			want:     "https://auth.ferentin.net/tenant/" + tenant,
		},
		{
			name:     "local dev",
			endpoint: "https://api.local.ferentin.test",
			tenantID: tenant,
			want:     "https://auth.local.ferentin.test/tenant/" + tenant,
		},
		{
			// A trailing slash must not produce a doubled separator; the SDK
			// appends "/token" to this value.
			name:     "trailing slash on the endpoint",
			endpoint: "https://api.ferentin.net/",
			tenantID: tenant,
			want:     "https://auth.ferentin.net/tenant/" + tenant,
		},
		{
			name:     "host without the api. prefix is not derivable",
			endpoint: "https://localhost:8443",
			tenantID: tenant,
			want:     "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveAuthURL(tc.endpoint, tc.tenantID); got != tc.want {
				t.Errorf("deriveAuthURL(%q, %q) = %q, want %q",
					tc.endpoint, tc.tenantID, got, tc.want)
			}
		})
	}
}

// The regression itself, stated as its own assertion: whatever else the
// derivation does, the result the SDK appends "/token" to must carry a tenant.
func TestDeriveAuthURL_IsTenantScoped(t *testing.T) {
	got := deriveAuthURL("https://api.ferentin.net", "abc-123")
	if !strings.Contains(got, "/tenant/abc-123") {
		t.Fatalf("derived %q — a URL without a tenant path mints against the global "+
			"token endpoint, which the platform rejects for client_credentials", got)
	}
	if strings.HasSuffix(got, "/") {
		t.Errorf("derived %q ends in a slash; the SDK appends \"/token\" to it", got)
	}
}
