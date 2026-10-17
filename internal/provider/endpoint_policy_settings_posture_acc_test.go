package provider

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

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
					resource.TestCheckTypeSetElemAttr(r, "unnamed_flow_exempt_cidrs.*", "10.1.2.3/8"),
					resource.TestCheckTypeSetElemAttr(r, "doh_resolver_hosts.*", "DNS.Tf-Acc.Example.COM"),
					resource.TestCheckTypeSetElemAttr(r, "doh_resolver_ips.*", "2001:DB8:0:0:0:0:0:1"),
					// The platform stored them, normalized: state alone would show the
					// configured values even if nothing had been stored.
					checkGroupPostureOnPlatform(t, &groupID, func(row *adminapi.EndpointPolicySettings) error {
						if row.DohResolverHosts == nil || len(*row.DohResolverHosts) != 2 ||
							!slices.Contains(*row.DohResolverHosts, "dns.tf-acc.example.com") {
							return fmt.Errorf("doh_resolver_hosts on the platform = %v", row.DohResolverHosts)
						}
						if row.UnnamedFlowExemptCidrs == nil || !slices.Contains(*row.UnnamedFlowExemptCidrs, "10.0.0.0/8") {
							return fmt.Errorf("unnamed_flow_exempt_cidrs on the platform = %v", row.UnnamedFlowExemptCidrs)
						}
						if row.DohResolverIps == nil || len(*row.DohResolverIps) != 2 {
							return fmt.Errorf("doh_resolver_ips on the platform = %v", row.DohResolverIps)
						}
						return nil
					}),
				),
			},
			{
				Config: cfg(`  default_destination_action = "block"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "default_destination_action", "block"),
					resource.TestCheckResourceAttr(r, "unapproved_agent_action", "block"),
					resource.TestCheckResourceAttr(r, "local_mcp_block_enabled", "true"),
					resource.TestCheckResourceAttr(r, "unnamed_flow_exempt_cidrs.#", "2"),
					resource.TestCheckTypeSetElemAttr(r, "doh_resolver_hosts.*", "DNS.Tf-Acc.Example.COM"),
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
					// State holds the planned [] whatever happened, so confirm the
					// platform actually cleared them.
					checkGroupPostureOnPlatform(t, &groupID, func(row *adminapi.EndpointPolicySettings) error {
						for name, l := range map[string]*[]string{"doh_resolver_hosts": row.DohResolverHosts,
							"doh_resolver_ips": row.DohResolverIps, "unnamed_flow_exempt_cidrs": row.UnnamedFlowExemptCidrs} {
							if l != nil && len(*l) > 0 {
								return fmt.Errorf("%s still on the platform after the clear: %v", name, *l)
							}
						}
						if row.LocalMcpBlockEnabled == nil || *row.LocalMcpBlockEnabled {
							return fmt.Errorf("local_mcp_block_enabled on the platform = %v", row.LocalMcpBlockEnabled)
						}
						return nil
					}),
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

// TestAccEndpointPolicySettings_approvalConfig covers local_mcp_approval_config
// on a device-group row: set, kept when removed from config, replaced, and
// imported with nothing ignored, which holds only if the platform's config
// reads back in the form jsonencode writes.
func TestAccEndpointPolicySettings_approvalConfig(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_endpoint_policy_settings.group"
	cfg := func(fields string) string {
		return fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name   = "tf-acc-dg-approval-%[1]s"
  source = "manual"
}

resource "ferentin_endpoint_policy_settings" "group" {
  device_group_id       = ferentin_device_group.test.group_id
  unapproved_mcp_action = "report_only"
%[2]s
}
`, suffix, fields)
	}
	approval := func(timeout int) string {
		return fmt.Sprintf(`
  local_mcp_approval_config = jsonencode({
    step_up_mode    = "approve"
    timeout_seconds = %d
    channels        = [{ type = "invocation_context" }]
  })
`, timeout)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(approval(900)),
				Check: resource.TestCheckResourceAttr(r, "local_mcp_approval_config",
					`{"channels":[{"type":"invocation_context"}],"step_up_mode":"approve","timeout_seconds":900}`),
			},
			{
				Config: cfg(`  default_destination_action = "block"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "default_destination_action", "block"),
					resource.TestCheckResourceAttr(r, "local_mcp_approval_config",
						`{"channels":[{"type":"invocation_context"}],"step_up_mode":"approve","timeout_seconds":900}`),
				),
			},
			{
				Config: cfg(approval(600)),
				Check: resource.TestCheckResourceAttr(r, "local_mcp_approval_config",
					`{"channels":[{"type":"invocation_context"}],"step_up_mode":"approve","timeout_seconds":600}`),
			},
			{
				ResourceName:      r,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// checkGroupPostureOnPlatform reads a device group's posture row through the
// SDK, so a test can assert what the platform stored rather than what state
// says.
func checkGroupPostureOnPlatform(t *testing.T, groupID *string,
	check func(*adminapi.EndpointPolicySettings) error) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		row, err := testAccSDK(t).EndpointPolicies().GetGroupSettings(context.Background(),
			os.Getenv(envTenantID), *groupID)
		if err != nil {
			return fmt.Errorf("read group posture from the platform: %w", err)
		}
		return check(row)
	}
}
