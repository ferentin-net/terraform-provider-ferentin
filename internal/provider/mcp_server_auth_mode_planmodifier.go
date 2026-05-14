package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// authModeAutoDefaultPlanModifier surfaces the platform's mig 845
// invariant in the plan: when upstream_auth_strategy is non-interactive
// (static_bearer / custom_headers / cc_federated) the platform requires
// auth_mode = "agent" — but if the user omits the attribute, the plan
// otherwise shows `auth_mode = (known after apply)` (when Computed) or
// nothing at all (when Optional-only). Operators don't see the value
// land until apply, and `terraform state show` still says null because
// the response DTO doesn't echo it (ferentin-platform#856).
//
// This modifier fills the resolved value into the plan so the diff reads
//
//	+ auth_mode = "agent"
//
// explicitly. Combined with stringplanmodifier.UseStateForUnknown
// upstream of it, the interaction is:
//
//   - state has a known value → UseStateForUnknown copies it to plan,
//     this modifier returns early.
//   - user set auth_mode explicitly in HCL → plan is known, this modifier
//     returns early.
//   - both are Unknown (first apply, no user value) → this modifier reads
//     upstream_auth_strategy and writes "agent" for non-interactive,
//     types.StringNull() otherwise.
//
// Wire it into the `auth_mode` schema attribute *after* UseStateForUnknown.
type authModeAutoDefaultPlanModifier struct{}

func (authModeAutoDefaultPlanModifier) Description(context.Context) string {
	return "Resolves auth_mode at plan time to mirror the platform's auth_mode/strategy interlock " +
		"so the diff names the value instead of `(known after apply)`."
}

func (m authModeAutoDefaultPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (authModeAutoDefaultPlanModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	var strategy types.String
	diags := req.Plan.GetAttribute(ctx, path.Root("upstream_auth_strategy"), &strategy)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.PlanValue = resolveAuthModeDefault(req.PlanValue, strategy)
}

// resolveAuthModeDefault is the pure-function form of the plan modifier
// above — separated so it's unit-testable without scaffolding a tfsdk.Plan.
//
// Decision table:
//
//	planAuth Known (not Unknown) →  return planAuth unchanged
//	                                  (user set it, or UseStateForUnknown
//	                                   copied state into plan)
//	planAuth Unknown, strategy in non-interactive set → return "agent"
//	                                  (mig 845 — we can predict what the
//	                                   platform will write, so we name it
//	                                   in the plan diff)
//	planAuth Unknown, anything else → return Unknown
//	                                  (let the wire response fill it post
//	                                   ferentin-platform#856; the response
//	                                   DTO now echoes auth_mode)
func resolveAuthModeDefault(planAuth, strategy types.String) types.String {
	if !planAuth.IsUnknown() {
		return planAuth
	}
	if strategy.IsUnknown() || strategy.IsNull() {
		return planAuth
	}
	switch strategy.ValueString() {
	case "static_bearer", "custom_headers", "cc_federated":
		return types.StringValue("agent")
	default:
		return planAuth
	}
}
