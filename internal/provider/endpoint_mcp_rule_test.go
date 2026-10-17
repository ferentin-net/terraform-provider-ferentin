package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

func stringList(vals ...string) types.List {
	elems := make([]attr.Value, len(vals))
	for i, v := range vals {
		elems[i] = types.StringValue(v)
	}
	return types.ListValueMust(types.StringType, elems)
}

// One fully populated rule, so a typo'd field in the mapper fails here. The
// provenance pair and enforcement get explicit checks: drift detection and the
// fleet signal depend on them landing in state.
func TestEndpointMCPRuleToModel_RoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	ruleID := mustParseUUID(t, "7f3d9b1c-2a4e-4c8f-9d0b-1e2f3a4b5c6d")
	groupID := mustParseUUID(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	enforcement := gen.EndpointMcpRuleResponseEnforcementPending

	rule := &adminapi.EndpointMCPRule{
		Id:                 &ruleID,
		Name:               strPtr("block-shell"),
		Description:        strPtr("No shell from agents"),
		Priority:           int32Ptr(10),
		Enabled:            boolPtr(true),
		MatchKind:          strPtr("tool_behavior"),
		MatchValues:        &[]string{"shell"},
		Action:             strPtr("block"),
		AppBundleIds:       &[]string{"com.anthropic.claudefordesktop"},
		DeviceGroupIds:     &[]adminapi.UUID{groupID},
		CriteriaCombinator: strPtr("OR"),
		Enforcement:        &enforcement,
		EnforcementReason:  strPtr("2 devices on an agent without call-time rules"),
		CreatedAt:          &now,
		UpdatedAt:          &now,
		Version:            int64Ptr(3),
		ManagedBy:          enumPtr(gen.EndpointMcpRuleResponseManagedByIac),
		ManagedByClientId:  strPtr("ferentin-iac-prod"),
		LastModifiedBy:     enumPtr(gen.EndpointMcpRuleResponseLastModifiedByConsole),
	}
	var diags diag.Diagnostics
	m := endpointMCPRuleToModel(fixtureTenantID, rule, &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}

	checks := map[string][2]string{
		"ID":                 {m.ID.ValueString(), fixtureTenantID + "/" + ruleID.String()},
		"RuleID":             {m.RuleID.ValueString(), ruleID.String()},
		"MatchKind":          {m.MatchKind.ValueString(), "tool_behavior"},
		"Action":             {m.Action.ValueString(), "block"},
		"CriteriaCombinator": {m.CriteriaCombinator.ValueString(), "OR"},
		"Enforcement":        {m.Enforcement.ValueString(), "pending"},
		"ManagedBy":          {m.ManagedBy.ValueString(), "iac"},
		"LastModifiedBy":     {m.LastModifiedBy.ValueString(), "console"},
	}
	for field, gw := range checks {
		if gw[0] != gw[1] {
			t.Errorf("%s = %q, want %q", field, gw[0], gw[1])
		}
	}
	if m.Priority.ValueInt64() != 10 || m.Version.ValueInt64() != 3 {
		t.Errorf("Priority/Version = %d/%d, want 10/3", m.Priority.ValueInt64(), m.Version.ValueInt64())
	}
	if !m.StoredMatchValues.Equal(stringList("shell")) || !m.MatchValues.Equal(stringList("shell")) {
		t.Errorf("match values = %v / stored %v, want [shell] for both", m.MatchValues, m.StoredMatchValues)
	}
	if !m.DeviceGroupIDs.Equal(stringList(groupID.String())) {
		t.Errorf("DeviceGroupIDs = %v", m.DeviceGroupIDs)
	}
}

