package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// TestAccEndpointMCPRule_basic covers create, update and import of a block
// rule, one removal per step so no removal hides behind another change:
//
//   - step 2 removes ONLY device_group_ids. description/app_bundle_ids/
//     device_group_ids are Optional, not Computed, so that alone must plan a
//     change; the check fails if the rule stayed narrowed.
//   - step 3 removes description and criteria_combinator = "OR". The platform
//     keeps criteria_combinator when a PUT omits it, so this converges (the
//     framework's post-apply empty-plan check) only because its schema Default
//     always sends it.
func TestAccEndpointMCPRule_basic(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_endpoint_mcp_rule.test"
	groups := "  device_group_ids = [ferentin_device_group.test.group_id]\n"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configEndpointMCPRule(suffix, 10, groups+`
  description         = "initial"
  criteria_combinator = "OR"
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "match_kind", "host"),
					resource.TestCheckResourceAttr(r, "action", "block"),
					resource.TestCheckResourceAttr(r, "priority", "10"),
					resource.TestCheckResourceAttr(r, "description", "initial"),
					resource.TestCheckResourceAttr(r, "criteria_combinator", "OR"),
					resource.TestCheckResourceAttr(r, "device_group_ids.#", "1"),
					resource.TestCheckResourceAttr(r, "managed_by", "iac"),
					resource.TestCheckResourceAttrSet(r, "version"),
					resource.TestCheckResourceAttrSet(r, "enforcement"),
				),
			},
			{
				Config: configEndpointMCPRule(suffix, 10, `
  description         = "initial"
  criteria_combinator = "OR"
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(r, "device_group_ids.#"),
					resource.TestCheckResourceAttr(r, "description", "initial"),
					resource.TestCheckResourceAttr(r, "criteria_combinator", "OR"),
				),
			},
			{
				Config: configEndpointMCPRule(suffix, 10, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(r, "description"),
					resource.TestCheckResourceAttr(r, "criteria_combinator", "AND"),
				),
			},
			{
				Config: configEndpointMCPRule(suffix, 20, ""),
				Check:  resource.TestCheckResourceAttr(r, "priority", "20"),
			},
			{
				ResourceName:      r,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccEndpointMCPRule_outOfBandEditShowsAsDrift is the property
// reconcileMatchValues exists for: match_values keeps the configured spelling
// while the platform's list is unchanged, but an edit made outside Terraform
// must still produce a plan.
func TestAccEndpointMCPRule_outOfBandEditShowsAsDrift(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_endpoint_mcp_rule.test"
	var ruleID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configEndpointMCPRule(suffix, 10, ""),
				Check: resource.TestCheckResourceAttrWith(r, "rule_id", func(v string) error {
					ruleID = v
					return nil
				}),
			},
			{
				PreConfig: func() {
					ctx := context.Background()
					sdk := testAccSDK(t)
					tenantID := os.Getenv(envTenantID)
					rule, err := sdk.EndpointMCPRules().Get(ctx, tenantID, ruleID)
					if err != nil {
						t.Fatalf("get rule %s: %v", ruleID, err)
					}
					_, err = sdk.EndpointMCPRules().Update(ctx, tenantID, ruleID,
						strconv.FormatInt(*rule.Version, 10), adminapi.EndpointMCPRuleWrite{
							Name:        *rule.Name,
							Action:      *rule.Action,
							MatchKind:   gen.EndpointMcpRuleRequestMatchKind(*rule.MatchKind),
							MatchValues: []string{"mcp.out-of-band-" + suffix + ".example"},
							Priority:    rule.Priority,
						})
					if err != nil {
						t.Fatalf("out-of-band update: %v", err)
					}
				},
				Config:             configEndpointMCPRule(suffix, 10, ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// The plan-only step does not save its refresh, and the post-test
				// destroy does not refresh either, so without this the destroy
				// sends the pre-edit version as If-Match and gets 412, leaving the
				// rule behind.
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
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

// configEndpointMCPRule renders a block rule plus a device group it may
// target; extra carries the optional attributes a step sets.
func configEndpointMCPRule(suffix string, priority int, extra string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name   = "tf-acc-dg-mcp-%[1]s"
  source = "manual"
}

resource "ferentin_endpoint_mcp_rule" "test" {
  name         = "tf-acc-mcp-rule-%[1]s"
  priority     = %[2]d
  match_kind   = "host"
  match_values = ["mcp.tf-acc-%[1]s.example"]
  action       = "block"
%[3]s
}
`, suffix, priority, extra)
}
