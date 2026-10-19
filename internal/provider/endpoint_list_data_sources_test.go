package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// The list data sources exist for adoption: they must return every object,
// whoever created it, with an import_id that the resource's import accepts, in
// the order devices evaluate rules. The fixture serves the lists out of order
// and mixes console- and iac-created objects.
const (
	listGroupA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	listGroupB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	listRule1  = "11111111-1111-4111-8111-111111111111"
	listRule2  = "22222222-2222-4222-8222-222222222222"
	listRule3  = "33333333-3333-4333-8333-333333333333"
)

func endpointListServer(t *testing.T) *adminapi.SDKClient {
	t.Helper()
	bodies := map[string]string{
		"/device-groups": `[
			{"groupId":"` + listGroupB + `","name":"zeta","source":"mdm","externalId":"jamf-7","managedBy":"iac","lastModifiedBy":"iac"},
			{"groupId":"` + listGroupA + `","name":"alpha","source":"manual","managedBy":"console","lastModifiedBy":"console"}]`,
		"/endpoint-policy/destination-rules": `[
			{"id":"` + listRule2 + `","name":"b-rule","priority":10,"action":"block","destinationKind":"host","destinationHosts":[".deepseek.com"],"managedBy":"iac","lastModifiedBy":"iac"},
			{"id":"` + listRule3 + `","name":"later","priority":40,"action":"allow","destinationKind":"ai_provider","catalogSlug":"anthropic","deviceGroupIds":["` + listGroupA + `"],"managedBy":"console","lastModifiedBy":"console"},
			{"id":"` + listRule1 + `","name":"a-rule","priority":10,"enabled":false,"action":"allow","destinationKind":"host","destinationHosts":["dp-g2.invalid"],"managedBy":"console","lastModifiedBy":"iac"}]`,
		"/endpoint-policy/settings": `[
			{"id":"44444444-4444-4444-8444-444444444444","deviceGroupId":"` + listGroupA + `","unapprovedMcpAction":"quarantine","defaultDestinationAction":"allow","hookRuleEnforcement":"enforce","managedBy":"console","lastModifiedBy":"console"},
			{"id":"55555555-5555-4555-8555-555555555555","unapprovedMcpAction":"report_only","defaultDestinationAction":"block","hookRuleEnforcement":"shadow","managedBy":"console","lastModifiedBy":"iac"}]`,
		"/endpoint-mcp-rules": `[
			{"id":"` + listRule2 + `","name":"local","priority":30,"action":"block","matchKind":"package","matchValues":["npm:@example/bad"],"enforcement":"active","managedBy":"console","lastModifiedBy":"console"},
			{"id":"` + listRule1 + `","name":"remote","priority":10,"action":"block","matchKind":"host","matchValues":["mcp.unvetted.example"],"managedBy":"iac","lastModifiedBy":"iac"}]`,
	}
	prefix := "/admin/tenants/" + fixtureTenantID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[strings.TrimPrefix(r.URL.Path, prefix)]
		if !ok || !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: srv.URL, Token: "test"})
	if err != nil {
		t.Fatalf("sdk: %v", err)
	}
	return sdk
}

// readList runs a data source's Read with no configuration and decodes the
// state into model.
func readList(t *testing.T, ds datasource.DataSource, model any) {
	t.Helper()
	ctx := context.Background()
	sresp := &datasource.SchemaResponse{}
	ds.Schema(ctx, datasource.SchemaRequest{}, sresp)
	if sresp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sresp.Diagnostics)
	}
	s := sresp.Schema
	objType := s.Type().TerraformType(ctx).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name, typ := range objType.AttributeTypes {
		vals[name] = tftypes.NewValue(typ, nil)
	}
	raw := tftypes.NewValue(objType, vals)

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	ds.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: s, Raw: raw}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	if d := resp.State.Get(ctx, model); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
}

func TestDeviceGroupsDataSource_ListsEveryGroupByName(t *testing.T) {
	ds := &DeviceGroupsDataSource{endpointListDataSource{sdk: endpointListServer(t), tenantID: fixtureTenantID}}
	var got DeviceGroupsDataSourceModel
	readList(t, ds, &got)

	if len(got.Groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(got.Groups))
	}
	a, z := got.Groups[0], got.Groups[1]
	if a.Name.ValueString() != "alpha" || z.Name.ValueString() != "zeta" {
		t.Errorf("order = %s, %s; want alpha, zeta", a.Name, z.Name)
	}
	if a.ImportID.ValueString() != fixtureTenantID+"/"+listGroupA {
		t.Errorf("import_id = %s, want <tenant>/<group_id>", a.ImportID)
	}
	if a.ManagedBy.ValueString() != "console" || z.ManagedBy.ValueString() != "iac" {
		t.Errorf("managed_by = %s, %s; want console, iac", a.ManagedBy, z.ManagedBy)
	}
	if z.ExternalID.ValueString() != "jamf-7" || !a.ExternalID.IsNull() {
		t.Errorf("external_id = %s, %s; want jamf-7 and null", z.ExternalID, a.ExternalID)
	}
}