func TestEndpointMCPRuleToWrite(t *testing.T) {
	ctx := context.Background()
	m := EndpointMCPRuleResourceModel{
		Name:               types.StringValue("allow-docs"),
		Action:             types.StringValue("allow"),
		MatchKind:          types.StringValue("host"),
		MatchValues:        stringList("Docs.Example.com"),
		Priority:           types.Int64Value(100),
		Enabled:            types.BoolValue(true),
		CriteriaCombinator: types.StringValue("AND"),
		Description:        types.StringUnknown(),
		SteerToURL:         types.StringNull(),
		AppBundleIDs:       types.ListUnknown(types.StringType),
		DeviceGroupIDs:     types.ListNull(types.StringType),
	}
	body, invalid, diags := m.toWrite(ctx)
	if diags.HasError() || len(invalid) != 0 {
		t.Fatalf("diags=%v invalid=%v", diags, invalid)
	}
	if body.Name != "allow-docs" || body.Action != "allow" || string(body.MatchKind) != "host" {
		t.Errorf("required fields = %q/%q/%q", body.Name, body.Action, body.MatchKind)
	}
	if len(body.MatchValues) != 1 || body.MatchValues[0] != "Docs.Example.com" {
		t.Errorf("MatchValues = %v, want the configured spelling", body.MatchValues)
	}
	// priority, enabled and criteria_combinator are kept by the platform when
	// omitted, so they must always be sent.
	if body.Priority == nil || body.Enabled == nil || body.CriteriaCombinator == nil {
		t.Errorf("priority/enabled/criteria_combinator must always be sent: %v/%v/%v",
			body.Priority, body.Enabled, body.CriteriaCombinator)
	}
	if body.Description != nil || body.AppBundleIds != nil || body.DeviceGroupIds != nil || body.Criteria != nil {
		t.Errorf("unset fields were sent: %+v", body)
	}

	// A malformed group id is reported, never dropped: an empty list targets the
	// whole tenant.
	m.DeviceGroupIDs = stringList("not-a-uuid")
	_, invalid, _ = m.toWrite(ctx)
	if len(invalid) != 1 || invalid[0] != "not-a-uuid" {
		t.Errorf("invalid = %v, want [not-a-uuid]", invalid)
	}
}

func TestReconcileMatchValues(t *testing.T) {
	configured := stringList("Docs.Example.com")
	canonical := stringList("docs.example.com")
	prior := EndpointMCPRuleResourceModel{MatchValues: configured, StoredMatchValues: canonical}

	unchanged := EndpointMCPRuleResourceModel{MatchValues: canonical, StoredMatchValues: canonical}
	if got := reconcileMatchValues(prior, unchanged); !got.Equal(configured) {
		t.Errorf("server unchanged: match_values = %v, want the configured %v", got, configured)
	}

	edited := stringList("docs.example.com", "api.example.com")
	console := EndpointMCPRuleResourceModel{MatchValues: edited, StoredMatchValues: edited}
	if got := reconcileMatchValues(prior, console); !got.Equal(edited) {
		t.Errorf("edited elsewhere: match_values = %v, want the server's %v so the plan shows drift", got, edited)
	}

	imported := EndpointMCPRuleResourceModel{
		MatchValues:       types.ListNull(types.StringType),
		StoredMatchValues: types.ListNull(types.StringType),
	}
	if got := reconcileMatchValues(imported, unchanged); !got.Equal(canonical) {
		t.Errorf("import: match_values = %v, want the server's %v", got, canonical)
	}
}

