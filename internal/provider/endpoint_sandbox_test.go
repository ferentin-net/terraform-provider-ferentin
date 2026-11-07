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
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

func sandboxSchema(t *testing.T) rschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewEndpointSandboxResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func sandboxAttrType(t *testing.T, s rschema.Schema, name string) tftypes.Type {
	t.Helper()
	return s.Type().TerraformType(context.Background()).(tftypes.Object).AttributeTypes[name]
}

func sbSet(vals ...string) tftypes.Value {
	elems := make([]tftypes.Value, len(vals))
	for i, v := range vals {
		elems[i] = tftypes.NewValue(tftypes.String, v)
	}
	return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elems)
}

// sbTools builds the tools map value: name -> {enabled, allow_unsandboxed}.
func sbTools(t *testing.T, s rschema.Schema, tools map[string][2]bool, reachUnknown bool) tftypes.Value {
	t.Helper()
	mt := sandboxAttrType(t, s, "tools").(tftypes.Map)
	ot := mt.ElementType.(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for k, v := range tools {
		reach := tftypes.NewValue(tftypes.String, nil)
		if reachUnknown {
			reach = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
		}
		vals[k] = tftypes.NewValue(ot, map[string]tftypes.Value{
			"enabled":           tftypes.NewValue(tftypes.Bool, v[0]),
			"allow_unsandboxed": tftypes.NewValue(tftypes.Bool, v[1]),
			"reach":             reach,
		})
	}
	return tftypes.NewValue(mt, vals)
}

func sandboxFS(t *testing.T, s rschema.Schema, writable, denied []string) tftypes.Value {
	t.Helper()
	ot := sandboxAttrType(t, s, "filesystem").(tftypes.Object)
	return tftypes.NewValue(ot, map[string]tftypes.Value{
		"writable": sbSet(writable...),
		"denied":   sbSet(denied...),
	})
}

func TestEndpointSandboxValidateConfig(t *testing.T) {
	s := sandboxSchema(t)
	for name, tc := range map[string]struct {
		set  map[string]tftypes.Value
		want string
	}{
		"bare tld":        {map[string]tftypes.Value{"allowed_domains": sbSet("localhost")}, `"localhost"`},
		"scheme":          {map[string]tftypes.Value{"allowed_domains": sbSet("https://pypi.org")}, `"https://pypi.org"`},
		"ip":              {map[string]tftypes.Value{"allowed_domains": sbSet("10.0.0.1")}, `"10.0.0.1"`},
		"relative folder": {map[string]tftypes.Value{"filesystem": sandboxFS(t, s, []string{"build"}, nil)}, `"build"`},
		"glob in folder":  {map[string]tftypes.Value{"filesystem": sandboxFS(t, s, nil, []string{"~/x*"})}, `"~/x*"`},
		"root denied":     {map[string]tftypes.Value{"filesystem": sandboxFS(t, s, nil, []string{"/"})}, `"/"`},
		"writable denied": {map[string]tftypes.Value{"filesystem": sandboxFS(t, s, []string{"~/.SSH/keys/"}, []string{"~/.ssh"})}, "denied folder"},
		"accepted, mixed": {map[string]tftypes.Value{"allowed_domains": sbSet("PyPI.org.", "*.githubusercontent.com"), "filesystem": sandboxFS(t, s, []string{"/opt/cache/"}, []string{"~/.ssh"})}, ""},
		"accepted, nofs":  {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, tc.set)}
			resp := &resource.ValidateConfigResponse{}
			(&EndpointSandboxResource{}).ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: cfg}, resp)
			errs := resp.Diagnostics.Errors()
			if tc.want == "" {
				if len(errs) != 0 {
					t.Fatalf("want no errors, got %v", errs)
				}
				return
			}
			if len(errs) != 1 || !strings.Contains(errs[0].Summary(), tc.want) {
				t.Fatalf("want one error containing %q, got %v", tc.want, errs)
			}
		})
	}
}

func sandboxSDK(t *testing.T, h http.HandlerFunc) *adminapi.SDKClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: srv.URL, Token: "test"})
	if err != nil {
		t.Fatalf("sdk: %v", err)
	}
	return sdk
}

