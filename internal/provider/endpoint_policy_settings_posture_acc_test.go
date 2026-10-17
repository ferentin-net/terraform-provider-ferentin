package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// TestAccEndpointPolicySettings_postureFields covers the six keep-the-stored-
// value posture fields on a device-group row (so the tenant default is never
// touched). Each step relies on the framework's post-apply empty-plan check:
//
//  1. Non-canonical spellings (mixed-case hosts, a long-form IPv6, a CIDR with
//     host bits) must not leave a diff after the platform normalizes them.
//  2. Removed from config, with another field changed, all six keep their
//     values: the provider does not send them and the upsert keeps them.
//  3. A list configured again, then
//  4. changed outside Terraform, must show as drift.
//  5. An explicit [] / false clears them.
func TestAccEndpointPolicySettings_postureFields(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_endpoint_policy_settings.group"
	var groupID string
	cfg := func(fields string) string {
		return fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name   = "tf-acc-dg-posture-%[1]s"
  source = "manual"
}

resource "ferentin_endpoint_policy_settings" "group" {
  device_group_id       = ferentin_device_group.test.group_id
  unapproved_mcp_action = "report_only"
%[2]s
}
`, suffix, fields)
	}
	all := `
  default_destination_action = "allow"
  unapproved_agent_action    = "block"
  local_mcp_block_enabled    = true
  unnamed_flow_block_enabled = true
  unnamed_flow_exempt_cidrs  = ["10.1.2.3/8", "192.0.2.0/24"]
  doh_resolver_hosts         = ["DNS.Tf-Acc.Example.COM", "*.doh.tf-acc.example"]
  doh_resolver_ips           = ["2001:DB8:0:0:0:0:0:1", "192.0.2.10"]
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(all),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(r, "device_group_id", func(v string) error {
						groupID = v
						return nil
					}),
					resource.TestCheckResourceAttr(r, "unapproved_agent_action", "block"),
					resource.TestCheckResourceAttr(r, "local_mcp_block_enabled", "true"),
					resource.TestCheckResourceAttr(r, "unnamed_flow_block_enabled", "true"),
					resource.TestCheckResourceAttr(r, "unnamed_flow_exempt_cidrs.0", "10.1.2.3/8"),
					resource.TestCheckResourceAttr(r, "doh_resolver_hosts.0", "DNS.Tf-Acc.Example.COM"),
					resource.TestCheckResourceAttr(r, "doh_resolver_ips.0", "2001:DB8:0:0:0:0:0:1"),
				),
			},
			{
				Config: cfg(`  default_destination_action = "block"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "default_destination_action", "block"),
					resource.TestCheckResourceAttr(r, "unapproved_agent_action", "block"),
					resource.TestCheckResourceAttr(r, "local_mcp_block_enabled", "true"),
					resource.TestCheckResourceAttr(r, "unnamed_flow_exempt_cidrs.#", "2"),
					resource.TestCheckResourceAttr(r, "doh_resolver_hosts.0", "DNS.Tf-Acc.Example.COM"),
				),
			},
			{
				Config: cfg(`  doh_resolver_hosts = ["DNS.Tf-Acc.Example.COM"]`),
				Check:  resource.TestCheckResourceAttr(r, "doh_resolver_hosts.#", "1"),
			},
			{
				PreConfig: func() {
					ctx := context.Background()
					sdk := testAccSDK(t)
					tenantID := os.Getenv(envTenantID)
					row, err := sdk.EndpointPolicies().GetGroupSettings(ctx, tenantID, groupID)
					if err != nil {
						t.Fatalf("get group settings: %v", err)
					}
					_, err = sdk.EndpointPolicies().UpsertGroupSettings(ctx, tenantID, groupID,
						strconv.FormatInt(*row.Version, 10), adminapi.EndpointPolicySettingsWrite{
							DohResolverHosts: &[]string{"out-of-band.tf-acc.example"},
						})
					if err != nil {
						t.Fatalf("out-of-band update: %v", err)
					}
				},
				Config:             cfg(`  doh_resolver_hosts = ["DNS.Tf-Acc.Example.COM"]`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Applies the config over the out-of-band edit (refreshing first,
				// so the next steps and destroy hold the current version), and
				// clears everything.
				Config: cfg(`
  unapproved_agent_action    = "report_only"
  local_mcp_block_enabled    = false
  unnamed_flow_block_enabled = false
  unnamed_flow_exempt_cidrs  = []
  doh_resolver_hosts         = []
  doh_resolver_ips           = []
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "unapproved_agent_action", "report_only"),
					resource.TestCheckResourceAttr(r, "local_mcp_block_enabled", "false"),
					resource.TestCheckResourceAttr(r, "unnamed_flow_exempt_cidrs.#", "0"),
					resource.TestCheckResourceAttr(r, "doh_resolver_hosts.#", "0"),
					resource.TestCheckResourceAttr(r, "doh_resolver_ips.#", "0"),
				),
			},
			{
				// Import has no configured form, so an explicit [] reads back as
				// the platform's absent list.
				ResourceName:            r,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"unnamed_flow_exempt_cidrs", "doh_resolver_hosts", "doh_resolver_ips"},
			},
		},
	})
}
