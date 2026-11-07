package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

func mcpPolicySchema(t *testing.T) rschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	(&MCPPolicyResource{}).Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// The update keeps whatever the request omits, so an update must restate
// every owned field with its empty form. Without this, removing criteria,
// targets, an exception or the description from config would never reach
// the platform.
func TestMCPPolicyToWrite_RestatesEmptyForms(t *testing.T) {
	m := MCPPolicyResourceModel{
		Name:               types.StringValue("p"),
		Description:        types.StringNull(),
		Priority:           types.Int64Value(100),
		Enabled:            types.BoolValue(true),
		ValidateArguments:  types.BoolValue(true),
		Mode:               types.StringValue("enforce"),
		AppliesAt:          types.StringValue("both"),
		CriteriaCombinator: types.StringValue("OR"),
		ProviderInstances:  types.ListNull(types.StringType),
		Effect:             &MCPPolicyEffectModel{Type: types.StringValue("deny")},
	}
	w, diags := m.toWrite(context.Background())
	if diags.HasError() {
		t.Fatalf("toWrite: %v", diags)
	}
	body := adminapi.MCPPolicyUpdate{
		Description: w.description, ProviderInstances: w.instances, Targets: w.targets,
		Criteria: w.criteria, Unless: w.unless,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	for _, key := range []string{"description", "provider_instances", "targets", "criteria", "unless"} {
		if _, ok := got[key]; !ok {
			t.Errorf("update body omits %q; the platform would keep the stored value: %s", key, raw)
		}
	}
	if got["description"] != "" {
		t.Errorf("description = %v, want \"\"", got["description"])
	}
	unless, _ := got["unless"].(map[string]any)
	if groups, ok := unless["groups"].([]any); !ok || len(groups) != 0 {
		t.Errorf("unless = %v, want an exception with no groups (which clears it)", got["unless"])
	}
}

func TestMCPPolicyToWrite_Targets(t *testing.T) {
	m := MCPPolicyResourceModel{
		Description: types.StringValue("block evil"),
		Effect:      &MCPPolicyEffectModel{Type: types.StringValue("deny")},
		Targets: []MCPPolicyTargetModel{
			{Kind: types.StringValue("package"), Values: stringList("npm:@evil/mcp")},
			{Kind: types.StringValue("app_bundle_id"), Values: stringList("com.todesktop.230313mzl4w4u92")},
		},
		Unless: &MCPPolicyUnlessModel{
			Combinator: types.StringValue("OR"),
			Groups: []PolicyCriteriaModel{{
				Operator: types.StringValue("AND"), Type: types.StringValue("claims"),
				Conditions: []PolicyCriteriaConditionModel{{
					Field: types.StringValue("groups"), Operator: types.StringValue("contains"),
					Value: types.StringValue(`"Security"`),
				}},
			}},
		},
	}
	w, diags := m.toWrite(context.Background())
	if diags.HasError() {
		t.Fatalf("toWrite: %v", diags)
	}
	if len(*w.targets) != 2 || string(*(*w.targets)[0].Kind) != "package" || (*(*w.targets)[0].Values)[0] != "npm:@evil/mcp" {
		t.Errorf("targets = %+v", *w.targets)
	}
	if w.unless.Combinator == nil || *w.unless.Combinator != "OR" || len(*w.unless.Groups) != 1 {
		t.Errorf("unless = %+v", w.unless)
	}
	if *w.description != "block evil" {
		t.Errorf("description = %q", *w.description)
	}
}

func TestDefaultAppliesAt(t *testing.T) {
	cases := []struct {
		effect                  string
		hasTargets, hasInstance bool
		want                    string
	}{
		{"allow", true, false, "admit"},
		{"allow", true, true, "both"},
		{"allow", false, true, "both"},
		{"deny", true, false, "both"},
	}
	for _, c := range cases {
		if got := defaultAppliesAt(c.effect, c.hasTargets, c.hasInstance); got != c.want {
			t.Errorf("defaultAppliesAt(%s, targets=%v, instances=%v) = %s, want %s",
				c.effect, c.hasTargets, c.hasInstance, got, c.want)
		}
	}
}

func TestMCPPolicyToModel_Unified(t *testing.T) {
	polID := mustParseUUID(t, "33333333-3333-4333-8333-333333333333")
	sib := mustParseUUID(t, "44444444-4444-4444-8444-444444444444")
	mode := gen.McpPolicyResponseMode("report_only")
	at := gen.McpPolicyResponseAppliesAt("admit")
	kind := gen.McpPolicyTargetKind("host")
	comb := gen.PolicyUnlessCombinator("OR")
	managed := gen.McpPolicyResponseManagedBy("iac")
	pol := &adminapi.MCPPolicy{
		Id: &polID, Name: strPtr("allow-github"), Description: strPtr(""),
		Mode: &mode, AppliesAt: &at, SiblingOf: &sib, SettingsInForce: boolPtr(false),
		Targets:   &[]gen.McpPolicyTarget{{Kind: &kind, Values: &[]string{"mcp.github.com"}}},
		Effect:    &gen.McpEffectDto{Effect: "allow"},
		Unless:    &gen.PolicyUnless{Combinator: &comb, Groups: &[]gen.PolicyCriteria{}},
		Version:   int64Ptr(4),
		ManagedBy: &managed,
	}
	m := mcpPolicyToModel(fixtureTenantID, pol)
	if m.Mode.ValueString() != "report_only" || m.AppliesAt.ValueString() != "admit" {
		t.Errorf("mode/applies_at = %s/%s", m.Mode.ValueString(), m.AppliesAt.ValueString())
	}
	if !m.Description.IsNull() {
		t.Errorf("an empty description must read as null, got %q", m.Description.ValueString())
	}
	if !m.ProviderInstances.IsNull() {
		t.Errorf("absent provider_instances must read as null")
	}
	if m.CriteriaCombinator.ValueString() != "OR" {
		t.Errorf("an unset combinator reads as OR, got %q", m.CriteriaCombinator.ValueString())
	}
	if len(m.Targets) != 1 || m.Targets[0].Kind.ValueString() != "host" {
		t.Errorf("targets = %+v", m.Targets)
	}
	if len(m.StoredTargets.Elements()) != 1 {
		t.Errorf("stored_targets = %v", m.StoredTargets)
	}
	if m.Unless != nil {
		t.Errorf("an exception with no groups must read as no exception, got %+v", m.Unless)
	}
	if m.SiblingOf.ValueString() != sib.String() || m.ManagedBy.ValueString() != "iac" || m.Version.ValueInt64() != 4 {
		t.Errorf("sibling_of/managed_by/version = %s/%s/%d",
			m.SiblingOf.ValueString(), m.ManagedBy.ValueString(), m.Version.ValueInt64())
	}

	// No targets still yields a known, empty stored_targets.
	pol.Targets = nil
	if m2 := mcpPolicyToModel(fixtureTenantID, pol); m2.StoredTargets.IsNull() || len(m2.StoredTargets.Elements()) != 0 || m2.Targets != nil {
		t.Errorf("no targets: targets=%v stored=%v", m2.Targets, m2.StoredTargets)
	}
}

// The platform canonicalizes target values. While the stored form is the one
// the last apply saw, the configured spelling is kept; a console edit replaces
// it so the plan shows the drift; an import takes the stored form.
func TestReconcileTargets(t *testing.T) {
	kind := gen.McpPolicyTargetKind("host")
	stored := func(v string) MCPPolicyResourceModel {
		m := MCPPolicyResourceModel{}
		m.Targets, m.StoredTargets = mcpPolicyTargetsFromSDK(&[]gen.McpPolicyTarget{{Kind: &kind, Values: &[]string{v}}})
		return m
	}
	prior := stored("mcp.github.com")
	prior.Targets = []MCPPolicyTargetModel{{Kind: types.StringValue("host"), Values: stringList("MCP.GitHub.com")}}

	if got := reconcileTargets(prior, stored("mcp.github.com")); got[0].Values.Elements()[0].(types.String).ValueString() != "MCP.GitHub.com" {
		t.Errorf("unchanged stored form: got %v, want the configured spelling", got)
	}
	if got := reconcileTargets(prior, stored("mcp.evil.com")); got[0].Values.Elements()[0].(types.String).ValueString() != "mcp.evil.com" {
		t.Errorf("console edit: got %v, want the stored form", got)
	}
	imported := MCPPolicyResourceModel{StoredTargets: types.ListNull(mcpPolicyTargetObjectType)}
	if got := reconcileTargets(imported, stored("mcp.github.com")); got[0].Values.Elements()[0].(types.String).ValueString() != "mcp.github.com" {
		t.Errorf("import: got %v, want the stored form", got)
	}
}

// ValidateConfig mirrors McpPolicyWriteCheck's cheap refusals. Each failing
// case has a passing neighbour, so a rule that fires too broadly fails too.
func TestMCPPolicyValidateConfig(t *testing.T) {
	ctx := context.Background()
	s := mcpPolicySchema(t)
	objType := s.Type().TerraformType(ctx).(tftypes.Object)
	str := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	strs := func(vals ...string) tftypes.Value {
		elems := make([]tftypes.Value, len(vals))
		for i, v := range vals {
			elems[i] = str(v)
		}
		return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, elems)
	}
	targetsType := objType.AttributeTypes["targets"].(tftypes.List)
	targetType := targetsType.ElementType.(tftypes.Object)
	targets := func(pairs ...[2]string) tftypes.Value {
		elems := make([]tftypes.Value, len(pairs))
		for i, p := range pairs {
			elems[i] = tftypes.NewValue(targetType, map[string]tftypes.Value{
				"kind": str(p[0]), "values": strs(strings.Split(p[1], ",")...),
			})
		}
		return tftypes.NewValue(targetsType, elems)
	}
	effectType := objType.AttributeTypes["effect"].(tftypes.Object)
	effect := func(typ string, deniedTools ...string) tftypes.Value {
		vals := map[string]tftypes.Value{}
		for name, at := range effectType.AttributeTypes {
			vals[name] = tftypes.NewValue(at, nil)
		}
		vals["type"] = str(typ)
		if len(deniedTools) > 0 {
			vals["denied_tools"] = strs(deniedTools...)
		}
		return tftypes.NewValue(effectType, vals)
	}
	instance := strs("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")

	cases := []struct {
		name    string
		attrs   map[string]tftypes.Value
		wantErr string
	}{
		{"instance only", map[string]tftypes.Value{"effect": effect("allow"), "provider_instances": instance}, ""},
		{"neither", map[string]tftypes.Value{"effect": effect("allow")}, "must name provider_instances, targets"},
		{"targets only", map[string]tftypes.Value{"effect": effect("deny"),
			"targets": targets([2]string{"package", "npm:@evil/mcp"})}, ""},
		{"allow on argv_contains", map[string]tftypes.Value{"effect": effect("allow"),
			"targets": targets([2]string{"argv_contains", "--yolo"})}, `An allow cannot name a "argv_contains"`},
		{"deny on argv_contains", map[string]tftypes.Value{"effect": effect("deny"),
			"targets": targets([2]string{"argv_contains", "--yolo"})}, ""},
		{"allow on class", map[string]tftypes.Value{"effect": effect("allow"),
			"targets": targets([2]string{"class", "remote"})}, `An allow cannot name a "class"`},
		{"unknown class", map[string]tftypes.Value{"effect": effect("deny"),
			"targets": targets([2]string{"class", "cloud"})}, "not a server class"},
		{"app bundle alone", map[string]tftypes.Value{"effect": effect("deny"),
			"targets": targets([2]string{"app_bundle_id", "com.example.app"})}, "need a server to narrow"},
		{"app bundle with an instance", map[string]tftypes.Value{"effect": effect("deny"), "provider_instances": instance,
			"targets": targets([2]string{"app_bundle_id", "com.example.app"})}, ""},
		{"app bundle with a host", map[string]tftypes.Value{"effect": effect("deny"),
			"targets": targets([2]string{"app_bundle_id", "com.example.app"}, [2]string{"host", "mcp.example.com"})}, ""},
		{"host with a scheme", map[string]tftypes.Value{"effect": effect("deny"),
			"targets": targets([2]string{"host", "https://mcp.example.com"})}, "not a bare hostname"},
		{"targets with tools", map[string]tftypes.Value{"effect": effect("deny", "delete_repo"),
			"targets": targets([2]string{"host", "mcp.example.com"})}, "cannot list tools or toolsets"},
		{"instance with tools", map[string]tftypes.Value{"effect": effect("deny", "delete_repo"),
			"provider_instances": instance}, ""},
		{"admit with tools", map[string]tftypes.Value{"effect": effect("deny", "delete_repo"),
			"provider_instances": instance, "applies_at": str("admit")}, "cannot list tools"},
		{"call with tools", map[string]tftypes.Value{"effect": effect("deny", "delete_repo"),
			"provider_instances": instance, "applies_at": str("call")}, ""},
		{"unknown targets", map[string]tftypes.Value{"effect": effect("deny"),
			"targets": tftypes.NewValue(targetsType, tftypes.UnknownValue)}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attrs := map[string]tftypes.Value{"name": str("p")}
			for k, v := range tc.attrs {
				attrs[k] = v
			}
			resp := &resource.ValidateConfigResponse{}
			(&MCPPolicyResource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{
				Config: tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, attrs)},
			}, resp)
			errs := resp.Diagnostics.Errors()
			if tc.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}
				return
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e.Summary(), tc.wantErr) {
					found = true
				}
			}
			if !found {
				t.Errorf("errors %v do not contain %q", errs, tc.wantErr)
			}
		})
	}
}
