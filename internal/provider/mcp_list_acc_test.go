package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Each list test creates one throwaway object and finds it in the list with
// the import ID its resource takes. depends_on makes the read wait for the
// create.

func TestAccMCPPolicies_list(t *testing.T) {
	suffix := randomSuffix(t)
	name := "tf-acc-mcp-pol-list-" + suffix
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerBlock() + fmt.Sprintf(`
resource "ferentin_mcp_policy" "test" {
  name     = %[1]q
  priority = 900
  mode     = "report_only"
  targets  = [{ kind = "package", values = ["npm:@tf-acc-%[2]s/never-published"] }]
  effect   = { type = "deny" }
}

data "ferentin_mcp_policies" "all" {
  depends_on = [ferentin_mcp_policy.test]
}
`, name, suffix),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckTypeSetElemNestedAttrs("data.ferentin_mcp_policies.all", "policies.*", map[string]string{
					"name":           name,
					"mode":           "report_only",
					"effect":         "deny",
					"managed_by":     "iac",
					"target_kinds.0": "package",
				}),
				resource.TestCheckTypeSetElemAttrPair("data.ferentin_mcp_policies.all", "policies.*.import_id",
					"ferentin_mcp_policy.test", "id"),
			),
		}},
	})
}

func TestAccMCPRoutes_list(t *testing.T) {
	suffix := randomSuffix(t)
	name := "tf-acc-mcp-route-list-" + suffix
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerBlock() + fmt.Sprintf(`
resource "ferentin_mcp_route" "test" {
  name   = %[1]q
  target = { kind = "package", values = ["npm:@tf-acc-%[2]s/never-published"] }
}

data "ferentin_mcp_routes" "all" {
  depends_on = [ferentin_mcp_route.test]
}
`, name, suffix),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckTypeSetElemNestedAttrs("data.ferentin_mcp_routes.all", "routes.*", map[string]string{
					"name":        name,
					"target_kind": "package",
					"mechanism":   "shim_wrap",
					"managed_by":  "iac",
				}),
				resource.TestCheckTypeSetElemAttrPair("data.ferentin_mcp_routes.all", "routes.*.import_id",
					"ferentin_mcp_route.test", "id"),
			),
		}},
	})
}

// A device group gets defaults of its own when it gets a posture row: here
// written through ferentin_mcp_scope_defaults itself.
func TestAccMCPScopeDefaultsScopes_list(t *testing.T) {
	suffix := randomSuffix(t)
	const ds = "data.ferentin_mcp_scope_defaults_scopes.all"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerBlock() + fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name   = "tf-acc-dg-scope-list-%[1]s"
  source = "manual"
}

resource "ferentin_mcp_scope_defaults" "group" {
  device_group_id = ferentin_device_group.test.group_id
  unattributed    = "block"
}

data "ferentin_mcp_scope_defaults_scopes" "all" {
  depends_on = [ferentin_mcp_scope_defaults.group]
}
`, suffix),
			Check: resource.ComposeAggregateTestCheckFunc(
				// The tenant default is always first.
				resource.TestCheckNoResourceAttr(ds, "scopes.0.device_group_id"),
				resource.TestCheckResourceAttrPair(ds, "scopes.0.import_id", "ferentin_mcp_scope_defaults.group", "tenant_id"),
				resource.TestCheckTypeSetElemNestedAttrs(ds, "scopes.*", map[string]string{
					"source":       "device_group",
					"unattributed": "block",
				}),
				resource.TestCheckTypeSetElemAttrPair(ds, "scopes.*.import_id", "ferentin_mcp_scope_defaults.group", "id"),
			),
		}},
	})
}
