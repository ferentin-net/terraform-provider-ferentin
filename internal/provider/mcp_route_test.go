package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

func mcpRouteSchema(t *testing.T) rschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	(&MCPRouteResource{}).Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// mcpRouteTargetValue builds the nested target object for a config/plan value.
func mcpRouteTargetValue(t *testing.T, s rschema.Schema, kind string, values ...string) tftypes.Value {
	t.Helper()
	objType := s.Type().TerraformType(context.Background()).(tftypes.Object)
	targetType := objType.AttributeTypes["target"].(tftypes.Object)
	elems := make([]tftypes.Value, len(values))
	for i, v := range values {
		elems[i] = tftypes.NewValue(tftypes.String, v)
	}
	return tftypes.NewValue(targetType, map[string]tftypes.Value{
		"kind":   tftypes.NewValue(tftypes.String, kind),
		"values": tftypes.NewValue(targetType.AttributeTypes["values"], elems),
	})
}

// One fully populated route, so a typo'd field in the mapper fails here.
func TestMCPRouteToModel_RoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	routeID := mustParseUUID(t, "3c1d2e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f")
	kind := gen.McpRouteTargetKindHost
	mech := gen.McpRouteMechanismRewriteToGateway
	route := &adminapi.MCPRoute{
		Id:             &routeID,
		Name:           strPtr("github-through-gateway"),
		Description:    strPtr("Send GitHub's MCP through the gateway"),
		Enabled:        boolPtr(false),
		Target:         &adminapi.MCPRouteTarget{Kind: &kind, Values: &[]string{"api.githubcopilot.com"}},
		Mechanism:      &mech,
		SteerToUrl:     strPtr("https://acme.mcp.ferentin.net/v1/github"),
		AppBundleIds:   &[]string{"com.anthropic.claudefordesktop"},
		DeviceGroupIds: &[]string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		CreatedAt:      &now,
		UpdatedAt:      &now,
		Version:        int64Ptr(4),
		ManagedBy:      strPtr("iac"),
		LastModifiedBy: strPtr("console"),
	}
	m := mcpRouteToModel(fixtureTenantID, route)

	checks := map[string][2]string{
		"ID":             {m.ID.ValueString(), fixtureTenantID + "/" + routeID.String()},
		"RouteID":        {m.RouteID.ValueString(), routeID.String()},
		"Name":           {m.Name.ValueString(), "github-through-gateway"},
		"Kind":           {m.Target.Kind.ValueString(), "host"},
		"Mechanism":      {m.Mechanism.ValueString(), "rewrite_to_gateway"},
		"SteerToURL":     {m.SteerToURL.ValueString(), "https://acme.mcp.ferentin.net/v1/github"},
		"ManagedBy":      {m.ManagedBy.ValueString(), "iac"},
		"LastModifiedBy": {m.LastModifiedBy.ValueString(), "console"},
		"CreatedAt":      {m.CreatedAt.ValueString(), now.Format(time.RFC3339)},
	}
	for field, gw := range checks {
		if gw[0] != gw[1] {
			t.Errorf("%s = %q, want %q", field, gw[0], gw[1])
		}
	}
	if m.Enabled.ValueBool() {
		t.Error("Enabled = true, want false")
	}
	if m.Version.ValueInt64() != 4 {
		t.Errorf("Version = %d, want 4", m.Version.ValueInt64())
	}
	if !m.Target.Values.Equal(stringList("api.githubcopilot.com")) || !m.StoredTargetValues.Equal(m.Target.Values) {
		t.Errorf("values = %v / stored %v", m.Target.Values, m.StoredTargetValues)
	}
	if len(m.DeviceGroupIDs.Elements()) != 1 || len(m.AppBundleIDs.Elements()) != 1 {
		t.Errorf("groups = %v, apps = %v", m.DeviceGroupIDs, m.AppBundleIDs)
	}
}

