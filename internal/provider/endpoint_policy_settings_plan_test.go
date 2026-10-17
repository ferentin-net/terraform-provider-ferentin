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
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

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

// The platform decides unvetted local MCP tools from the tenant-default row
// alone and refuses local_mcp_risk_policy on a device-group override (400). The
// schema says so at plan time instead.
func TestEndpointSettings_RiskPolicyConflictsWithDeviceGroup(t *testing.T) {
	ctx := context.Background()
	s := endpointSettingsSchema(t)
	policyType := s.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes["local_mcp_risk_policy"]
	policy := tftypes.NewValue(policyType, map[string]tftypes.Value{
		"low":    tftypes.NewValue(tftypes.String, "allow"),
		"medium": tftypes.NewValue(tftypes.String, "ask_once"),
		"high":   tftypes.NewValue(tftypes.String, "deny"),
	})
	policyAttr, ok := s.Attributes["local_mcp_risk_policy"].(rschema.ObjectAttribute)
	if !ok {
		t.Fatal("local_mcp_risk_policy is not an object attribute")
	}

	run := func(groupID tftypes.Value) diag.Diagnostics {
		cfg := tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
			"device_group_id":       groupID,
			"local_mcp_risk_policy": policy,
		})}
		var obj types.Object
		if d := cfg.GetAttribute(ctx, path.Root("local_mcp_risk_policy"), &obj); d.HasError() {
			t.Fatalf("read policy: %v", d)
		}
		var diags diag.Diagnostics
		for _, v := range policyAttr.Validators {
			resp := &validator.ObjectResponse{}
			v.ValidateObject(ctx, validator.ObjectRequest{
				Path:           path.Root("local_mcp_risk_policy"),
				PathExpression: path.MatchRoot("local_mcp_risk_policy"),
				ConfigValue:    obj,
				Config:         cfg,
			}, resp)
			diags.Append(resp.Diagnostics...)
		}
		return diags
	}

	if d := run(tftypes.NewValue(tftypes.String, nil)); d.HasError() {
		t.Errorf("tenant default row: unexpected error %v", d)
	}
	if d := run(tftypes.NewValue(tftypes.String, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")); !d.HasError() {
		t.Error("device-group override: want a conflict error, got none")
	}
	// A group id not yet known at plan (a new ferentin_device_group) must not fail
	// the plan; the platform still refuses it at apply.
	if d := run(tftypes.NewValue(tftypes.String, tftypes.UnknownValue)); d.HasError() {
		t.Errorf("unknown device_group_id: unexpected error %v", d)
	}
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
	riskPolicy := types.ObjectValueMust(localMcpRiskPolicyAttrTypes, map[string]attr.Value{
		"low": types.StringValue("allow"), "medium": types.StringValue("ask_once"), "high": types.StringValue("deny"),
	})
	plan := EndpointPolicySettingsResourceModel{
		HookRuleEnforcement: types.StringValue("enforce"),
		LocalMcpRiskPolicy:  riskPolicy,
	}

	body := plan.toWrite()
	omitUnconfigured(&body, EndpointPolicySettingsResourceModel{
		HookRuleEnforcement: types.StringNull(),
		LocalMcpRiskPolicy:  types.ObjectNull(localMcpRiskPolicyAttrTypes),
	})
	if body.HookRuleEnforcement != nil || body.LocalMcpRiskPolicy != nil {
		t.Errorf("config omits both: sent hook=%v policy=%v, want both omitted",
			body.HookRuleEnforcement, body.LocalMcpRiskPolicy)
	}

	body = plan.toWrite()
	omitUnconfigured(&body, plan)
	if body.HookRuleEnforcement == nil || *body.HookRuleEnforcement != "enforce" {
		t.Errorf("config sets hook_rule_enforcement: sent %v, want enforce", body.HookRuleEnforcement)
	}
	if body.LocalMcpRiskPolicy == nil {
		t.Error("config sets local_mcp_risk_policy: sent nothing, want the policy")
	}
}

// local_mcp_risk_policy is a plain object attribute (see the schema note), so
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
			Path: path.Root("local_mcp_risk_policy"), ConfigValue: tc.value,
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
