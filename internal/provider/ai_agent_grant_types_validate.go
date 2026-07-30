package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// Plan-time guidance for `ferentin_ai_agent.grant_types` (issue #7).
//
// A SERVICE agent that omits `client_credentials` is created as
// `ai_client_type = "assistant"` — the platform generates that column from
// grant_types alone — and cannot mint a token without a signed-in user. The
// schema says so in prose; this puts it in the plan output the operator is
// actually reading.
//
// WARNING, NOT ERROR, AND NOT A DEFAULT. Deliberately:
//
//   - `grant_types` is a security-relevant capability set. A plan modifier that
//     widened or narrowed it during apply would change what a client can do
//     without the change appearing as an operator-authored diff.
//   - A derived default can emit a set the actor is not permitted to write:
//     AgentClientScopeAllowlist constrains agent clients to
//     grant_types ⊆ {client_credentials, authorization_code} for actors holding
//     `clients:agent:rw` but not `clients:rw`. A previously-working config would
//     start failing at apply on a value the operator never wrote and cannot find
//     in their HCL.
//
// This lives in ValidateConfig rather than a plan modifier because the
// attribute is Optional+Computed: at plan time a modifier sees an unknown value
// and cannot distinguish "operator omitted it" from "provider will fill it".
// Config sees null-vs-set cleanly.

// grantTypesForM2M is the conventional set for a SERVICE agent.
const grantTypeClientCredentials = "client_credentials"

// agentGrantTypesAdvice reports the warning detail for a given configuration,
// or ok=false when there is nothing to say.
//
// Split out from ValidateConfig so the matrix is unit-testable without
// assembling a framework request.
func agentGrantTypesAdvice(applicationType string, grantTypes []string, unset bool) (detail string, ok bool) {
	// Only SERVICE gets a warning. NATIVE and WEB agents act for a signed-in
	// user, so `authorization_code` + `refresh_token` — no client_credentials —
	// is the correct shape for them, and warning there would be noise that
	// teaches operators to ignore the diagnostic.
	if applicationType != "SERVICE" {
		return "", false
	}
	if !unset && containsString(grantTypes, grantTypeClientCredentials) {
		return "", false
	}

	var lead string
	if unset {
		lead = "`grant_types` is not set. The platform's default omits " +
			"`client_credentials`"
	} else {
		lead = fmt.Sprintf("`grant_types` is set to [%s], which omits `client_credentials`",
			strings.Join(quoteAll(grantTypes), ", "))
	}

	return lead + ", and the platform generates `ai_client_type` from `grant_types` " +
		"and nothing else — so this agent is created as `ai_client_type = \"assistant\"` " +
		"and cannot mint a token without a signed-in user.\n\n" +
		"`application_type = \"SERVICE\"` agents are almost always machine-to-machine; the " +
		"conventional set is:\n\n" +
		"    grant_types = [\"client_credentials\"]\n\n" +
		"(`NATIVE` / `WEB` agents act for a signed-in user and conventionally take " +
		"[\"authorization_code\", \"refresh_token\"], which is why this warning is scoped to " +
		"SERVICE.)\n\n" +
		"This is a warning, not an error: set `grant_types` explicitly to whatever this agent " +
		"genuinely needs and it goes away.", true
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func quoteAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}

func (r *AIAgentResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config AIAgentResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// application_type is Required, but it can still be unknown when it comes
	// from a variable or another resource's attribute. Nothing useful to say
	// then.
	if config.ApplicationType.IsNull() || config.ApplicationType.IsUnknown() {
		return
	}
	// An unknown list is a reference we cannot inspect — silence beats a guess.
	if config.GrantTypes.IsUnknown() {
		return
	}

	unset := config.GrantTypes.IsNull()
	var grantTypes []string
	if !unset {
		resp.Diagnostics.Append(config.GrantTypes.ElementsAs(ctx, &grantTypes, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	detail, ok := agentGrantTypesAdvice(config.ApplicationType.ValueString(), grantTypes, unset)
	if !ok {
		return
	}
	resp.Diagnostics.AddAttributeWarning(path.Root("grant_types"),
		"SERVICE agent will be created as an assistant", detail)
}
