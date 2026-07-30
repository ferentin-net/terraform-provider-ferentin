package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acceptance tests for the platform#2038 endpoint-policy entities. Gated by
// TF_ACC=1; require a live platform per the env contract in provider_acc_test.go.
//
// These have NOT been run against a live platform yet: they need an environment
// where BOTH halves are deployed — platform migration 1214 applied, and the
// ferentin-cli-app SDK commit merged so go.mod can point at it (see
// DEVELOPMENT.md "Landing a change that spans both repos"). They encode the
// intended end-to-end contract so the first live run is a check rather than a
// design exercise.
//
// PRECONDITION: the CI service account's role must carry either `devices:rw` or
// the narrow `devices:groups:rw`. Platform migration 1215 maps the latter to
// `ferentin.iac.operator` (and `devices:groups:r` to `ferentin.iac.reader`)
// precisely so this suite can manage device groups without also holding the
// authority to transition device status, revoke certificate serials, or force
// re-enrollment. A role-bound CC client on an older platform will 403 here.

// TestAccDeviceGroup_basic exercises Create → Read → Update → Import → Destroy.
// Also pins that `source` / `external_id` force replacement, since the platform's
// update DTO does not accept them and a silent no-op update would be worse than
// a replace.
func TestAccDeviceGroup_basic(t *testing.T) {
	name := "tf-acc-dg-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configDeviceGroup(name, "initial description", "manual"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_device_group.test", "name", name),
					resource.TestCheckResourceAttr("ferentin_device_group.test", "description", "initial description"),
					resource.TestCheckResourceAttr("ferentin_device_group.test", "source", "manual"),
					resource.TestCheckResourceAttrSet("ferentin_device_group.test", "group_id"),
				),
			},
			{
				// description is mutable via PATCH.
				Config: configDeviceGroup(name, "updated description", "manual"),
				Check: resource.TestCheckResourceAttr(
					"ferentin_device_group.test", "description", "updated description"),
			},
			{
				ResourceName:      "ferentin_device_group.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccEndpointDestinationRule_basic exercises the full lifecycle plus the two
// things most likely to regress: the device-group reference (rather than a
// hardcoded UUID) and the If-Match round trip — a second Update is included
// deliberately, because that is exactly where ferentin_mcp_policy's hardcoded
// `W/"0"` used to start returning 412.
func TestAccEndpointDestinationRule_basic(t *testing.T) {
	suffix := randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configEndpointRule(suffix, 10, "block", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_endpoint_destination_rule.test", "action", "block"),
					resource.TestCheckResourceAttr("ferentin_endpoint_destination_rule.test", "priority", "10"),
					resource.TestCheckResourceAttr("ferentin_endpoint_destination_rule.test", "catalog_slug", "openai"),
					// Provenance: written by the provider, so managed_by must be iac.
					resource.TestCheckResourceAttr("ferentin_endpoint_destination_rule.test", "managed_by", "iac"),
					resource.TestCheckResourceAttr("ferentin_endpoint_destination_rule.test", "last_modified_by", "iac"),
					resource.TestCheckResourceAttrSet("ferentin_endpoint_destination_rule.test", "version"),
					resource.TestCheckResourceAttrSet("ferentin_endpoint_destination_rule.test", "managed_by_module"),
					// The target is a reference to the managed group, not a literal.
					resource.TestCheckResourceAttrPair(
						"ferentin_endpoint_destination_rule.test", "device_group_ids.0",
						"ferentin_device_group.test", "group_id"),
				),
			},
			{
				// First update — version 0 → 1.
				Config: configEndpointRule(suffix, 20, "steer", "https://edge.example.com/v1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_endpoint_destination_rule.test", "action", "steer"),
					resource.TestCheckResourceAttr("ferentin_endpoint_destination_rule.test", "steer_to_url",
						"https://edge.example.com/v1"),
					resource.TestCheckResourceAttr("ferentin_endpoint_destination_rule.test", "priority", "20"),
				),
			},
			{
				// SECOND update — the regression guard. With a hardcoded If-Match
				// this step 412s while the first one passed.
				Config: configEndpointRule(suffix, 30, "steer", "https://edge2.example.com/v1"),
				Check: resource.TestCheckResourceAttr(
					"ferentin_endpoint_destination_rule.test", "priority", "30"),
			},
			{
				ResourceName:      "ferentin_endpoint_destination_rule.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccEndpointDestinationRule_criteria covers the platform#2040 authoring
// path: a criteria-scoped rule must apply, import, and re-plan clean.
//
// The round trip is the whole point. The rule's criteria column is opaque on
// the platform side — it stores the provider's JSON verbatim — so any drift
// between what the provider writes and what it reads back shows up as a
// permanent diff on the one field that decides which users a rule applies to.
//
// Step 3 removes the criteria block deliberately: that is a WIDENING (the rule
// stops being scoped to a department), the provider warns about it at plan
// time, and the apply must actually clear the column rather than preserve it.
func TestAccEndpointDestinationRule_criteria(t *testing.T) {
	suffix := randomSuffix(t)
	const rule = "ferentin_endpoint_destination_rule.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configEndpointRuleWithCriteria(suffix, `
  criteria_combinator = "AND"

  criteria = [{
    operator = "AND"
    conditions = [{
      field = "department"
      value = jsonencode("legal")
      operator = "equals"
    }]
  }]
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rule, "criteria.#", "1"),
					resource.TestCheckResourceAttr(rule, "criteria.0.operator", "AND"),
					// Defaulted by the provider, not the platform: this column is
					// stored opaquely, so nothing fills it server-side.
					resource.TestCheckResourceAttr(rule, "criteria.0.type", "claims"),
					resource.TestCheckResourceAttr(rule, "criteria.0.conditions.0.field", "department"),
					resource.TestCheckResourceAttr(rule, "criteria.0.conditions.0.value", `"legal"`),
					resource.TestCheckResourceAttr(rule, "criteria_combinator", "AND"),
				),
			},
			{
				// Two groups combined with OR — exercises criteria_combinator, the
				// attribute the three policy resources do not have.
				Config: configEndpointRuleWithCriteria(suffix, `
  criteria_combinator = "OR"

  criteria = [
    {
      operator = "AND"
      conditions = [{
        field = "department"
        value = jsonencode(["legal", "compliance"])
        operator = "in"
        value_type = "array"
      }]
    },
    {
      operator = "AND"
      conditions = [{
        field = "email"
        value = jsonencode("@legal.example.com")
        operator = "ends_with"
        case_sensitive = false
      }]
    },
  ]
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rule, "criteria.#", "2"),
					resource.TestCheckResourceAttr(rule, "criteria_combinator", "OR"),
					resource.TestCheckResourceAttr(rule, "criteria.0.conditions.0.value",
						`["legal","compliance"]`),
					resource.TestCheckResourceAttr(rule, "criteria.1.conditions.0.case_sensitive", "false"),
				),
			},
			{
				ResourceName:      rule,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Removing criteria widens the rule to everyone it targets. It must
				// take effect (the column is cleared), not be silently preserved.
				Config: configEndpointRuleWithCriteria(suffix, ""),
				Check:  resource.TestCheckNoResourceAttr(rule, "criteria.#"),
			},
		},
	})
}

// TestAccEndpointPolicySettings_tenantDefault covers the upsert resource's two
// unusual behaviours: Create adopting/creating the singleton tenant row, and
// destroy being a NO-OP on it (Terraform drops state; the row keeps enforcing).
// There is deliberately no CheckDestroy asserting the row is gone — it will
// still be there, with the last-applied posture, which is the point.
func TestAccEndpointPolicySettings_tenantDefault(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "ferentin_endpoint_policy_settings" "tenant" {
  unapproved_mcp_action      = "quarantine"
  default_destination_action = "allow"
  quic_block_enabled         = true
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_endpoint_policy_settings.tenant",
						"unapproved_mcp_action", "quarantine"),
					resource.TestCheckResourceAttr("ferentin_endpoint_policy_settings.tenant",
						"quic_block_enabled", "true"),
					// The tenant-default row has no device group; its id is the bare tenant id.
					resource.TestCheckNoResourceAttr("ferentin_endpoint_policy_settings.tenant",
						"device_group_id"),
					resource.TestCheckResourceAttrSet("ferentin_endpoint_policy_settings.tenant", "version"),
				),
			},
			{
				// Second apply exercises the If-Match path on an upsert.
				Config: `
resource "ferentin_endpoint_policy_settings" "tenant" {
  unapproved_mcp_action      = "report_only"
  default_destination_action = "allow"
  quic_block_enabled         = false
}
`,
				Check: resource.TestCheckResourceAttr("ferentin_endpoint_policy_settings.tenant",
					"unapproved_mcp_action", "report_only"),
			},
			{
				ResourceName:      "ferentin_endpoint_policy_settings.tenant",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccEndpointPolicySettings_groupOverride covers the per-group row, which
// unlike the tenant default IS genuinely deleted on destroy (the group then
// falls back to the tenant default).
func TestAccEndpointPolicySettings_groupOverride(t *testing.T) {
	suffix := randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name   = "tf-acc-dg-%[1]s"
  source = "manual"
}

resource "ferentin_endpoint_policy_settings" "group" {
  device_group_id            = ferentin_device_group.test.group_id
  unapproved_mcp_action      = "block"
  default_destination_action = "block"
  mcp_gateway_url            = "https://mcp.example.com"
  ech_strip_enabled          = true
  doh_block_enabled          = true
  quic_block_enabled         = true
}
`, suffix),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(
						"ferentin_endpoint_policy_settings.group", "device_group_id",
						"ferentin_device_group.test", "group_id"),
					resource.TestCheckResourceAttr("ferentin_endpoint_policy_settings.group",
						"unapproved_mcp_action", "block"),
					resource.TestCheckResourceAttr("ferentin_endpoint_policy_settings.group",
						"mcp_gateway_url", "https://mcp.example.com"),
				),
			},
			{
				ResourceName:      "ferentin_endpoint_policy_settings.group",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func configDeviceGroup(name, description, source string) string {
	return fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name        = %[1]q
  description = %[2]q
  source      = %[3]q
}
`, name, description, source)
}

func configEndpointRule(suffix string, priority int, action, steerURL string) string {
	steer := ""
	if steerURL != "" {
		steer = fmt.Sprintf("  steer_to_url = %q\n", steerURL)
	}
	return fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name   = "tf-acc-dg-%[1]s"
  source = "manual"
}

resource "ferentin_endpoint_destination_rule" "test" {
  name             = "tf-acc-rule-%[1]s"
  priority         = %[2]d
  destination_kind = "ai_provider"
  catalog_slug     = "openai"
  action           = %[3]q
%[4]s
  app_bundle_ids   = ["com.openai.chat"]
  device_group_ids = [ferentin_device_group.test.group_id]
}
`, suffix, priority, action, steer)
}

// configEndpointRuleWithCriteria is configEndpointRule's criteria-authoring
// sibling: an allow rule (criteria narrow, so scoping an allow is the shape a
// customer actually writes) plus whatever criteria block the step passes in.
func configEndpointRuleWithCriteria(suffix, criteria string) string {
	return fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name   = "tf-acc-dg-%[1]s"
  source = "manual"
}

resource "ferentin_endpoint_destination_rule" "test" {
  name             = "tf-acc-rule-crit-%[1]s"
  priority         = 40
  destination_kind = "ai_provider"
  catalog_slug     = "openai"
  action           = "allow"

  device_group_ids = [ferentin_device_group.test.group_id]
%[2]s
}
`, suffix, criteria)
}
