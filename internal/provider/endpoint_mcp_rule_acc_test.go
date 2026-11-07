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
//   - step 2 removes description and criteria_combinator = "OR". The platform
//     keeps criteria_combinator when a PUT omits it, so this converges (the
//     framework's post-apply empty-plan check) only because its schema Default
//     always sends it.
//
// device_group_ids is not exercised here: the platform refuses a call-time
// rule whose device groups hold no active device ("nothing could carry it"),
// and a group a test creates is empty. TestEndpointMCPRuleModifyPlan_* and
// TestEndpointMCPRule_EmptyFormsReadAsNullUnlessConfigured cover its removal.
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
					resource.TestCheckResourceAttr(r, "match_kind", "tool_name"),
					resource.TestCheckResourceAttr(r, "priority", "10"),
					resource.TestCheckResourceAttr(r, "description", "initial"),
					resource.TestCheckResourceAttr(r, "criteria_combinator", "OR"),
					resource.TestCheckResourceAttr(r, "managed_by", "iac"),
					resource.TestCheckResourceAttrSet(r, "version"),
					resource.TestCheckResourceAttrSet(r, "enforcement"),
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
							Action:      "block",
							MatchKind:   gen.EndpointMcpRuleRequestMatchKind(*rule.MatchKind),
							MatchValues: []string{"tf_acc_out_of_band_" + suffix},
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

// TestAccEndpointMCPRule_canonicalizedBehaviour: the platform trims and
// lowercases a tool_behavior rule's values. Writing " Shell " must not leave a
// perpetual diff: match_values keeps the configured spelling,
// stored_match_values the platform's. Import has no configured spelling, so it
// is ignored there.
func TestAccEndpointMCPRule_canonicalizedBehaviour(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_endpoint_mcp_rule.behaviour"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerBlock() + fmt.Sprintf(`
resource "ferentin_endpoint_mcp_rule" "behaviour" {
  name         = "tf-acc-mcp-behaviour-%s"
  match_kind   = "tool_behavior"
  match_values = [" Shell "]
}
`, suffix),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "match_values.0", " Shell "),
					resource.TestCheckResourceAttr(r, "stored_match_values.0", "shell"),
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

// configEndpointMCPRule renders a tenant-wide block rule; extra carries the
// optional attributes a step sets.
func configEndpointMCPRule(suffix string, priority int, extra string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_endpoint_mcp_rule" "test" {
  name         = "tf-acc-mcp-rule-%[1]s"
  priority     = %[2]d
  match_kind   = "tool_name"
  match_values = ["tf_acc_tool_%[1]s"]
%[3]s
}
`, suffix, priority, extra)
}
