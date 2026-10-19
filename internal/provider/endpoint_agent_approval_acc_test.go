package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// testAccAgentSlug is a detection-catalog agent the test fleet does not run.
// Every step keeps the decision scoped to a device group the test creates and
// puts no device in, so it reaches no device: a group-scoped write does not
// stage a tenant-wide change either.
const testAccAgentSlug = "diffusionbee"

// TestAccEndpointAgentApproval_basic covers create, an in-place update of the
// action and description, and import.
func TestAccEndpointAgentApproval_basic(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_endpoint_agent_approval.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configEndpointAgentApproval(suffix, "report_only", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "catalog_slug", testAccAgentSlug),
					resource.TestCheckResourceAttr(r, "action", "report_only"),
					resource.TestCheckResourceAttr(r, "device_group_ids.#", "1"),
					resource.TestCheckResourceAttr(r, "managed_by", "iac"),
					resource.TestCheckResourceAttr(r, "last_modified_by", "iac"),
					resource.TestCheckResourceAttr(r, "scope_unreadable", "false"),
					resource.TestCheckResourceAttrSet(r, "approval_id"),
					resource.TestCheckResourceAttrSet(r, "version"),
				),
			},
			{
				Config: configEndpointAgentApproval(suffix, "block", `  description = "tf-acc: denied in an empty group"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "action", "block"),
					resource.TestCheckResourceAttr(r, "description", "tf-acc: denied in an empty group"),
					resource.TestCheckResourceAttr(r, "managed_by", "iac"),
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

// TestAccEndpointAgentApproval_refusesExistingDecision: the write is an upsert,
// so Create must check first. The tenant's decision for the slug, made in the
// console, must be left alone and the error must say to import it.
func TestAccEndpointAgentApproval_refusesExistingDecision(t *testing.T) {
	suffix := randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Create the decision outside the test resource's control, then
				// try to create it again.
				Config: configEndpointAgentApproval(suffix, "report_only", "") + fmt.Sprintf(`
resource "ferentin_endpoint_agent_approval" "again" {
  catalog_slug     = %q
  action           = "block"
  device_group_ids = [ferentin_device_group.test.group_id]
  depends_on       = [ferentin_endpoint_agent_approval.test]
}
`, testAccAgentSlug),
				ExpectError: regexp.MustCompile(`already has a decision`),
			},
		},
	})
}

func configEndpointAgentApproval(suffix, action, extra string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name   = "tf-acc-dg-agent-%[1]s"
  source = "manual"
}

resource "ferentin_endpoint_agent_approval" "test" {
  catalog_slug     = %[2]q
  action           = %[3]q
  device_group_ids = [ferentin_device_group.test.group_id]
%[4]s
}
`, suffix, testAccAgentSlug, action, extra)
}
