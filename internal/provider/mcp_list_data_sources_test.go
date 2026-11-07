package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// mcpListServer serves the MCP lists out of order with mixed provenance.
// /mcp-policies is paged: pages[i] (raw policy JSON objects) is page i+1, and total is what every
// page reports.
func mcpListServer(t *testing.T, pages [][]string, total int, requests *int32) *adminapi.SDKClient {
	t.Helper()
	bodies := map[string]string{
		"/mcp-routes": `[
			{"id":"` + listRule2 + `","name":"zeta-route","enabled":true,"target":{"kind":"host","values":["mcp.example.com"]},
			 "mechanism":"rewrite_to_gateway","steer_to_url":"https://acme.mcp.ferentin.net/v1/mcp/gh","managed_by":"console","last_modified_by":"console"},
			{"id":"` + listRule1 + `","name":"alpha-route","target":{"kind":"package","values":["npm:@acme/mcp"]},
			 "mechanism":"shim_wrap","device_group_ids":["` + listGroupA + `"],"managed_by":"iac","last_modified_by":"iac"}]`,
		"/mcp-policies/defaults": `{"device_group_id":null,"source":"tenant","version":7,
			"server_approval":{"value":"block","mode":"report_only"},"remote_entry":{"value":"block","mode":"report_only"},
			"unattributed":{"value":"report_only","mode":"report_only"},"tool_vetting":{"mode":"enforce","mode_source":"tenant"}}`,
		"/mcp-policies/defaults/device-groups/" + listGroupA: `{"device_group_id":"` + listGroupA + `","source":"device_group","version":3,
			"server_approval":{"value":"quarantine","mode":"enforce"},"unattributed":{"value":"block","mode":"enforce"},
			"tool_vetting":{"mode":"inherit","mode_source":"tenant"}}`,
		"/endpoint-policy/settings": `[
			{"id":"44444444-4444-4444-8444-444444444444","deviceGroupId":"` + listGroupA + `","managedBy":"console","lastModifiedBy":"console"},
			{"id":"55555555-5555-4555-8555-555555555555","managedBy":"console","lastModifiedBy":"iac"}]`,
	}
	prefix := "/admin/tenants/" + fixtureTenantID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		sub := strings.TrimPrefix(r.URL.Path, prefix)
		w.Header().Set("Content-Type", "application/json")
		if sub == "/mcp-policies" {
			if requests != nil {
				atomic.AddInt32(requests, 1)
			}
			if r.URL.Query().Get("size") == "" {
				t.Errorf("policy list sent no page size")
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page < 1 || page > len(pages) {
				_, _ = fmt.Fprintf(w, `{"items":[],"pagination":{"page":%d,"total":%d,"hasNext":false}}`, page, total)
				return
			}
			_, _ = fmt.Fprintf(w, `{"items":[%s],"pagination":{"page":%d,"total":%d,"hasNext":%t}}`,
				strings.Join(pages[page-1], ","), page, total, page < len(pages))
			return
		}
		body, ok := bodies[sub]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: srv.URL, Token: "test"})
	if err != nil {
		t.Fatalf("sdk: %v", err)
	}
	return sdk
}

func policyJSON(id, name string, priority int, extra string) string {
	return fmt.Sprintf(`{"id":%q,"name":%q,"priority":%d,"effect":{"effect":"deny"},"managed_by":"console","last_modified_by":"console"%s}`,
		id, name, priority, extra)
}

func policyPages(pages ...[]string) [][]string { return pages }

func TestMCPPoliciesDataSource_ReadsEveryPage(t *testing.T) {
	p1 := policyJSON(listRule1, "remote", 50, `,"provider_instances":["`+listGroupB+`"],"mode":"enforce","applies_at":"both"`)
	p2 := policyJSON(listRule2, "block-local", 10, `,"targets":[{"kind":"package","values":["npm:@evil/x"]},{"kind":"app_bundle_id","values":["com.example"]}],"mode":"report_only"`)
	p3 := policyJSON(listRule3, "a-tie", 50, `,"sibling_of":"`+listRule1+`"`)
	var requests int32
	sdk := mcpListServer(t, policyPages([]string{p1, p2}, []string{p3}), 3, &requests)

	ds := &MCPPoliciesDataSource{endpointListDataSource{sdk: sdk, tenantID: fixtureTenantID}}
	var got MCPPoliciesDataSourceModel
	readList(t, ds, &got)

	if requests != 2 {
		t.Errorf("requests = %d, want 2 pages", requests)
	}
	if len(got.Policies) != 3 {
		t.Fatalf("got %d policies, want 3", len(got.Policies))
	}
	// priority, then name
	order := []string{got.Policies[0].Name.ValueString(), got.Policies[1].Name.ValueString(), got.Policies[2].Name.ValueString()}
	if strings.Join(order, ",") != "block-local,a-tie,remote" {
		t.Errorf("order = %v", order)
	}
	local := got.Policies[0]
	if local.ImportID.ValueString() != fixtureTenantID+"/"+listRule2 || local.Mode.ValueString() != "report_only" ||
		local.Effect.ValueString() != "deny" {
		t.Errorf("local = %+v", local)
	}
	if !local.TargetKinds.Equal(stringList("package", "app_bundle_id")) || len(local.ProviderInstances.Elements()) != 0 {
		t.Errorf("target_kinds = %v, provider_instances = %v", local.TargetKinds, local.ProviderInstances)
	}
	if got.Policies[1].SiblingOf.ValueString() != listRule1 {
		t.Errorf("sibling_of = %v", got.Policies[1].SiblingOf)
	}
	if !got.Policies[2].ProviderInstances.Equal(stringList(listGroupB)) || len(got.Policies[2].TargetKinds.Elements()) != 0 {
		t.Errorf("remote = %+v", got.Policies[2])
	}
}

