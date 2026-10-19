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

// Removing device_group_ids or an app filter widens the rule the same way
// removing criteria does, and after an upgrade it can be a removal the provider
// used to ignore, so ModifyPlan warns. Only a non-empty list going empty warns:
// an unknown plan may resolve to the same list, and [] -> null (the one-time
// update after upgrading) was never narrowed.
func TestEndpointRuleModifyPlan_WarnsWhenTargetsAreRemoved(t *testing.T) {
	ctx := context.Background()
	s := endpointRuleSchema(t)
	listType := s.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes["device_group_ids"]
	group := tftypes.NewValue(listType, []tftypes.Value{
		tftypes.NewValue(tftypes.String, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
	})
	null := tftypes.NewValue(listType, nil)
	empty := tftypes.NewValue(listType, []tftypes.Value{})
	unknown := tftypes.NewValue(listType, tftypes.UnknownValue)

	for _, attr := range []string{"device_group_ids", "app_bundle_ids", "app_signing_ids", "app_team_ids"} {
		for name, tc := range map[string]struct {
			state, plan tftypes.Value
			wantWarning bool
		}{
			"removed":       {state: group, plan: null, wantWarning: true},
			"emptied":       {state: group, plan: empty, wantWarning: true},
			"unchanged":     {state: group, plan: group},
			"unknown":       {state: group, plan: unknown},
			"empty to null": {state: empty, plan: null},
			"added":         {state: null, plan: group},
		} {
			t.Run(attr+"/"+name, func(t *testing.T) {
				state := tfsdk.State{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
					"name": tftypes.NewValue(tftypes.String, "allow-contractors"),
					attr:   tc.state,
				})}
				plan := tfsdk.Plan{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
					"name": tftypes.NewValue(tftypes.String, "allow-contractors"),
					attr:   tc.plan,
				})}

				r := &EndpointDestinationRuleResource{}
				resp := &resource.ModifyPlanResponse{Plan: plan}
				r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan}, resp)

				if resp.Diagnostics.HasError() {
					t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
				}
				warnings := resp.Diagnostics.Warnings()
				if !tc.wantWarning {
					if len(warnings) != 0 {
						t.Errorf("unexpected warning: %v", warnings)
					}
					return
				}
				if len(warnings) != 1 {
					t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
				}
				if got := warnings[0].Summary(); !strings.Contains(got, attr) {
					t.Errorf("warning summary = %q, want it to name %s", got, attr)
				}
				detail := warnings[0].Detail()
				if !strings.Contains(detail, "allow-contractors") {
					t.Errorf("warning detail = %q, want it to name the rule", detail)
				}
				if !strings.Contains(detail, "1 `"+attr+"` value ") {
					t.Errorf("warning detail = %q, want a singular count", detail)
				}
				if !strings.Contains(detail, "Earlier provider versions") {
					t.Errorf("warning detail = %q, want the upgrade note", detail)
				}
			})
		}
	}
}

// Destination steering is not available yet (only MCP traffic is steered), so a
// destination `steer` rule still plans but says so; allow and block stay quiet.
func TestEndpointRuleValidateConfig_SteerWarns(t *testing.T) {
	ctx := context.Background()
	s := endpointRuleSchema(t)

	validate := func(action string) *resource.ValidateConfigResponse {
		attrs := map[string]tftypes.Value{
			"name":         tftypes.NewValue(tftypes.String, "r"),
			"action":       tftypes.NewValue(tftypes.String, action),
			"catalog_slug": tftypes.NewValue(tftypes.String, "anthropic"),
		}
		if action == "steer" {
			attrs["steer_to_url"] = tftypes.NewValue(tftypes.String, "https://edge.example.com/v1")
		}
		cfg := tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, attrs)}
		resp := &resource.ValidateConfigResponse{}
		(&EndpointDestinationRuleResource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: cfg}, resp)
		return resp
	}

	resp := validate("steer")
	if resp.Diagnostics.HasError() {
		t.Fatalf("a complete steer rule must still plan: %v", resp.Diagnostics.Errors())
	}
	if w := resp.Diagnostics.Warnings(); len(w) != 1 || !strings.Contains(w[0].Summary(), "not available yet") {
		t.Errorf("want one not-available warning, got %v", w)
	}
	for _, a := range []string{"allow", "block"} {
		if w := validate(a).Diagnostics.Warnings(); len(w) != 0 {
			t.Errorf("%s rule warned: %v", a, w)
		}
	}
}
