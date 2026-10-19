package provider

import (
	"strings"
	"testing"
)

func TestTrustOverrideWarning(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	for _, tc := range []struct {
		name string
		goos string
		env  func(string) string
		warn bool
	}{
		{"override on with a CA file", "darwin", env("GODEBUG", "x509sslcertoverrideplatform=1", "SSL_CERT_FILE", "/tmp/ca.pem"), true},
		{"windows too", "windows", env("GODEBUG", "x509sslcertoverrideplatform=1", "SSL_CERT_DIR", `C:\ca`), true},
		// The runtime trims nothing: "=0 " is not 0, " x509…=0" is another key.
		{"trailing space is not 0", "darwin", env("GODEBUG", "x509sslcertoverrideplatform=0 ", "SSL_CERT_FILE", "x"), true},
		{"leading space is another key", "darwin", env("GODEBUG", "x509sslcertoverrideplatform=1, x509sslcertoverrideplatform=0", "SSL_CERT_FILE", "x"), true},
		{"last one wins", "darwin", env("GODEBUG", "x509sslcertoverrideplatform=1,x509sslcertoverrideplatform=0", "SSL_CERT_FILE", "x"), false},
		// A bisect pattern makes the value "" for stacks it does not match.
		{"bisect pattern", "darwin", env("GODEBUG", "x509sslcertoverrideplatform=0#n", "SSL_CERT_FILE", "x"), true},
		{"bisect pattern that may match", "darwin", env("GODEBUG", "x509sslcertoverrideplatform=0#1", "SSL_CERT_FILE", "x"), true},
		{"empty value is not 0", "darwin", env("GODEBUG", "x509sslcertoverrideplatform=", "SSL_CERT_FILE", "x"), true},
		{"override off", "darwin", env("GODEBUG", "x509sslcertoverrideplatform=0", "SSL_CERT_FILE", "x"), false},
		{"no CA file", "darwin", env("GODEBUG", "x509sslcertoverrideplatform=1"), false},
		{"linux is the trust store anyway", "linux", env("GODEBUG", "x509sslcertoverrideplatform=1", "SSL_CERT_FILE", "x"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := trustOverrideWarning(tc.goos, tc.env)
			if (got != "") != tc.warn {
				t.Fatalf("warning = %q, want warn=%v", got, tc.warn)
			}
			if tc.warn && !strings.Contains(got, "SSL_CERT_") {
				t.Errorf("warning does not name the variable: %q", got)
			}
		})
	}
}
