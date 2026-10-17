package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// TestAccEndpointDestinationRule_posturePredicate covers #20: an apply that
// touches the rule must not erase its posture condition.
//
// Step 2 is the regression guard: an unrelated change (priority) keeps the
// condition. Step 4 removes it from config and checks the platform cleared it.
// Step 5 sets it outside Terraform, as the console does, and the plan must show
// it. Before #20 that plan was empty and the next apply erased the condition.
//
// The rule is disabled throughout. A posture condition moves the devices that
// receive the rule to bundle section 1.4.0, which older agents in the test
// tenant would refuse.
func TestAccEndpointDestinationRule_posturePredicate(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_endpoint_destination_rule.test"
	var ruleID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configEndpointRulePosture(suffix, 50, "  posture_predicate = { require_compliant = true }\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "posture_predicate.require_compliant", "true"),
					resource.TestCheckResourceAttrWith(r, "rule_id", func(v string) error {
						ruleID = v
						return nil
					}),
				),
			},
			{
				Config: configEndpointRulePosture(suffix, 60, "  posture_predicate = { require_compliant = true }\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "priority", "60"),
					resource.TestCheckResourceAttr(r, "posture_predicate.require_compliant", "true"),
					testAccCheckRulePosturePredicate(t, &ruleID, true),
				),
			},
			{
				ResourceName:      r,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: configEndpointRulePosture(suffix, 60, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(r, "posture_predicate.require_compliant"),
					testAccCheckRulePosturePredicate(t, &ruleID, false),
				),
			},
			{
				PreConfig: func() {
					ctx := context.Background()
					sdk := testAccSDK(t)
					tenantID := os.Getenv(envTenantID)
					rule, err := sdk.EndpointPolicies().GetRule(ctx, tenantID, ruleID)
					if err != nil {
						t.Fatalf("get rule %s: %v", ruleID, err)
					}
					// Full replace: carry every field the config sets, or this edit
					// changes more than the predicate.
					_, err = sdk.EndpointPolicies().UpdateRule(ctx, tenantID, ruleID,
						strconv.FormatInt(*rule.Version, 10), adminapi.EndpointDestinationRuleWrite{
							Name:               *rule.Name,
							Action:             *rule.Action,
							Priority:           rule.Priority,
							Enabled:            rule.Enabled,
							DestinationKind:    rule.DestinationKind,
							CatalogSlug:        rule.CatalogSlug,
							DeviceGroupIds:     rule.DeviceGroupIds,
							CriteriaCombinator: rule.CriteriaCombinator,
							PosturePredicate:   &gen.EndpointPosturePredicateDto{RequireCompliant: boolPtr(true)},
						})
					if err != nil {
						t.Fatalf("out-of-band update: %v", err)
					}
				},
				Config:             configEndpointRulePosture(suffix, 60, ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				// Any diff would satisfy ExpectNonEmptyPlan; pin it to the
				// predicate's removal. PreApply checks do not run on a PlanOnly
				// step, PostApplyPostRefresh does.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(r, plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue(r, tfjsonpath.New("posture_predicate"), knownvalue.Null()),
					},
				},
			},
			{
				// The plan-only step does not save its refresh; without this the
				// destroy sends the pre-edit version as If-Match and gets 412.
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// testAccCheckRulePosturePredicate asserts what the platform stored, not what
// state says: the bug in #20 was state and platform disagreeing.
func testAccCheckRulePosturePredicate(t *testing.T, ruleID *string, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		rule, err := testAccSDK(t).EndpointPolicies().GetRule(context.Background(),
			os.Getenv(envTenantID), *ruleID)
		if err != nil {
			return fmt.Errorf("get rule %s: %w", *ruleID, err)
		}
		got := rule.PosturePredicate != nil && rule.PosturePredicate.RequireCompliant != nil &&
			*rule.PosturePredicate.RequireCompliant
		if got != want {
			return fmt.Errorf("platform requireCompliant = %v, want %v", got, want)
		}
		return nil
	}
}

func configEndpointRulePosture(suffix string, priority int, predicate string) string {
	return fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name   = "tf-acc-dg-%[1]s"
  source = "manual"
}

resource "ferentin_endpoint_destination_rule" "test" {
  name             = "tf-acc-rule-posture-%[1]s"
  priority         = %[2]d
  enabled          = false
  destination_kind = "ai_provider"
  catalog_slug     = "anthropic"
  action           = "allow"

  device_group_ids = [ferentin_device_group.test.group_id]
%[3]s}
`, suffix, priority, predicate)
}
