package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

const testLocalServerID = "7f3d9b1c-2a4e-4c8f-9d0b-1e2f3a4b5c6d"

func localServerSchema(t *testing.T) rschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewMCPLocalServerResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func localServerValue(t *testing.T, set map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	objType := localServerSchema(t).Type().TerraformType(context.Background()).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name, typ := range objType.AttributeTypes {
		vals[name] = tftypes.NewValue(typ, nil)
	}
	for k, v := range set {
		vals[k] = v
	}
	return tftypes.NewValue(objType, vals)
}

func str(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

// localServerAPI is a fake admin API for one server. It records each write as
// "METHOD path If-Match" and answers with the server at the next version.
type localServerAPI struct {
	writes  []string
	bodies  []map[string]any
	version int
	status  string
	tmpl    []string
	code    int // when set, every write answers with it
	errBody string
}

func (f *localServerAPI) serve(t *testing.T) *adminapi.SDKClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/admin/tenants/"+fixtureTenantID+"/mcp-local-servers")
		if r.Method != http.MethodGet {
			f.writes = append(f.writes, r.Method+" "+path+" "+r.Header.Get("If-Match"))
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			f.bodies = append(f.bodies, body)
			if f.code != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(f.code)
				_, _ = w.Write([]byte(f.errBody))
				return
			}
			if s, ok := body["status"].(string); ok {
				f.status = s
			}
			if tm, ok := body["argv_template"].([]any); ok {
				f.tmpl = nil
				for _, x := range tm {
					f.tmpl = append(f.tmpl, x.(string))
				}
			}
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if r.Method == http.MethodPost {
				f.status, f.version = "approved", 0
			} else {
				f.version++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": testLocalServerID, "display_name": "Okta", "kind": "local", "declared_via": "template",
			"status": f.status, "argv_template": f.tmpl, "version": f.version,
			"managed_by": "iac", "last_modified_by": "iac",
		})
	}))
	t.Cleanup(srv.Close)
	sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: srv.URL, Token: "test"})
	if err != nil {
		t.Fatalf("sdk: %v", err)
	}
	return sdk
}

func TestMCPLocalServerValidateConfig(t *testing.T) {
	s := localServerSchema(t)
	tmpl := strList("uvx", "okta-mcp-server")
	for name, tc := range map[string]struct {
		set  map[string]tftypes.Value
		want string
	}{
		"template and catalog": {map[string]tftypes.Value{"argv_template": tmpl, "catalog_slug": str("local-okta")}, "mutually exclusive"},
		"catalog with a name":  {map[string]tftypes.Value{"catalog_slug": str("local-okta"), "display_name": str("Okta")}, "not used with catalog_slug"},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &resource.ValidateConfigResponse{}
			(&MCPLocalServerResource{}).ValidateConfig(context.Background(),
				resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: localServerValue(t, tc.set)}}, resp)
			if errs := resp.Diagnostics.Errors(); len(errs) != 1 || !strings.Contains(errs[0].Summary(), tc.want) {
				t.Errorf("want %q, got %v", tc.want, errs)
			}
		})
	}
}

