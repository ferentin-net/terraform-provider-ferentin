package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// markComputedUnknownOnChange plans the given server-computed attributes as
// unknown when the planned resource differs from state.
//
// The framework does this itself only when the CONFIG differs from state: it
// marks computed attributes unknown before attribute plan modifiers run. A
// diff that a plan modifier or ModifyPlan introduces (a mode derived from its
// value, applies_at planned as the platform defaults it) therefore leaves
// version and its kin planned at their state values, and the apply then fails
// with "Provider produced inconsistent result after apply". Call this at the
// end of ModifyPlan, after every planned value is set.
func markComputedUnknownOnChange(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, unknowns map[string]attr.Value) {
	if req.State.Raw.IsNull() || resp.Plan.Raw.IsNull() || resp.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	for name, unknown := range unknowns {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), unknown)...)
	}
}