func TestEndpointDestinationRulesDataSource_ListsInEvaluationOrder(t *testing.T) {
	ds := &EndpointDestinationRulesDataSource{endpointListDataSource{sdk: endpointListServer(t), tenantID: fixtureTenantID}}
	var got EndpointDestinationRulesDataSourceModel
	readList(t, ds, &got)

	var names []string
	for _, r := range got.Rules {
		names = append(names, r.Name.ValueString())
	}
	// Ascending priority; the two priority-10 rules by name.
	if strings.Join(names, ",") != "a-rule,b-rule,later" {
		t.Fatalf("order = %v, want a-rule, b-rule, later", names)
	}
	first, last := got.Rules[0], got.Rules[2]
	if first.ImportID.ValueString() != fixtureTenantID+"/"+listRule1 {
		t.Errorf("import_id = %s, want <tenant>/<rule_id>", first.ImportID)
	}
	if first.Enabled.ValueBool() || !got.Rules[1].Enabled.ValueBool() {
		t.Errorf("enabled = %v, %v; want false, and true when the platform omits it", first.Enabled, got.Rules[1].Enabled)
	}
	if first.ManagedBy.ValueString() != "console" || first.LastModifiedBy.ValueString() != "iac" {
		t.Errorf("provenance = %s/%s, want console/iac", first.ManagedBy, first.LastModifiedBy)
	}
	if last.CatalogSlug.ValueString() != "anthropic" || len(last.DeviceGroupIDs.Elements()) != 1 {
		t.Errorf("catalog_slug = %s, device_group_ids = %v", last.CatalogSlug, last.DeviceGroupIDs)
	}
	if !first.DeviceGroupIDs.IsNull() {
		t.Errorf("device_group_ids = %v, want null (every device)", first.DeviceGroupIDs)
	}
}

func TestEndpointMCPRulesDataSource_ListsInEvaluationOrder(t *testing.T) {
	ds := &EndpointMCPRulesDataSource{endpointListDataSource{sdk: endpointListServer(t), tenantID: fixtureTenantID}}
	var got EndpointMCPRulesDataSourceModel
	readList(t, ds, &got)

	if len(got.Rules) != 2 || got.Rules[0].Name.ValueString() != "remote" {
		t.Fatalf("rules = %v, want remote (10) before local (30)", got.Rules)
	}
	local := got.Rules[1]
	if local.MatchKind.ValueString() != "package" || local.Enforcement.ValueString() != "active" {
		t.Errorf("match_kind = %s, enforcement = %s", local.MatchKind, local.Enforcement)
	}
	if local.ImportID.ValueString() != fixtureTenantID+"/"+listRule2 || local.ManagedBy.ValueString() != "console" {
		t.Errorf("import_id = %s, managed_by = %s", local.ImportID, local.ManagedBy)
	}
}

func TestEndpointPolicySettingsScopesDataSource_TenantDefaultFirst(t *testing.T) {
	ds := &EndpointPolicySettingsScopesDataSource{endpointListDataSource{sdk: endpointListServer(t), tenantID: fixtureTenantID}}
	var got EndpointPolicySettingsScopesDataSourceModel
	readList(t, ds, &got)

	if len(got.Scopes) != 2 {
		t.Fatalf("got %d scopes, want 2", len(got.Scopes))
	}
	def, grp := got.Scopes[0], got.Scopes[1]
	if !def.DeviceGroupID.IsNull() || def.ImportID.ValueString() != fixtureTenantID {
		t.Errorf("first scope = %s / %s, want the tenant default with import_id <tenant_id>", def.DeviceGroupID, def.ImportID)
	}
	if grp.ImportID.ValueString() != fixtureTenantID+"/"+listGroupA || grp.HookRuleEnforcement.ValueString() != "enforce" {
		t.Errorf("override = %s, hook_rule_enforcement = %s", grp.ImportID, grp.HookRuleEnforcement)
	}
	if def.DefaultDestinationAction.ValueString() != "block" || def.LastModifiedBy.ValueString() != "iac" {
		t.Errorf("default_destination_action = %s, last_modified_by = %s", def.DefaultDestinationAction, def.LastModifiedBy)
	}
}
