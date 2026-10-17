package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
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
	policyAttr, ok := s.Attributes["local_mcp_risk_policy"].(rschema.SingleNestedAttribute)
	if !ok {
		t.Fatal("local_mcp_risk_policy is not a single nested attribute")
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

// The 409 a move to `enforce` gets is about fleet readiness, not a name clash,
// so it must not be reported with addSDKError's "already in use" text. Any other
// 409 still is.
func TestAddEndpointSettingsWriteError_EnforceConflict(t *testing.T) {
	conflict := fmt.Errorf("upsert: %w", adminapi.ErrConflict)

	var d diag.Diagnostics
	addEndpointSettingsWriteError(&d, "Failed to update endpoint policy settings", conflict,
		EndpointPolicySettingsResourceModel{HookRuleEnforcement: types.StringValue("enforce")})
	if len(d.Errors()) != 1 {
		t.Fatalf("errors = %v, want exactly one", d.Errors())
	}
	if got := d.Errors()[0].Summary(); !strings.Contains(got, "fleet not ready") {
		t.Errorf("summary = %q, want the fleet-readiness message", got)
	}
	if got := d.Errors()[0].Detail(); !strings.Contains(got, "hookRuleEnforcementCoverage") {
		t.Errorf("detail does not point at hookRuleEnforcementCoverage: %q", got)
	}

	d = nil
	addEndpointSettingsWriteError(&d, "Failed to update endpoint policy settings", conflict,
		EndpointPolicySettingsResourceModel{HookRuleEnforcement: types.StringValue("shadow")})
	if got := d.Errors()[0].Summary(); !strings.Contains(got, "conflict with existing resource") {
		t.Errorf("summary = %q, want addSDKError's generic conflict message", got)
	}
}
