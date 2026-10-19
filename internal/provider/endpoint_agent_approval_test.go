package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

func agentApprovalSchema(t *testing.T) tfsdk.Plan {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewEndpointAgentApprovalResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return tfsdk.Plan{Schema: resp.Schema}
}

// agentApprovalValue builds a full object value with the given attributes set
// and the rest null.
func agentApprovalValue(t *testing.T, set map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	s := agentApprovalSchema(t).Schema
	objType := s.Type().TerraformType(context.Background()).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name, typ := range objType.AttributeTypes {
		vals[name] = tftypes.NewValue(typ, nil)
	}
	for k, v := range set {
		vals[k] = v
	}
	return tftypes.NewValue(objType, vals)
}

func strList(vals ...string) tftypes.Value {
	elems := make([]tftypes.Value, len(vals))
	for i, v := range vals {
		elems[i] = tftypes.NewValue(tftypes.String, v)
	}
	return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, elems)
}

func TestEndpointAgentApprovalValidateConfig_RefusesGroupNames(t *testing.T) {
	s := agentApprovalSchema(t).Schema
	cfg := tfsdk.Config{Schema: s, Raw: agentApprovalValue(t, map[string]tftypes.Value{
		"catalog_slug":     tftypes.NewValue(tftypes.String, "openclaw"),
		"action":           tftypes.NewValue(tftypes.String, "block"),
		"device_group_ids": strList("11111111-1111-4111-8111-111111111111", "contractors"),
	})}
	resp := &resource.ValidateConfigResponse{}
	(&EndpointAgentApprovalResource{}).ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: cfg}, resp)
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Summary(), `"contractors"`) {
		t.Errorf("want one error naming the group name, got %v", errs)
	}
}

func TestEndpointAgentApprovalModifyPlan(t *testing.T) {
	ctx := context.Background()
	s := agentApprovalSchema(t).Schema
	state := func(set map[string]tftypes.Value) tfsdk.State {
		base := map[string]tftypes.Value{
			"catalog_slug": tftypes.NewValue(tftypes.String, "claude-code"),
			"version":      tftypes.NewValue(tftypes.Number, 4),
		}
		for k, v := range set {
			base[k] = v
		}
		return tfsdk.State{Schema: s, Raw: agentApprovalValue(t, base)}
	}
	run := func(st tfsdk.State, plan tftypes.Value) *resource.ModifyPlanResponse {
		resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: plan}}
		(&EndpointAgentApprovalResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{
			State: st, Plan: tfsdk.Plan{Schema: s, Raw: plan}}, resp)
		return resp
	}
	nullPlan := tftypes.NewValue(s.Type().TerraformType(ctx), nil)

	t.Run("destroying an approval warns", func(t *testing.T) {
		resp := run(state(map[string]tftypes.Value{"action": tftypes.NewValue(tftypes.String, "report_only")}), nullPlan)
		if w := resp.Diagnostics.Warnings(); len(w) != 1 || !strings.Contains(w[0].Detail(), "DENIES") {
			t.Errorf("want the deny-on-destroy warning, got %v", w)
		}
	})
	t.Run("destroying a block does not", func(t *testing.T) {
		resp := run(state(map[string]tftypes.Value{"action": tftypes.NewValue(tftypes.String, "block")}), nullPlan)
		if w := resp.Diagnostics.Warnings(); len(w) != 0 {
			t.Errorf("unexpected warning %v", w)
		}
	})
	t.Run("removing device_group_ids warns", func(t *testing.T) {
		st := state(map[string]tftypes.Value{
			"action":           tftypes.NewValue(tftypes.String, "block"),
			"device_group_ids": strList("11111111-1111-4111-8111-111111111111"),
		})
		plan := agentApprovalValue(t, map[string]tftypes.Value{
			"catalog_slug": tftypes.NewValue(tftypes.String, "claude-code"),
			"action":       tftypes.NewValue(tftypes.String, "block"),
			"version":      tftypes.NewValue(tftypes.Number, 4),
		})
		if w := run(st, plan).Diagnostics.Warnings(); len(w) != 1 || !strings.Contains(w[0].Detail(), "every device") {
			t.Errorf("want the widening warning, got %v", w)
		}
	})
	t.Run("an unreadable scope forces an update", func(t *testing.T) {
		st := state(map[string]tftypes.Value{
			"action":           tftypes.NewValue(tftypes.String, "block"),
			"scope_unreadable": tftypes.NewValue(tftypes.Bool, true),
		})
		plan := agentApprovalValue(t, map[string]tftypes.Value{
			"catalog_slug":     tftypes.NewValue(tftypes.String, "claude-code"),
			"action":           tftypes.NewValue(tftypes.String, "block"),
			"version":          tftypes.NewValue(tftypes.Number, 4),
			"scope_unreadable": tftypes.NewValue(tftypes.Bool, true),
		})
		resp := run(st, plan)
		var m EndpointAgentApprovalResourceModel
		if d := resp.Plan.Get(ctx, &m); d.HasError() {
			t.Fatalf("plan: %v", d)
		}
		if !m.Version.IsUnknown() {
			t.Error("version must be unknown so the apply rewrites the scope")
		}
		if w := resp.Diagnostics.Warnings(); len(w) != 1 || !strings.Contains(w[0].Summary(), "no device") {
			t.Errorf("want the in-force-nowhere warning, got %v", w)
		}
	})
}

// Create must not overwrite a decision that already exists: the platform's
// write is an upsert, so the provider checks first and sends no PUT.
func TestEndpointAgentApprovalCreate_RefusesExistingDecision(t *testing.T) {
	var puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"catalogSlug":"cursor","action":"report_only","managedBy":"console","version":2}]`))
	}))
	defer srv.Close()
	sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: srv.URL, Token: "test"})
	if err != nil {
		t.Fatalf("sdk: %v", err)
	}

	s := agentApprovalSchema(t).Schema
	plan := tfsdk.Plan{Schema: s, Raw: agentApprovalValue(t, map[string]tftypes.Value{
		"catalog_slug": tftypes.NewValue(tftypes.String, "cursor"),
		"action":       tftypes.NewValue(tftypes.String, "block"),
	})}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: plan.Raw}}
	(&EndpointAgentApprovalResource{sdk: sdk, tenantID: fixtureTenantID}).Create(context.Background(),
		resource.CreateRequest{Plan: plan}, resp)

	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Summary(), "already has a decision") ||
		!strings.Contains(errs[0].Detail(), "terraform import") || !strings.Contains(errs[0].Detail(), `"console"`) {
		t.Fatalf("want an import-instead error, got %v", errs)
	}
	if puts != 0 {
		t.Errorf("sent %d PUTs; the existing decision must be left alone", puts)
	}
}
