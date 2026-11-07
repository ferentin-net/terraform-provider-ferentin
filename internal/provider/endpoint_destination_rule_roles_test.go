package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

func rolesSet(vals ...string) tftypes.Value {
	elems := make([]tftypes.Value, len(vals))
	for i, v := range vals {
		elems[i] = tftypes.NewValue(tftypes.String, v)
	}
	return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elems)
}

func TestEndpointRuleValidateConfig_CatalogRoles(t *testing.T) {
	ctx := context.Background()
	s := endpointRuleSchema(t)
	for name, tc := range map[string]struct {
		set     map[string]tftypes.Value
		wantErr bool
	}{
		"ai_provider with roles": {map[string]tftypes.Value{
			"catalog_slug":  tftypes.NewValue(tftypes.String, "claude-code"),
			"catalog_roles": rolesSet("model_api"),
		}, false},
		"host rule with roles": {map[string]tftypes.Value{
			"destination_kind":  tftypes.NewValue(tftypes.String, "host"),
			"destination_hosts": strList("api.example.com"),
			"catalog_roles":     rolesSet("model_api"),
		}, true},
		"host rule with empty roles": {map[string]tftypes.Value{
			"destination_kind":  tftypes.NewValue(tftypes.String, "host"),
			"destination_hosts": strList("api.example.com"),
			"catalog_roles":     rolesSet(),
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			set := map[string]tftypes.Value{
				"name":   tftypes.NewValue(tftypes.String, "r"),
				"action": tftypes.NewValue(tftypes.String, "block"),
			}
			for k, v := range tc.set {
				set[k] = v
			}
			cfg := tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, set)}
			resp := &resource.ValidateConfigResponse{}
			(&EndpointDestinationRuleResource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: cfg}, resp)
			got := false
			for _, e := range resp.Diagnostics.Errors() {
				if strings.Contains(e.Summary(), "catalog_roles") {
					got = true
				}
			}
			if got != tc.wantErr {
				t.Errorf("catalog_roles error = %v, want %v (%v)", got, tc.wantErr, resp.Diagnostics)
			}
		})
	}
}

// catalog_roles is always sent, as [] when unset: the platform keeps stored
// roles when an update omits the field, so removing it from config must still
// clear them. test_rule always carries its default.
func TestEndpointRuleToWrite_CatalogRolesAndTestRule(t *testing.T) {
	ctx := context.Background()
	m := EndpointDestinationRuleResourceModel{
		Name: types.StringValue("r"), Action: types.StringValue("block"),
		CatalogRoles: types.SetNull(types.StringType),
		TestRule:     types.BoolValue(false),
	}
	body, _, d := m.toWrite(ctx)
	if d.HasError() {
		t.Fatalf("toWrite: %v", d)
	}
	if body.CatalogRoles == nil || len(*body.CatalogRoles) != 0 {
		t.Errorf("CatalogRoles = %v, want an explicit empty list", body.CatalogRoles)
	}
	if body.TestRule == nil || *body.TestRule {
		t.Errorf("TestRule = %v, want false sent", body.TestRule)
	}

	m.CatalogRoles = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("updater")})
	body, _, _ = m.toWrite(ctx)
	if body.CatalogRoles == nil || len(*body.CatalogRoles) != 1 || (*body.CatalogRoles)[0] != "updater" {
		t.Errorf("CatalogRoles = %v, want [updater]", body.CatalogRoles)
	}
}

// The platform answers no roles as null; an explicit empty set in config stays
// in state so the plan converges.
func TestEndpointRuleToModel_CatalogRoles(t *testing.T) {
	var d diag.Diagnostics
	roles := []string{"model_api", "app"}
	yes := true
	got := endpointRuleToModel(fixtureTenantID, &adminapi.EndpointDestinationRule{CatalogRoles: &roles, TestRule: &yes}, &d)
	if len(got.CatalogRoles.Elements()) != 2 || !got.TestRule.ValueBool() {
		t.Errorf("roles/test_rule = %v/%v", got.CatalogRoles, got.TestRule)
	}

	none := endpointRuleToModel(fixtureTenantID, &adminapi.EndpointDestinationRule{}, &d)
	if !none.CatalogRoles.IsNull() || none.TestRule.ValueBool() {
		t.Errorf("unset: roles/test_rule = %v/%v, want null/false", none.CatalogRoles, none.TestRule)
	}
	none.keepEmptyForms(EndpointDestinationRuleResourceModel{
		CatalogRoles: types.SetValueMust(types.StringType, []attr.Value{}),
	})
	if none.CatalogRoles.IsNull() {
		t.Error("an explicit empty catalog_roles must stay in state")
	}
}
