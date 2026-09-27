package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// Plan-time guard for `cloud_provider = "cursor"` on
// `ferentin_workload_identity_provider`.
//
// Cursor is the first WIF issuer that lets anyone mint a token for any
// audience: api.cursor.com does not allowlist `aud`, so a correctly-signed
// token proves only "some Cursor agent, somewhere". `team_id` is the one
// signed, non-caller-selectable anchor, so the authorization server's
// CursorTokenValidator refuses every token from a registration whose
// `cloud_config.allowed_team_ids` is empty or made only of wildcards.
//
// ERROR, NOT WARNING. The admin API accepts that config — the refusal happens
// at token time in a different server — so without this the apply succeeds
// and the operator holds a provider that can never authenticate anything.
// There is no working configuration this rejects.
//
// Mirrors readPatterns / validateAllowedTeams in CursorTokenValidator: a bare
// string counts as a one-element list, empty entries are dropped, and a
// pattern of only `*` is unbounded. Keep the two in step.

// cursorAllowedTeamsProblem reports why cloudConfig would leave a Cursor
// provider unable to accept any token, or ok=false when it is fine.
func cursorAllowedTeamsProblem(cloudConfig string) (detail string, ok bool) {
	const fix = "Set `cloud_config` to a JSON object whose `allowed_team_ids` names your Cursor " +
		"team, e.g.\n\n" +
		"    cloud_config = jsonencode({ allowed_team_ids = [\"team_abc123\"] })\n\n" +
		"Prefix patterns such as \"team_acme*\" are accepted."

	if strings.TrimSpace(cloudConfig) == "" {
		return "`cloud_config` is not set. " + cursorWhy + "\n\n" + fix, true
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(cloudConfig), &parsed); err != nil {
		return fmt.Sprintf("`cloud_config` is not a JSON object (%v). %s\n\n%s", err, cursorWhy, fix), true
	}

	teams := readCursorTeamPatterns(parsed["allowed_team_ids"])
	if len(teams) == 0 {
		return "`cloud_config` has no `allowed_team_ids`. " + cursorWhy + "\n\n" + fix, true
	}
	var unbounded []string
	for _, t := range teams {
		if strings.Trim(t, "*") == "" {
			unbounded = append(unbounded, t)
		}
	}
	if len(unbounded) > 0 {
		return fmt.Sprintf("`allowed_team_ids` contains [%s], which matches every Cursor team. %s\n\n%s",
			strings.Join(quoteAll(unbounded), ", "), cursorWhy, fix), true
	}
	return "", false
}

const cursorWhy = "Cursor does not restrict who may mint a token for a given audience, so the team " +
	"allowlist is the only thing tying a token to your organisation; the authorization server " +
	"rejects every token from a Cursor provider without a bounded one."

func readCursorTeamPatterns(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if e == nil {
				continue
			}
			s := fmt.Sprint(e)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (r *WorkloadIdentityProviderResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config WorkloadIdentityProviderResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.CloudProvider.IsUnknown() || config.CloudProvider.ValueString() != "cursor" {
		return
	}
	// A reference we cannot inspect yet — the check runs again once it is known.
	if config.CloudConfig.IsUnknown() {
		return
	}
	detail, bad := cursorAllowedTeamsProblem(config.CloudConfig.ValueString())
	if !bad {
		return
	}
	resp.Diagnostics.AddAttributeError(path.Root("cloud_config"),
		"Cursor provider would reject every token", detail)
}
