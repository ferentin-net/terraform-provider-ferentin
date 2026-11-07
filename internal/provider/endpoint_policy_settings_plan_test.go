package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

func endpointSettingsSchema(t *testing.T) rschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	(&EndpointPolicySettingsResource{}).Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// The 409 a MOVE to `enforce` gets is about fleet readiness, not a name clash,
// so it must not be reported with addSDKError's "already in use" text. A row
// already on `enforce` saves normally, so a 409 on it, like any request that
// does not send `enforce`, keeps the generic message.
func TestAddEndpointSettingsWriteError_EnforceConflict(t *testing.T) {
	conflict := fmt.Errorf("upsert: %w", adminapi.ErrConflict)
	sending := func(v string) adminapi.EndpointPolicySettingsWrite {
		h := gen.EndpointPolicySettingsRequestHookRuleEnforcement(v)
		return adminapi.EndpointPolicySettingsWrite{HookRuleEnforcement: &h}
	}

	for name, tc := range map[string]struct {
		sent      adminapi.EndpointPolicySettingsWrite
		prior     types.String
		wantFleet bool
	}{
		"create sending enforce":     {sending("enforce"), types.StringNull(), true},
		"shadow to enforce":          {sending("enforce"), types.StringValue("shadow"), true},
		"already enforcing":          {sending("enforce"), types.StringValue("enforce"), false},
		"sending shadow":             {sending("shadow"), types.StringValue("enforce"), false},
		"not sent (config omits it)": {adminapi.EndpointPolicySettingsWrite{}, types.StringValue("enforce"), false},
	} {
		var d diag.Diagnostics
		addEndpointSettingsWriteError(&d, "Failed to update endpoint policy settings", conflict, tc.sent, tc.prior)
		if len(d.Errors()) != 1 {
			t.Fatalf("%s: errors = %v, want exactly one", name, d.Errors())
		}
		summary := d.Errors()[0].Summary()
		if got := strings.Contains(summary, "fleet not ready"); got != tc.wantFleet {
			t.Errorf("%s: summary = %q, want fleet-readiness message = %v", name, summary, tc.wantFleet)
		}
		if tc.wantFleet && !strings.Contains(d.Errors()[0].Detail(), "hookRuleEnforcementCoverage") {
			t.Errorf("%s: detail does not point at hookRuleEnforcementCoverage", name)
		}
		if !tc.wantFleet && !strings.Contains(summary, "conflict with existing resource") {
			t.Errorf("%s: summary = %q, want addSDKError's generic conflict message", name, summary)
		}
	}
}

// On Update, UseStateForUnknown puts the state value into the plan for an
// attribute the config leaves out, so the plan is known there. omitUnconfigured
// must still drop it, or every apply rewrites a value it does not manage.
func TestOmitUnconfigured_DropsStateCarriedValuesTheConfigDoesNotSet(t *testing.T) {
	plan := EndpointPolicySettingsResourceModel{
		HookRuleEnforcement:   types.StringValue("enforce"),
		UnapprovedAgentAction: types.StringValue("block"),
	}

	body, _ := plan.toWrite(context.Background())
	omitUnconfigured(&body, EndpointPolicySettingsResourceModel{
		HookRuleEnforcement:   types.StringNull(),
		UnapprovedAgentAction: types.StringNull(),
	})
	if body.HookRuleEnforcement != nil || body.UnapprovedAgentAction != nil {
		t.Errorf("config omits both: sent hook=%v agent=%v, want both omitted",
			body.HookRuleEnforcement, body.UnapprovedAgentAction)
	}

	body, _ = plan.toWrite(context.Background())
	omitUnconfigured(&body, plan)
	if body.HookRuleEnforcement == nil || *body.HookRuleEnforcement != "enforce" {
		t.Errorf("config sets hook_rule_enforcement: sent %v, want enforce", body.HookRuleEnforcement)
	}
	if body.UnapprovedAgentAction == nil {
		t.Error("config sets unapproved_agent_action: sent nothing, want it")
	}
}

// tool_vetting_risk_policy (ferentin_mcp_scope_defaults) is a plain object attribute (see the schema note), so
// the "all three tiers, each a known action" rule is a validator rather than
// Required + OneOf on nested attributes.
func TestLocalMcpRiskPolicyTiers(t *testing.T) {
	ctx := context.Background()
	obj := func(low, medium, high attr.Value) types.Object {
		return types.ObjectValueMust(localMcpRiskPolicyAttrTypes, map[string]attr.Value{
			"low": low, "medium": medium, "high": high,
		})
	}
	s := types.StringValue
	for name, tc := range map[string]struct {
		value   types.Object
		wantErr string
	}{
		"valid":          {obj(s("allow"), s("ask_once"), s("deny")), ""},
		"null policy":    {types.ObjectNull(localMcpRiskPolicyAttrTypes), ""},
		"unknown policy": {types.ObjectUnknown(localMcpRiskPolicyAttrTypes), ""},
		"unknown tier":   {obj(s("allow"), types.StringUnknown(), s("deny")), ""},
		"missing tier":   {obj(s("allow"), s("deny"), types.StringNull()), "high is required"},
		"bad action":     {obj(s("maybe"), s("deny"), s("deny")), "low must be one of"},
	} {
		resp := &validator.ObjectResponse{}
		localMcpRiskPolicyTiers{}.ValidateObject(ctx, validator.ObjectRequest{
			Path: path.Root("tool_vetting_risk_policy"), ConfigValue: tc.value,
		}, resp)
		if tc.wantErr == "" {
			if resp.Diagnostics.HasError() {
				t.Errorf("%s: unexpected error %v", name, resp.Diagnostics)
			}
			continue
		}
		if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), tc.wantErr) {
			t.Errorf("%s: diags = %v, want an error containing %q", name, resp.Diagnostics, tc.wantErr)
		}
	}
}