// The PUT is a full replace and reads an omitted enabled as true, so toWrite
// must always send enabled (it has a Default) and the whole target.
func TestMCPRouteToWrite(t *testing.T) {
	ctx := context.Background()
	m := MCPRouteResourceModel{
		Name:           types.StringValue("wrap-acme"),
		Description:    types.StringNull(),
		Enabled:        types.BoolValue(false),
		Target:         &MCPRouteTargetModel{Kind: types.StringValue("package"), Values: stringList("npm:@acme/mcp")},
		SteerToURL:     types.StringNull(),
		AppBundleIDs:   types.ListNull(types.StringType),
		DeviceGroupIDs: stringList("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
	}
	body, invalid := m.toWrite(ctx)
	if len(invalid) != 0 {
		t.Fatalf("invalid groups = %v", invalid)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["name"] != "wrap-acme" || got["enabled"] != false {
		t.Errorf("name/enabled = %v / %v", got["name"], got["enabled"])
	}
	tgt, _ := got["target"].(map[string]any)
	if tgt["kind"] != "package" || len(tgt["values"].([]any)) != 1 {
		t.Errorf("target = %v", got["target"])
	}
	if _, ok := got["steer_to_url"]; ok {
		t.Errorf("steer_to_url sent while unset: %v", got["steer_to_url"])
	}
	if groups, _ := got["device_group_ids"].([]any); len(groups) != 1 {
		t.Errorf("device_group_ids = %v", got["device_group_ids"])
	}

	// A device group that is not a UUID must be reported, not silently dropped:
	// an empty list means every device in the tenant.
	m.DeviceGroupIDs = stringList("contractors")
	if _, invalid := m.toWrite(ctx); len(invalid) != 1 || invalid[0] != "contractors" {
		t.Errorf("invalid = %v, want [contractors]", invalid)
	}
}

func TestReconcileRouteValues(t *testing.T) {
	configured := stringList("API.GitHubCopilot.com")
	stored := stringList("api.githubcopilot.com")
	prior := MCPRouteResourceModel{
		Target:             &MCPRouteTargetModel{Values: configured},
		StoredTargetValues: stored,
	}
	same := MCPRouteResourceModel{Target: &MCPRouteTargetModel{Values: stored}, StoredTargetValues: stored}
	if got := reconcileRouteValues(prior, same); !got.Equal(configured) {
		t.Errorf("unchanged server value: got %v, want the configured spelling", got)
	}
	edited := stringList("mcp.other.example")
	changed := MCPRouteResourceModel{Target: &MCPRouteTargetModel{Values: edited}, StoredTargetValues: edited}
	if got := reconcileRouteValues(prior, changed); !got.Equal(edited) {
		t.Errorf("console edit: got %v, want the server's list", got)
	}
	imported := MCPRouteResourceModel{StoredTargetValues: types.ListNull(types.StringType)}
	if got := reconcileRouteValues(imported, same); !got.Equal(stored) {
		t.Errorf("import: got %v, want the server's list", got)
	}
}

// ValidateConfig mirrors the cheap server checks. Each failing case has a
// passing neighbour, so a rule that fires too broadly fails too.
func TestMCPRouteValidateConfig(t *testing.T) {
	ctx := context.Background()
	s := mcpRouteSchema(t)
	str := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

	cases := []struct {
		name    string
		attrs   map[string]tftypes.Value
		wantErr string // substring of the error summary; "" = no error
	}{
		{"remote route with URL", map[string]tftypes.Value{
			"target":       mcpRouteTargetValue(t, s, "url_prefix", "https://mcp.example.com/sse"),
			"steer_to_url": str("https://acme.mcp.ferentin.net/v1/example")}, ""},
		{"remote route without URL", map[string]tftypes.Value{
			"target": mcpRouteTargetValue(t, s, "host", "mcp.example.com")}, ""},
		{"local route with URL", map[string]tftypes.Value{
			"target":       mcpRouteTargetValue(t, s, "package", "npm:@acme/mcp"),
			"steer_to_url": str("https://acme.mcp.ferentin.net/v1/example")}, "remote-only"},
		{"local route without URL", map[string]tftypes.Value{
			"target": mcpRouteTargetValue(t, s, "argv_contains", "server.mjs")}, ""},
		{"host with a scheme", map[string]tftypes.Value{
			"target": mcpRouteTargetValue(t, s, "host", "https://mcp.example.com")}, "not a bare hostname"},
		{"url_prefix may carry a scheme", map[string]tftypes.Value{
			"target": mcpRouteTargetValue(t, s, "url_prefix", "https://mcp.example.com/")}, ""},
	}
	for _, tc := range cases {
		cfg := tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, tc.attrs)}
		resp := &resource.ValidateConfigResponse{}
		(&MCPRouteResource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: cfg}, resp)
		if tc.wantErr == "" {
			if resp.Diagnostics.HasError() {
				t.Errorf("%s: unexpected error: %v", tc.name, resp.Diagnostics)
			}
			continue
		}
		if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), tc.wantErr) {
			t.Errorf("%s: diags = %v, want an error containing %q", tc.name, resp.Diagnostics, tc.wantErr)
		}
	}
}