func TestMCPLocalServerModifyPlan(t *testing.T) {
	ctx := context.Background()
	s := localServerSchema(t)
	nullState := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	run := func(st tfsdk.State, cfg tftypes.Value, plan tftypes.Value) *resource.ModifyPlanResponse {
		resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: plan}}
		(&MCPLocalServerResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{
			State: st, Config: tfsdk.Config{Schema: s, Raw: cfg}, Plan: tfsdk.Plan{Schema: s, Raw: plan}}, resp)
		return resp
	}

	t.Run("create needs a template or a catalog entry", func(t *testing.T) {
		v := localServerValue(t, map[string]tftypes.Value{"status": str("approved")})
		if errs := run(nullState, v, v).Diagnostics.Errors(); len(errs) != 1 || !strings.Contains(errs[0].Detail(), "import it") {
			t.Errorf("want the import hint, got %v", errs)
		}
	})
	t.Run("a template needs a display_name", func(t *testing.T) {
		v := localServerValue(t, map[string]tftypes.Value{"argv_template": strList("uvx", "okta-mcp-server")})
		if errs := run(nullState, v, v).Diagnostics.Errors(); len(errs) != 1 || !strings.Contains(errs[0].Summary(), "display_name") {
			t.Errorf("got %v", errs)
		}
	})
	nullPlan := tftypes.NewValue(s.Type().TerraformType(ctx), nil)
	t.Run("destroying a device-reported server says it stays", func(t *testing.T) {
		st := tfsdk.State{Schema: s, Raw: localServerValue(t, map[string]tftypes.Value{
			"display_name": str("filesystem"), "status": str("approved")})}
		if w := run(st, nullPlan, nullPlan).Diagnostics.Warnings(); len(w) != 1 || !strings.Contains(w[0].Summary(), "will not be deleted") {
			t.Errorf("got %v", w)
		}
	})
	t.Run("destroying a declared approval warns", func(t *testing.T) {
		st := tfsdk.State{Schema: s, Raw: localServerValue(t, map[string]tftypes.Value{
			"display_name": str("Okta"), "status": str("approved"), "declared_via": str("template")})}
		if w := run(st, nullPlan, nullPlan).Diagnostics.Warnings(); len(w) != 1 || !strings.Contains(w[0].Detail(), "unapproved_mcp_action") {
			t.Errorf("got %v", w)
		}
	})
}

// A configured blocked verdict is a second write, asserting the version the
// create returned.
func TestMCPLocalServerCreate_DeclaresThenSetsStatus(t *testing.T) {
	api := &localServerAPI{}
	s := localServerSchema(t)
	plan := tfsdk.Plan{Schema: s, Raw: localServerValue(t, map[string]tftypes.Value{
		"argv_template": strList("uv", "run", "--directory", "*", "okta-mcp-server"),
		"display_name":  str("Okta"), "status": str("blocked"), "reason": str("pilot"),
		"version": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
	})}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: plan.Raw}}
	(&MCPLocalServerResource{sdk: api.serve(t), tenantID: fixtureTenantID}).Create(context.Background(),
		resource.CreateRequest{Plan: plan}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	want := []string{"POST  ", "PUT /" + testLocalServerID + `/status W/"0"`}
	if strings.Join(api.writes, "|") != strings.Join(want, "|") {
		t.Errorf("writes = %q, want %q", api.writes, want)
	}
	if api.bodies[0]["display_name"] != "Okta" || api.bodies[1]["reason"] != "pilot" {
		t.Errorf("bodies = %v", api.bodies)
	}
	var got MCPLocalServerResourceModel
	resp.State.Get(context.Background(), &got)
	if got.Status.ValueString() != "blocked" || got.Version.ValueInt64() != 1 || got.Reason.ValueString() != "pilot" {
		t.Errorf("state = %s v%d reason %s", got.Status, got.Version.ValueInt64(), got.Reason)
	}
}

func TestMCPLocalServerCreate_ConflictSaysImport(t *testing.T) {
	api := &localServerAPI{code: http.StatusConflict, errBody: `{"error":{"code":"CONFLICT"}}`}
	s := localServerSchema(t)
	plan := tfsdk.Plan{Schema: s, Raw: localServerValue(t, map[string]tftypes.Value{"catalog_slug": str("local-playwright")})}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: plan.Raw}}
	(&MCPLocalServerResource{sdk: api.serve(t), tenantID: fixtureTenantID}).Create(context.Background(),
		resource.CreateRequest{Plan: plan}, resp)
	if errs := resp.Diagnostics.Errors(); len(errs) != 1 || !strings.Contains(errs[0].Detail(), "terraform import") {
		t.Errorf("got %v", errs)
	}
	if len(api.writes) != 1 || !strings.HasPrefix(api.writes[0], "POST /from-catalog") {
		t.Errorf("writes = %q", api.writes)
	}
}