// A row shifted across a page boundary by a concurrent write is read twice;
// it must be listed once.
func TestListAllMCPPolicies_DeduplicatesShiftedRows(t *testing.T) {
	p1 := policyJSON(listRule1, "one", 1, "")
	p2 := policyJSON(listRule2, "two", 2, "")
	sdk := mcpListServer(t, policyPages([]string{p1, p2}, []string{p2}), 2, nil)
	got, err := listAllMCPPolicies(t.Context(), sdk.MCPPolicies(), fixtureTenantID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d policies, want 2", len(got))
	}
}

// Fewer rows than the platform's total is an error, never a short list: an
// adoption config built from a short list would leave policies unmanaged.
func TestListAllMCPPolicies_ShortReadIsAnError(t *testing.T) {
	sdk := mcpListServer(t, policyPages([]string{policyJSON(listRule1, "one", 1, "")}), 2, nil)
	if _, err := listAllMCPPolicies(t.Context(), sdk.MCPPolicies(), fixtureTenantID); err == nil ||
		!strings.Contains(err.Error(), "reported 2") {
		t.Fatalf("err = %v, want a short-read error", err)
	}
}

func TestMCPRoutesDataSource_ListsByName(t *testing.T) {
	ds := &MCPRoutesDataSource{endpointListDataSource{sdk: mcpListServer(t, nil, 0, nil), tenantID: fixtureTenantID}}
	var got MCPRoutesDataSourceModel
	readList(t, ds, &got)
	if len(got.Routes) != 2 {
		t.Fatalf("got %d routes, want 2", len(got.Routes))
	}
	a, z := got.Routes[0], got.Routes[1]
	if a.Name.ValueString() != "alpha-route" || z.Name.ValueString() != "zeta-route" {
		t.Errorf("order = %s, %s", a.Name, z.Name)
	}
	if a.ImportID.ValueString() != fixtureTenantID+"/"+listRule1 || a.TargetKind.ValueString() != "package" ||
		a.Mechanism.ValueString() != "shim_wrap" || !a.Enabled.ValueBool() {
		t.Errorf("alpha = %+v", a)
	}
	if !a.DeviceGroupIDs.Equal(stringList(listGroupA)) || len(z.DeviceGroupIDs.Elements()) != 0 {
		t.Errorf("device_group_ids = %v / %v", a.DeviceGroupIDs, z.DeviceGroupIDs)
	}
	if !z.TargetValues.Equal(stringList("mcp.example.com")) || z.ManagedBy.ValueString() != "console" {
		t.Errorf("zeta = %+v", z)
	}
}

func TestMCPScopeDefaultsScopesDataSource_TenantThenGroups(t *testing.T) {
	ds := &MCPScopeDefaultsScopesDataSource{endpointListDataSource{sdk: mcpListServer(t, nil, 0, nil), tenantID: fixtureTenantID}}
	var got MCPScopeDefaultsScopesDataSourceModel
	readList(t, ds, &got)
	if len(got.Scopes) != 2 {
		t.Fatalf("got %d scopes, want the tenant default and one group", len(got.Scopes))
	}
	tenant, group := got.Scopes[0], got.Scopes[1]
	if !tenant.DeviceGroupID.IsNull() || tenant.ImportID.ValueString() != fixtureTenantID ||
		tenant.ServerApproval.ValueString() != "block" || tenant.ServerApprovalMode.ValueString() != "report_only" ||
		tenant.Version.ValueInt64() != 7 {
		t.Errorf("tenant = %+v", tenant)
	}
	if group.ImportID.ValueString() != fixtureTenantID+"/"+listGroupA || group.Source.ValueString() != "device_group" ||
		group.Unattributed.ValueString() != "block" || group.ToolVettingMode.ValueString() != "inherit" {
		t.Errorf("group = %+v", group)
	}
}
