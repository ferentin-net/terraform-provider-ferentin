package provider

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// rateLimitErr is what an SDK sub-client returns for a 429 after retries: the
// sentinel and the *HTTPError carrying the body.
func rateLimitErr(body string) error {
	return fmt.Errorf("%w: %w", adminapi.ErrRateLimited, &adminapi.HTTPError{Status: 429, Body: body})
}

func TestAddSDKError_RateLimited(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		want, deny []string
	}{
		{
			name: "write-wide budget",
			err: rateLimitErr(`{"error":"rate_limit_exceeded","retry_after":60,` +
				`"category":"tenant_write_hourly_principal","scope":"writes"}`),
			want: []string{`"tenant_write_hourly_principal"`, "every create, update and delete",
				"lowering -parallelism does not help", "at least 1m0s"},
			deny: []string{"-parallelism=1`"},
		},
		{
			name: "a category's own window",
			err:  rateLimitErr(`{"error":"rate_limit_exceeded","retry_after":30,"category":"admin_policy_write"}`),
			want: []string{`"admin_policy_write"`, "-parallelism=1", "at least 30s"},
			deny: []string{"hourly"},
		},
		{
			name: "no body",
			err:  fmt.Errorf("wrap: %w", adminapi.ErrRateLimited),
			want: []string{"A platform rate limit was exceeded", "-parallelism=1"},
			deny: []string{"asked for at least"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var diags diag.Diagnostics
			addSDKError(&diags, "Failed to update thing", c.err)
			if len(diags) != 1 {
				t.Fatalf("want one diagnostic, got %d", len(diags))
			}
			if got := diags[0].Summary(); got != "Failed to update thing — rate-limited" {
				t.Errorf("summary = %q", got)
			}
			detail := diags[0].Detail()
			for _, w := range c.want {
				if !strings.Contains(detail, w) {
					t.Errorf("detail lacks %q:\n%s", w, detail)
				}
			}
			for _, d := range c.deny {
				if strings.Contains(detail, d) {
					t.Errorf("detail should not contain %q:\n%s", d, detail)
				}
			}
		})
	}
}
