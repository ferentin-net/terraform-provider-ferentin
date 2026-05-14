package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestResolveAuthModeDefault locks in the resolution table that makes the
// auto-default visible in `terraform plan`. Cases mirror the platform's
// mig 845 invariant — non-interactive strategies must pair with
// auth_mode=agent; everything else lets the platform infer.
func TestResolveAuthModeDefault(t *testing.T) {
	cases := []struct {
		name     string
		planAuth types.String
		strategy types.String
		want     types.String
		why      string
	}{
		{
			name:     "known plan value passes through",
			planAuth: types.StringValue("user"),
			strategy: types.StringValue("static_bearer"),
			want:     types.StringValue("user"),
			why:      "user set auth_mode explicitly — modifier must not override",
		},
		{
			name:     "known-null plan value passes through",
			planAuth: types.StringNull(),
			strategy: types.StringValue("static_bearer"),
			want:     types.StringNull(),
			why:      "UseStateForUnknown copied a null state into plan — don't re-default",
		},
		{
			name:     "static_bearer unknown plan → agent",
			planAuth: types.StringUnknown(),
			strategy: types.StringValue("static_bearer"),
			want:     types.StringValue("agent"),
			why:      "mig 845 — non-interactive strategy requires agent mode",
		},
		{
			name:     "custom_headers unknown plan → agent",
			planAuth: types.StringUnknown(),
			strategy: types.StringValue("custom_headers"),
			want:     types.StringValue("agent"),
			why:      "non-interactive — custom_headers",
		},
		{
			name:     "cc_federated unknown plan → agent",
			planAuth: types.StringUnknown(),
			strategy: types.StringValue("cc_federated"),
			want:     types.StringValue("agent"),
			why:      "non-interactive — cc_federated",
		},
		{
			name:     "oauth2_user unknown plan → null",
			planAuth: types.StringUnknown(),
			strategy: types.StringValue("oauth2_user"),
			want:     types.StringNull(),
			why:      "interactive — platform infers; null keeps plan/state aligned",
		},
		{
			name:     "xaa_federated unknown plan → null",
			planAuth: types.StringUnknown(),
			strategy: types.StringValue("xaa_federated"),
			want:     types.StringNull(),
			why:      "interactive — xaa_federated",
		},
		{
			name:     "none unknown plan → null",
			planAuth: types.StringUnknown(),
			strategy: types.StringValue("none"),
			want:     types.StringNull(),
			why:      "no-auth strategy — platform infers",
		},
		{
			name:     "unknown strategy keeps plan Unknown",
			planAuth: types.StringUnknown(),
			strategy: types.StringUnknown(),
			want:     types.StringUnknown(),
			why:      "strategy interpolated from another resource — don't guess; create-time fallback covers it",
		},
		{
			name:     "null strategy → null",
			planAuth: types.StringUnknown(),
			strategy: types.StringNull(),
			why:      "no strategy set → no auto-default; platform infers",
			want:     types.StringNull(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAuthModeDefault(tc.planAuth, tc.strategy)
			if !got.Equal(tc.want) {
				t.Errorf("resolveAuthModeDefault(plan=%v, strategy=%v) = %v, want %v (%s)",
					tc.planAuth, tc.strategy, got, tc.want, tc.why)
			}
		})
	}
}