func endpointMCPRuleSchema(t *testing.T) rschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	(&EndpointMCPRuleResource{}).Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// ValidateConfig mirrors the platform's cross-field rules. Each failing case
// has a passing neighbour, so a rule that fires too broadly fails too.
func TestEndpointMCPRuleValidateConfig(t *testing.T) {
	ctx := context.Background()
	s := endpointMCPRuleSchema(t)
	objType := s.Type().TerraformType(ctx).(tftypes.Object)
	listOf := func(name string, vals ...string) tftypes.Value {
		elems := make([]tftypes.Value, len(vals))
		for i, v := range vals {
			elems[i] = tftypes.NewValue(tftypes.String, v)
		}
		return tftypes.NewValue(objType.AttributeTypes[name], elems)
	}
	str := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	criteriaGroup := tftypes.NewValue(endpointRuleCriteriaType(t, s), []tftypes.Value{
		tftypes.NewValue(endpointRuleCriteriaType(t, s).(tftypes.List).ElementType, map[string]tftypes.Value{
			"operator":    str("AND"),
			"type":        str("claims"),
			"description": tftypes.NewValue(tftypes.String, nil),
			"conditions": tftypes.NewValue(
				endpointRuleCriteriaType(t, s).(tftypes.List).ElementType.(tftypes.Object).AttributeTypes["conditions"],
				nil),
		}),
	})

	cases := []struct {
		name    string
		attrs   map[string]tftypes.Value
		wantErr string // substring of the error summary; "" = no error
	}{
		{"remote allow", map[string]tftypes.Value{
			"match_kind": str("host"), "action": str("allow"), "match_values": listOf("match_values", "a.example")}, ""},
		{"call-time allow", map[string]tftypes.Value{
			"match_kind": str("tool_name"), "action": str("allow"), "match_values": listOf("match_values", "run")},
			`supports action "block" only`},
		{"call-time block", map[string]tftypes.Value{
			"match_kind": str("command_binary"), "action": str("block"), "match_values": listOf("match_values", "curl")}, ""},
		{"unknown behaviour", map[string]tftypes.Value{
			"match_kind": str("tool_behavior"), "action": str("block"), "match_values": listOf("match_values", "network")},
			"not a known tool behaviour"},
		{"known behaviour", map[string]tftypes.Value{
			"match_kind": str("tool_behavior"), "action": str("block"), "match_values": listOf("match_values", "file_write")}, ""},
		{"remote steer without target", map[string]tftypes.Value{
			"match_kind": str("host"), "action": str("steer"), "match_values": listOf("match_values", "a.example")},
			"required on a remote"},
		{"remote steer with target", map[string]tftypes.Value{
			"match_kind": str("url_prefix"), "action": str("steer"), "match_values": listOf("match_values", "https://a.example/mcp"),
			"steer_to_url": str("https://acme.mcp.example.com/github")}, ""},
		{"local steer with target", map[string]tftypes.Value{
			"match_kind": str("package"), "action": str("steer"), "match_values": listOf("match_values", "npm:@acme/mcp"),
			"steer_to_url": str("https://acme.mcp.example.com/github")}, "remote-only"},
		{"target on a block rule", map[string]tftypes.Value{
			"match_kind": str("host"), "action": str("block"), "match_values": listOf("match_values", "a.example"),
			"steer_to_url": str("https://acme.mcp.example.com/github")}, `only meaningful on a "steer" rule`},
		{"local allow narrowed by groups", map[string]tftypes.Value{
			"match_kind": str("argv_contains"), "action": str("allow"), "match_values": listOf("match_values", "--safe"),
			"device_group_ids": listOf("device_group_ids", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")},
			"cannot be narrowed by device_group_ids"},
		{"local allow narrowed by criteria", map[string]tftypes.Value{
			"match_kind": str("package"), "action": str("allow"), "match_values": listOf("match_values", "npm:@acme/mcp"),
			"criteria": criteriaGroup}, "cannot be narrowed by criteria"},
		{"local block narrowed by groups", map[string]tftypes.Value{
			"match_kind": str("package"), "action": str("block"), "match_values": listOf("match_values", "npm:@evil/mcp"),
			"device_group_ids": listOf("device_group_ids", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")}, ""},
		{"unknown action skips action rules", map[string]tftypes.Value{
			"match_kind": str("tool_name"), "action": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			"match_values": listOf("match_values", "run")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attrs := map[string]tftypes.Value{"name": str("rule")}
			for k, v := range tc.attrs {
				attrs[k] = v
			}
			resp := &resource.ValidateConfigResponse{}
			(&EndpointMCPRuleResource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{
				Config: tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, attrs)},
			}, resp)
			errs := resp.Diagnostics.Errors()
			if tc.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatalf("want an error containing %q, got none", tc.wantErr)
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
