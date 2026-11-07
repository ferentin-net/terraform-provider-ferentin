package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccMCPPolicy_deviceTargets covers the unified MCP policy's device half:
// a deny that names targets and no provider instance, with an exception.
//
//   - step 1 writes a mixed-case host. The platform stores it lowercased; the
//     framework's post-apply empty-plan check fails if that shows as drift.
//   - step 2 removes the exception and switches to report_only. The update
//     keeps an omitted unless, so this converges only because the provider
//     sends an exception with no groups.
//   - step 3 imports. An import has no configured spelling of targets.
//
// Every target names a host and package that do not exist, so nothing on a
// device matches them even if the tenant activates mid-run.
func TestAccMCPPolicy_deviceTargets(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_mcp_policy.targets"
	unless := `
  unless = {
    groups = [{
      operator = "AND"
      type     = "claims"
      conditions = [{
        field    = "groups"
        operator = "contains"
        value    = jsonencode("tf-acc-exempt")
      }]
    }]
  }
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configMCPPolicyTargets(suffix, "enforce", unless),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "targets.0.values.0", "MCP.TF-Acc-"+suffix+".Example"),
					resource.TestCheckResourceAttr(r, "stored_targets.0.values.0", "mcp.tf-acc-"+suffix+".example"),
					resource.TestCheckResourceAttr(r, "applies_at", "both"),
					resource.TestCheckResourceAttr(r, "mode", "enforce"),
					resource.TestCheckResourceAttr(r, "unless.groups.#", "1"),
					resource.TestCheckNoResourceAttr(r, "provider_instances.#"),
					resource.TestCheckResourceAttr(r, "managed_by", "iac"),
				),
			},
			{
				Config: configMCPPolicyTargets(suffix, "report_only", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "mode", "report_only"),
					resource.TestCheckNoResourceAttr(r, "unless.groups.#"),
				),
			},
			{
				ResourceName:            r,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"targets"},
			},
		},
	})
}

// TestAccMCPPolicy_clearsCriteria: the update keeps omitted criteria, so
// removing them from config converges only because the provider sends [].
func TestAccMCPPolicy_clearsCriteria(t *testing.T) {
	name := "tf-acc-mcp-pol-crit-" + randomSuffix(t)
	const r = "ferentin_mcp_policy.test"
	criteria := `
  criteria = [{
    operator = "AND"
    type     = "claims"
    conditions = [{
      field    = "groups"
      operator = "contains"
      value    = jsonencode("tf-acc")
    }]
  }]
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configMCPPolicyWithServer(name, criteria),
				Check:  resource.TestCheckResourceAttr(r, "criteria.#", "1"),
			},
			{
				Config: configMCPPolicyWithServer(name, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(r, "criteria.#"),
					resource.TestCheckNoResourceAttr(r, "description"),
				),
			},
		},
	})
}

// TestAccMCPRoute_basic covers create, update and import of a local route.
func TestAccMCPRoute_basic(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_mcp_route.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configMCPRoute(suffix, `  description = "initial"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "mechanism", "shim_wrap"),
					resource.TestCheckResourceAttr(r, "description", "initial"),
					resource.TestCheckResourceAttr(r, "managed_by", "iac"),
					resource.TestCheckResourceAttrSet(r, "version"),
				),
			},
			{
				Config: configMCPRoute(suffix, ""),
				Check:  resource.TestCheckNoResourceAttr(r, "description"),
			},
			{
				ResourceName:            r,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"target.values"},
			},
		},
	})
}

func configMCPPolicyTargets(suffix, mode, extra string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_mcp_policy" "targets" {
  name     = "tf-acc-mcp-pol-targets-%[1]s"
  priority = 900
  mode     = %[2]q

  targets = [
    { kind = "host", values = ["MCP.TF-Acc-%[1]s.Example"] },
    { kind = "package", values = ["npm:@tf-acc-%[1]s/never-published"] },
  ]

  effect = {
    type    = "deny"
    message = "Acceptance test policy"
  }
%[3]s
}
`, suffix, mode, extra)
}

func configMCPPolicyWithServer(name, extra string) string {
	return providerBlock() + mcpServerStack("policy_dep", name+"-srv") + fmt.Sprintf(`
resource "ferentin_mcp_policy" "test" {
  name               = %[1]q
  provider_instances = [ferentin_mcp_server.policy_dep.server_id]

  effect = {
    type = "allow"
  }
%[2]s
}
`, name, extra)
}

func configMCPRoute(suffix, extra string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_mcp_route" "test" {
  name = "tf-acc-mcp-route-%[1]s"

  target = {
    kind   = "package"
    values = ["npm:@tf-acc-%[1]s/never-published"]
  }
%[2]s
}
`, suffix, extra)
}