func TestEndpointSandboxCreate_RefusesExistingPolicy(t *testing.T) {
	var puts int
	sdk := sandboxSDK(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"configured":true,"version":3,"lastModifiedBy":"console"}`))
	})
	s := sandboxSchema(t)
	plan := tfsdk.Plan{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
		"tools": sbTools(t, s, map[string][2]bool{"codex": {true, false}}, true),
	})}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: plan.Raw}}
	(&EndpointSandboxResource{sdk: sdk, tenantID: fixtureTenantID}).Create(context.Background(),
		resource.CreateRequest{Plan: plan}, resp)
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Summary(), "already has") ||
		!strings.Contains(errs[0].Detail(), "terraform import") || !strings.Contains(errs[0].Detail(), `"console"`) {
		t.Fatalf("want an import-instead error, got %v", errs)
	}
	if puts != 0 {
		t.Errorf("sent %d PUTs; the saved policy must be left alone", puts)
	}
}

// The first write carries no If-Match, sends the filesystem block with its
// denied list, and state keeps the configured spelling of what the platform
// normalizes.
func TestEndpointSandboxCreate_WritesAndKeepsSpelling(t *testing.T) {
	var putIfMatch string
	var body map[string]any
	sdk := sandboxSDK(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"configured":false,"includeDerivedDomains":true}`))
			return
		}
		putIfMatch = r.Header.Get("If-Match")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"configured":true,"version":1,"managedBy":"iac","lastModifiedBy":"iac",
		 "tools":{"claude-code":{"enabled":true,"allowUnsandboxed":false,"reach":"enforced"},
		          "codex":{"enabled":false,"allowUnsandboxed":false,"reach":"enforced"},
		          "copilot":{"enabled":false,"allowUnsandboxed":false,"reach":"enforced"},
		          "cursor":{"enabled":false,"allowUnsandboxed":false,"reach":"default_only"},
		          "antigravity":{"enabled":false,"allowUnsandboxed":false,"reach":"default_only"}},
		 "filesystem":{"writable":[],"denied":["~/.ssh"]},
		 "allowedDomains":["pypi.org"],"includeDerivedDomains":false}`))
	})
	s := sandboxSchema(t)
	// filesystem.writable unset in config.
	plan := tfsdk.Plan{Schema: s}
	plan.Raw = endpointRuleValue(t, s, map[string]tftypes.Value{
		"tools": sbTools(t, s, map[string][2]bool{"claude-code": {true, false}}, true),
		"filesystem": tftypes.NewValue(sandboxAttrType(t, s, "filesystem"), map[string]tftypes.Value{
			"writable": tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, nil),
			"denied":   sbSet("~/.ssh/"),
		}),
		"allowed_domains":         sbSet("PyPI.org."),
		"include_derived_domains": tftypes.NewValue(tftypes.Bool, false),
	})
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: plan.Raw}}
	(&EndpointSandboxResource{sdk: sdk, tenantID: fixtureTenantID}).Create(context.Background(),
		resource.CreateRequest{Plan: plan}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	if putIfMatch != "" {
		t.Errorf("first write sent If-Match %q; a never-saved tenant answers 412", putIfMatch)
	}
	fs, _ := body["filesystem"].(map[string]any)
	if fs == nil || fs["denied"] == nil {
		t.Errorf("filesystem.denied must be sent when the block is set; body = %v", body)
	}
	tools, _ := body["tools"].(map[string]any)
	if len(tools) != 1 || tools["claude-code"] == nil {
		t.Errorf("tools sent = %v, want only claude-code", tools)
	}

	var m EndpointSandboxResourceModel
	if d := resp.State.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d)
	}
	if got := m.AllowedDomains.Elements(); len(got) != 1 || got[0].(types.String).ValueString() != "PyPI.org." {
		t.Errorf("allowed_domains = %v, want the configured spelling", m.AllowedDomains)
	}
	if got := m.Filesystem.Denied.Elements(); len(got) != 1 || got[0].(types.String).ValueString() != "~/.ssh/" {
		t.Errorf("denied = %v, want the configured spelling", m.Filesystem.Denied)
	}
	if !m.Filesystem.Writable.IsNull() {
		t.Errorf("writable = %v, want null (unset in config)", m.Filesystem.Writable)
	}
	if n := len(m.Tools.Elements()); n != 1 {
		t.Errorf("tools in state = %d, want 1: disabled tools the config does not name stay out", n)
	}
	if m.Version.ValueInt64() != 1 || m.ID.ValueString() != fixtureTenantID {
		t.Errorf("version/id = %v/%v", m.Version, m.ID)
	}
}

// A refresh shows a tool the console enabled, and drops the resource when the
// tenant has no saved policy.
func TestEndpointSandboxRead(t *testing.T) {
	s := sandboxSchema(t)
	prior := endpointRuleValue(t, s, map[string]tftypes.Value{
		"tenant_id":       tftypes.NewValue(tftypes.String, fixtureTenantID),
		"tools":           sbTools(t, s, map[string][2]bool{"claude-code": {true, false}}, false),
		"allowed_domains": sbSet("PyPI.org"),
		"version":         tftypes.NewValue(tftypes.Number, 2),
	})

	t.Run("console enabled a tool, changed domains", func(t *testing.T) {
		sdk := sandboxSDK(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"configured":true,"version":3,
			 "tools":{"claude-code":{"enabled":true,"reach":"enforced"},"codex":{"enabled":true,"reach":"enforced"},
			          "cursor":{"enabled":false,"reach":"default_only"}},
			 "allowedDomains":["pypi.org","evil.example"],"includeDerivedDomains":true}`))
		})
		resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: prior}}
		(&EndpointSandboxResource{sdk: sdk, tenantID: fixtureTenantID}).Read(context.Background(),
			resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: prior}}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("read: %v", resp.Diagnostics)
		}
		var m EndpointSandboxResourceModel
		if d := resp.State.Get(context.Background(), &m); d.HasError() {
			t.Fatalf("state: %v", d)
		}
		if _, ok := m.Tools.Elements()["codex"]; !ok || len(m.Tools.Elements()) != 2 {
			t.Errorf("tools = %v, want claude-code and the console-enabled codex", m.Tools)
		}
		if len(m.AllowedDomains.Elements()) != 2 {
			t.Errorf("allowed_domains = %v, want the stored set so the drift shows", m.AllowedDomains)
		}
	})

	t.Run("never saved", func(t *testing.T) {
		sdk := sandboxSDK(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"configured":false}`))
		})
		resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: prior}}
		(&EndpointSandboxResource{sdk: sdk, tenantID: fixtureTenantID}).Read(context.Background(),
			resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: prior}}, resp)
		if !resp.State.Raw.IsNull() {
			t.Error("an unconfigured policy must leave state")
		}
	})
}

// Destroy saves a policy enabling no tool, under the state's version.
func TestEndpointSandboxDelete_DisablesEveryTool(t *testing.T) {
	var method, ifMatch string
	var body map[string]any
	sdk := sandboxSDK(t, func(w http.ResponseWriter, r *http.Request) {
		method, ifMatch = r.Method, r.Header.Get("If-Match")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"configured":true,"version":5}`))
	})
	s := sandboxSchema(t)
	st := tfsdk.State{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
		"tenant_id": tftypes.NewValue(tftypes.String, fixtureTenantID),
		"tools":     sbTools(t, s, map[string][2]bool{"codex": {true, true}}, false),
		"version":   tftypes.NewValue(tftypes.Number, 4),
	})}
	resp := &resource.DeleteResponse{State: st}
	(&EndpointSandboxResource{sdk: sdk, tenantID: fixtureTenantID}).Delete(context.Background(),
		resource.DeleteRequest{State: st}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
	if method != http.MethodPut || ifMatch != `W/"4"` {
		t.Errorf("%s If-Match=%q, want PUT W/\"4\"", method, ifMatch)
	}
	if tools, ok := body["tools"].(map[string]any); !ok || len(tools) != 0 {
		t.Errorf("tools = %v, want an empty map", body["tools"])
	}
	if _, ok := body["filesystem"]; ok {
		t.Error("destroy must not author a filesystem block")
	}
}