// A template change and a status change in one apply: the status write must
// assert the version the template write returned, not the stale one.
func TestMCPLocalServerUpdate_ThreadsVersionAcrossWrites(t *testing.T) {
	api := &localServerAPI{version: 3, status: "approved", tmpl: []string{"uvx", "okta-mcp-server"}}
	s := localServerSchema(t)
	state := tfsdk.State{Schema: s, Raw: localServerValue(t, map[string]tftypes.Value{
		"tenant_id": str(fixtureTenantID), "server_id": str(testLocalServerID), "declared_via": str("template"),
		"argv_template": strList("uvx", "okta-mcp-server"), "status": str("approved"),
		"version": tftypes.NewValue(tftypes.Number, 3),
	})}
	plan := tfsdk.Plan{Schema: s, Raw: localServerValue(t, map[string]tftypes.Value{
		"tenant_id": str(fixtureTenantID), "server_id": str(testLocalServerID), "declared_via": str("template"),
		"argv_template": strList("uv", "run", "--directory", "*", "okta-mcp-server"), "status": str("blocked"),
		"version": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
	})}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: plan.Raw}}
	(&MCPLocalServerResource{sdk: api.serve(t), tenantID: fixtureTenantID}).Update(context.Background(),
		resource.UpdateRequest{State: state, Plan: plan}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	want := []string{
		"PUT /" + testLocalServerID + `/argv-template W/"3"`,
		"PUT /" + testLocalServerID + `/status W/"4"`,
	}
	if strings.Join(api.writes, "|") != strings.Join(want, "|") {
		t.Errorf("writes = %q, want %q", api.writes, want)
	}
}

// Destroying an imported server a device reported must not fail: the platform
// keeps it, and the provider stops managing it with a warning.
func TestMCPLocalServerDelete_DeviceReportedIsAWarning(t *testing.T) {
	api := &localServerAPI{code: http.StatusConflict, errBody: `{"error":{"code":"LOCAL_SERVER_NOT_DELETABLE"}}`}
	s := localServerSchema(t)
	state := tfsdk.State{Schema: s, Raw: localServerValue(t, map[string]tftypes.Value{
		"tenant_id": str(fixtureTenantID), "server_id": str(testLocalServerID),
		"version": tftypes.NewValue(tftypes.Number, 2),
	})}
	resp := &resource.DeleteResponse{State: state}
	(&MCPLocalServerResource{sdk: api.serve(t), tenantID: fixtureTenantID}).Delete(context.Background(),
		resource.DeleteRequest{State: state}, resp)
	if resp.Diagnostics.HasError() || len(resp.Diagnostics.Warnings()) != 1 {
		t.Errorf("want one warning and no error, got %v", resp.Diagnostics)
	}
	if len(api.writes) != 1 || api.writes[0] != "DELETE /"+testLocalServerID+` W/"2"` {
		t.Errorf("writes = %q", api.writes)
	}
}

func TestMCPLocalServersDataSource_ListsByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"id":"` + testLocalServerID + `","display_name":"zeta","kind":"local","status":"approved","declared_via":"template","managed_by":"console","last_modified_by":"console"},
			{"id":"8f3d9b1c-2a4e-4c8f-9d0b-1e2f3a4b5c6d","display_name":"alpha","kind":"local","status":"pending","managed_by":"system","last_modified_by":"system","packages":["npm:@acme/x"]}
		],"pagination":{"hasNext":false}}`))
	}))
	defer srv.Close()
	sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: srv.URL, Token: "test"})
	if err != nil {
		t.Fatalf("sdk: %v", err)
	}
	var got MCPLocalServersDataSourceModel
	readList(t, &MCPLocalServersDataSource{endpointListDataSource{sdk: sdk, tenantID: fixtureTenantID}}, &got)
	if len(got.Servers) != 2 || got.Servers[0].DisplayName.ValueString() != "alpha" {
		t.Fatalf("servers = %v", got.Servers)
	}
	a, z := got.Servers[0], got.Servers[1]
	if !a.DeclaredVia.IsNull() || a.ManagedBy.ValueString() != "system" || len(a.Packages.Elements()) != 1 {
		t.Errorf("device-reported server = %v", a)
	}
	if z.ImportID.ValueString() != fixtureTenantID+"/"+testLocalServerID || z.DeclaredVia.ValueString() != "template" {
		t.Errorf("declared server = %v", z)
	}
}
