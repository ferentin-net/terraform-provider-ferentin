package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Helpers shared by the endpoint rule resources (destination rules and MCP
// rules), so a third rule type does not make a third copy.

// parseTenantScopedImportID splits an import id of the form
// `<tenant_id>/<rule_id>` or a bare `<rule_id>` (tenant from the provider
// block). The rule id must be a UUID: a trailing slash or an extra segment
// would otherwise reach Read as a malformed id and fail with an error that
// does not name the import.
func parseTenantScopedImportID(id, defaultTenant, kind string) (tenantID, ruleID string, diags diag.Diagnostics) {
	parts := strings.Split(id, "/")
	switch len(parts) {
	case 1:
		tenantID, ruleID = defaultTenant, parts[0]
	case 2:
		tenantID, ruleID = parts[0], parts[1]
	default:
		diags.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<tenant_id>/<%s_id>` or `<%s_id>`, got %q.", kind, kind, id))
		return "", "", diags
	}
	if tenantID == "" {
		diags.AddError("Cannot determine tenant for import",
			fmt.Sprintf("Pass `<tenant_id>/<%s_id>` or configure `tenant_id` on the provider block.", kind))
		return "", "", diags
	}
	if _, err := uuid.Parse(ruleID); err != nil {
		diags.AddError("Invalid import ID",
			fmt.Sprintf("The %s id must be a UUID, got %q (from import ID %q).", kind, ruleID, id))
		return "", "", diags
	}
	return tenantID, ruleID, diags
}

// warnOnCriteriaRemoval warns when an apply is about to delete a rule's
// criteria. Criteria NARROW a rule to a population, so removing them is a
// widening, worth more than a `-` in the diff.
//
// Criteria are read as types.List rather than through the model: they are a Go
// slice there, and an unknown (criteria computed from something not yet
// created) cannot be reflected into one. extra is appended to the detail.
func warnOnCriteriaRemoval(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, extra string) {
	// Create (no prior state) and destroy (no plan) are not widenings.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var stateCriteria, planCriteria types.List
	var name types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("criteria"), &stateCriteria)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("criteria"), &planCriteria)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("name"), &name)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// An unknown plan value may resolve to anything, including the same criteria.
	if planCriteria.IsUnknown() {
		return
	}
	if len(stateCriteria.Elements()) > 0 && len(planCriteria.Elements()) == 0 {
		resp.Diagnostics.AddAttributeWarning(path.Root("criteria"),
			"This apply removes the rule's subject criteria",
			fmt.Sprintf("Rule %q currently has %d criteria group(s) and the plan has none. Criteria "+
				"NARROW a rule to a population, so removing them widens it to every user in the "+
				"targeted device groups — an `allow` rule scoped to one department becomes an allow "+
				"for everyone.%s", name.ValueString(), len(stateCriteria.Elements()), extra))
	}
}

// warnOnTargetRemoval warns when an apply is about to empty a list that narrows
// the rule (device_group_ids, an app filter). Like criteria, an empty list
// means "everything", so the removal widens the rule. widensTo names what the
// rule then applies to; extra is appended to the detail, as for
// warnOnCriteriaRemoval.
func warnOnTargetRemoval(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, attr, widensTo, extra string) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var stateList, planList types.List
	var name types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(attr), &stateList)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(attr), &planList)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("name"), &name)...)
	if resp.Diagnostics.HasError() || planList.IsUnknown() {
		return
	}
	if n := len(stateList.Elements()); n > 0 && len(planList.Elements()) == 0 {
		values := "values"
		if n == 1 {
			values = "value"
		}
		resp.Diagnostics.AddAttributeWarning(path.Root(attr),
			fmt.Sprintf("This apply removes the rule's `%s`", attr),
			fmt.Sprintf("Rule %q currently has %d `%s` %s and the plan has none, so it will "+
				"apply to %s. An empty list means \"no narrowing\", not \"nothing\".\n\n"+
				"If you meant to keep the rule narrowed, add `%s` back to the config.%s",
				name.ValueString(), n, attr, values, widensTo, attr, extra))
	}
}

// emptyStringAsNull reads an absent or empty string as null, for an Optional
// (not Computed) attribute the platform clears to "" or omits.
func emptyStringAsNull(p *string) types.String {
	if p == nil || *p == "" {
		return types.StringNull()
	}
	return types.StringValue(*p)
}

// emptyListAsNull reads an empty list as null, for an Optional (not Computed)
// list the platform returns as [] or omits when unset.
func emptyListAsNull(l types.List) types.List {
	if l.IsNull() || l.IsUnknown() || len(l.Elements()) == 0 {
		return types.ListNull(types.StringType)
	}
	return l
}

// keepEmptyString and keepEmptyList restore the empty form a config wrote
// explicitly (`description = ""`, `device_group_ids = []`). The platform does
// not distinguish it from unset, but Terraform requires state to match config,
// so when prior (the plan, or state on refresh) holds the empty form and the
// server has nothing, the empty form is kept.
func keepEmptyString(prior, got types.String) types.String {
	if got.IsNull() && !prior.IsNull() && !prior.IsUnknown() && prior.ValueString() == "" {
		return prior
	}
	return got
}

func keepEmptyList(prior, got types.List) types.List {
	if got.IsNull() && !prior.IsNull() && !prior.IsUnknown() && len(prior.Elements()) == 0 {
		return prior
	}
	return got
}