func TestMCPRoute_NameValidatorRefusesSurroundingSpace(t *testing.T) {
	for name, want := range map[string]bool{"wrap": true, "wrap acme": true, "x": true, " wrap": false, "wrap ": false} {
		if got := mcpRouteNameShape.MatchString(name); got != want {
			t.Errorf("%q: match = %v, want %v", name, got, want)
		}
	}
}

func TestMCPRoute_EmptyFormsReadAsNullUnlessConfigured(t *testing.T) {
	m := mcpRouteToModel(fixtureTenantID, &adminapi.MCPRoute{
		Description:    strPtr(""),
		SteerToUrl:     strPtr(""),
		AppBundleIds:   &[]string{},
		DeviceGroupIds: &[]string{},
	})
	if !m.Description.IsNull() || !m.SteerToURL.IsNull() || !m.AppBundleIDs.IsNull() || !m.DeviceGroupIDs.IsNull() {
		t.Fatalf("empty forms = %v / %v / %v / %v, want all null", m.Description, m.SteerToURL, m.AppBundleIDs, m.DeviceGroupIDs)
	}
	empty := MCPRouteResourceModel{
		Description:    types.StringValue(""),
		SteerToURL:     types.StringNull(),
		AppBundleIDs:   types.ListValueMust(types.StringType, []attr.Value{}),
		DeviceGroupIDs: types.ListValueMust(types.StringType, []attr.Value{}),
	}
	got := m
	got.keepEmptyForms(empty)
	if got.Description.IsNull() || got.DeviceGroupIDs.IsNull() || got.AppBundleIDs.IsNull() {
		t.Errorf("config wrote the empty forms: got %v / %v / %v", got.Description, got.DeviceGroupIDs, got.AppBundleIDs)
	}
	if !got.SteerToURL.IsNull() {
		t.Errorf("steer_to_url = %v, want null", got.SteerToURL)
	}
}

// ModifyPlan plans mechanism from the target kind, so a create does not show
// it as "known after apply", and warns when a narrowing list is removed.
func TestMCPRouteModifyPlan(t *testing.T) {
	ctx := context.Background()
	s := mcpRouteSchema(t)
	objType := s.Type().TerraformType(ctx).(tftypes.Object)
	unknown := tftypes.NewValue(tftypes.String, tftypes.UnknownValue)

	for kind, want := range map[string]string{"package": "shim_wrap", "host": "rewrite_to_gateway"} {
		plan := tfsdk.Plan{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
			"name":      tftypes.NewValue(tftypes.String, "r"),
			"target":    mcpRouteTargetValue(t, s, kind, "v"),
			"mechanism": unknown,
		})}
		state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(objType, nil)}
		resp := &resource.ModifyPlanResponse{Plan: plan}
		(&MCPRouteResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", kind, resp.Diagnostics)
		}
		var got types.String
		resp.Plan.GetAttribute(ctx, path.Root("mechanism"), &got)
		if got.ValueString() != want {
			t.Errorf("%s: mechanism = %v, want %s", kind, got, want)
		}
	}

	groups := tftypes.NewValue(objType.AttributeTypes["device_group_ids"], []tftypes.Value{
		tftypes.NewValue(tftypes.String, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
	})
	state := tfsdk.State{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
		"name": tftypes.NewValue(tftypes.String, "contractors-through-gateway"), "device_group_ids": groups,
		"target": mcpRouteTargetValue(t, s, "host", "mcp.example.com"),
	})}
	plan := tfsdk.Plan{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
		"name":   tftypes.NewValue(tftypes.String, "contractors-through-gateway"),
		"target": mcpRouteTargetValue(t, s, "host", "mcp.example.com"),
	})}
	resp := &resource.ModifyPlanResponse{Plan: plan}
	(&MCPRouteResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan}, resp)
	if w := resp.Diagnostics.Warnings(); len(w) != 1 || !strings.Contains(w[0].Detail(), "every device in the tenant") {
		t.Errorf("warnings = %v, want one naming the widening", w)
	}
}
