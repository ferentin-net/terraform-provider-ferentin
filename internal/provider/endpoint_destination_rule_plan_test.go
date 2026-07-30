package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Plan-time tests for ferentin_endpoint_destination_rule.
//
// Both ValidateConfig and ModifyPlan run during `terraform plan`, where values
// can still be UNKNOWN. `criteria` is the resource's only attribute backed by a
// plain Go slice rather than a types.List, and the framework cannot put an
// unknown into a Go slice — it raises "Value Conversion Error ... always an
// error in the provider". So a config whose criteria come from something not
// yet created (a for-expression over an uncreated resource, say) must not be
// read through the whole-model Get.

func endpointRuleSchema(t *testing.T) rschema.Schema {
	t.Helper()
	r := &EndpointDestinationRuleResource{}
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// endpointRuleValue builds a whole-resource tftypes value, null everywhere
// except the named overrides. Deriving the attribute list from the schema keeps
// the test from rotting every time an attribute is added.
func endpointRuleValue(t *testing.T, s rschema.Schema, overrides map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	objType, ok := s.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("resource schema is not an object type")
	}
	vals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, typ := range objType.AttributeTypes {
		if v, found := overrides[name]; found {
			vals[name] = v
			continue
		}
		vals[name] = tftypes.NewValue(typ, nil)
	}
	return tftypes.NewValue(objType, vals)
}

func endpointRuleCriteriaType(t *testing.T, s rschema.Schema) tftypes.Type {
	t.Helper()
	objType := s.Type().TerraformType(context.Background()).(tftypes.Object)
	return objType.AttributeTypes["criteria"]
}

// A rule whose criteria are not yet resolvable must still plan. Reading them
// through a Go slice fails the whole plan with a "report this to the provider
// developer" error on a config that is perfectly valid.
func TestEndpointRuleValidateConfig_UnknownCriteriaDoesNotBreakPlan(t *testing.T) {
	ctx := context.Background()
	s := endpointRuleSchema(t)

	hostsType := s.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes["destination_hosts"]
	cfg := tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
		"name":             tftypes.NewValue(tftypes.String, "block-unsanctioned"),
		"action":           tftypes.NewValue(tftypes.String, "block"),
		"destination_kind": tftypes.NewValue(tftypes.String, "host"),
		"destination_hosts": tftypes.NewValue(hostsType, []tftypes.Value{
			tftypes.NewValue(tftypes.String, "api.unsanctioned.example"),
		}),
		"criteria": tftypes.NewValue(endpointRuleCriteriaType(t, s), tftypes.UnknownValue),
	})}

	r := &EndpointDestinationRuleResource{}
	resp := &resource.ValidateConfigResponse{}
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: cfg}, resp)

	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Detail(), "cannot handle unknown values") {
			t.Fatalf("unknown criteria broke ValidateConfig: %s — %s", d.Summary(), d.Detail())
		}
	}
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors on a valid config: %v", resp.Diagnostics.Errors())
	}
}

// Same hazard on the ModifyPlan side, which reads BOTH prior state and the
// proposed plan to decide whether an apply is about to widen the rule.
func TestEndpointRuleModifyPlan_UnknownCriteriaDoesNotBreakPlan(t *testing.T) {
	ctx := context.Background()
	s := endpointRuleSchema(t)
	critType := endpointRuleCriteriaType(t, s)

	// Prior state: one criteria group, so the widening check has something to
	// compare against.
	condType := critType.(tftypes.List).ElementType.(tftypes.Object).AttributeTypes["conditions"]
	group := tftypes.NewValue(critType.(tftypes.List).ElementType, map[string]tftypes.Value{
		"operator":    tftypes.NewValue(tftypes.String, "AND"),
		"type":        tftypes.NewValue(tftypes.String, "claims"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"conditions": tftypes.NewValue(condType, []tftypes.Value{
			tftypes.NewValue(condType.(tftypes.List).ElementType, map[string]tftypes.Value{
				"field":          tftypes.NewValue(tftypes.String, "department"),
				"operator":       tftypes.NewValue(tftypes.String, "equals"),
				"value":          tftypes.NewValue(tftypes.String, `"legal"`),
				"value_type":     tftypes.NewValue(tftypes.String, nil),
				"case_sensitive": tftypes.NewValue(tftypes.Bool, nil),
				"description":    tftypes.NewValue(tftypes.String, nil),
			}),
		}),
	})

	state := tfsdk.State{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
		"name":     tftypes.NewValue(tftypes.String, "allow-legal"),
		"action":   tftypes.NewValue(tftypes.String, "allow"),
		"criteria": tftypes.NewValue(critType, []tftypes.Value{group}),
	})}
	plan := tfsdk.Plan{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
		"name":     tftypes.NewValue(tftypes.String, "allow-legal"),
		"action":   tftypes.NewValue(tftypes.String, "allow"),
		"criteria": tftypes.NewValue(critType, tftypes.UnknownValue),
	})}

	r := &EndpointDestinationRuleResource{}
	resp := &resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unknown criteria broke ModifyPlan: %v", resp.Diagnostics.Errors())
	}
	// Unknown is not "removed" — warning here would cry wolf on every plan that
	// computes its criteria.
	if resp.Diagnostics.WarningsCount() != 0 {
		t.Errorf("warned about a widening that may not happen: %v", resp.Diagnostics.Warnings())
	}
}

// The widening warning must still fire for the case it exists for: prior state
// has criteria, the plan resolves to none.
func TestEndpointRuleModifyPlan_WarnsWhenCriteriaAreRemoved(t *testing.T) {
	ctx := context.Background()
	s := endpointRuleSchema(t)
	critType := endpointRuleCriteriaType(t, s)
	condType := critType.(tftypes.List).ElementType.(tftypes.Object).AttributeTypes["conditions"]

	group := tftypes.NewValue(critType.(tftypes.List).ElementType, map[string]tftypes.Value{
		"operator":    tftypes.NewValue(tftypes.String, "AND"),
		"type":        tftypes.NewValue(tftypes.String, "claims"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"conditions": tftypes.NewValue(condType, []tftypes.Value{
			tftypes.NewValue(condType.(tftypes.List).ElementType, map[string]tftypes.Value{
				"field":          tftypes.NewValue(tftypes.String, "department"),
				"operator":       tftypes.NewValue(tftypes.String, "equals"),
				"value":          tftypes.NewValue(tftypes.String, `"legal"`),
				"value_type":     tftypes.NewValue(tftypes.String, nil),
				"case_sensitive": tftypes.NewValue(tftypes.Bool, nil),
				"description":    tftypes.NewValue(tftypes.String, nil),
			}),
		}),
	})

	state := tfsdk.State{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
		"name":     tftypes.NewValue(tftypes.String, "allow-legal"),
		"criteria": tftypes.NewValue(critType, []tftypes.Value{group}),
	})}
	plan := tfsdk.Plan{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
		"name":     tftypes.NewValue(tftypes.String, "allow-legal"),
		"criteria": tftypes.NewValue(critType, nil),
	})}

	r := &EndpointDestinationRuleResource{}
	resp := &resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("no warning when an apply removes criteria — that apply widens the rule to everyone it targets")
	}
	if got := resp.Diagnostics.Warnings()[0].Detail(); !strings.Contains(got, "allow-legal") {
		t.Errorf("warning detail = %q, want it to name the rule", got)
	}
}
