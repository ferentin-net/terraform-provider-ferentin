package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccEndpointMCPRule_basic covers create, update and import of a block
// rule. Step two removes criteria_combinator = "OR": the platform keeps that
// field when a PUT omits it, so the plan only converges (the framework's
// post-apply empty-plan check) because the schema Default always sends it.
func TestAccEndpointMCPRule_basic(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_endpoint_mcp_rule.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configEndpointMCPRule(suffix, 10, `
  description         = "initial"
  criteria_combinator = "OR"
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "match_kind", "host"),
					resource.TestCheckResourceAttr(r, "action", "block"),
					resource.TestCheckResourceAttr(r, "priority", "10"),
					resource.TestCheckResourceAttr(r, "criteria_combinator", "OR"),
					resource.TestCheckResourceAttr(r, "device_group_ids.#", "1"),
					resource.TestCheckResourceAttr(r, "managed_by", "iac"),
					resource.TestCheckResourceAttrSet(r, "version"),
					resource.TestCheckResourceAttrSet(r, "enforcement"),
				),
			},
			{
				Config: configEndpointMCPRule(suffix, 20, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "priority", "20"),
					resource.TestCheckResourceAttr(r, "criteria_combinator", "AND"),
					resource.TestCheckNoResourceAttr(r, "description"),
				),
			},
			{
				ResourceName:      r,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccEndpointMCPRule_canonicalizedAllow: the platform canonicalizes the
// values of an allow rule. Writing a mixed-case host must not leave a perpetual
// diff — match_values keeps the configured spelling, stored_match_values the
// platform's. Import has no configured spelling, so it is ignored there.
func TestAccEndpointMCPRule_canonicalizedAllow(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_endpoint_mcp_rule.allow"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerBlock() + fmt.Sprintf(`
resource "ferentin_endpoint_mcp_rule" "allow" {
  name         = "tf-acc-mcp-allow-%s"
  match_kind   = "host"
  match_values = ["Docs.Tf-Acc.Example.COM"]
  action       = "allow"
}
`, suffix),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "match_values.0", "Docs.Tf-Acc.Example.COM"),
					resource.TestCheckResourceAttrSet(r, "stored_match_values.0"),
				),
			},
			{
				ResourceName:            r,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"match_values"},
			},
		},
	})
}

func configEndpointMCPRule(suffix string, priority int, extra string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name   = "tf-acc-dg-mcp-%[1]s"
  source = "manual"
}

resource "ferentin_endpoint_mcp_rule" "test" {
  name             = "tf-acc-mcp-rule-%[1]s"
  priority         = %[2]d
  match_kind       = "host"
  match_values     = ["mcp.tf-acc-%[1]s.example"]
  action           = "block"
  device_group_ids = [ferentin_device_group.test.group_id]
%[3]s
}
`, suffix, priority, extra)
}
