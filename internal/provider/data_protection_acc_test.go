package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acceptance tests for the data-protection entities. Gated by TF_ACC=1;
// require a live platform per the env contract in provider_acc_test.go.

// TestAccDataProtectionPolicy_basic exercises the resource end-to-end:
// Create → Read → Update → Import → Destroy. Covers the map attributes
// (effects / detector_thresholds / disabled_detectors / detector_configs),
// the `log` effect, the scope flags, and a nested ABAC criteria block.
func TestAccDataProtectionPolicy_basic(t *testing.T) {
	name := "tf-acc-dpp-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configDataProtectionPolicy(name, "log", "blocked by acc test"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_data_protection_policy.test", "name", name),
					resource.TestCheckResourceAttr("ferentin_data_protection_policy.test", "default_effect", "log"),
					resource.TestCheckResourceAttr("ferentin_data_protection_policy.test", "enabled", "true"),
					resource.TestCheckResourceAttr("ferentin_data_protection_policy.test", "effects.EXFILTRATION_URL", "log"),
					resource.TestCheckResourceAttr("ferentin_data_protection_policy.test", "effects.US_SSN", "tokenize"),
					resource.TestCheckResourceAttr("ferentin_data_protection_policy.test", "detector_thresholds.US_SSN", "0.95"),
					resource.TestCheckResourceAttr("ferentin_data_protection_policy.test", "apply_to_mcp_output", "true"),
					resource.TestCheckResourceAttr("ferentin_data_protection_policy.test", "criteria.0.operator", "AND"),
					resource.TestCheckResourceAttr("ferentin_data_protection_policy.test", "criteria.0.conditions.0.field", "department"),
					resource.TestCheckResourceAttrSet("ferentin_data_protection_policy.test", "policy_id"),
					resource.TestCheckResourceAttrSet("ferentin_data_protection_policy.test", "detector_configs.EXFILTRATION_URL"),
				),
			},
			{
				// Flip the default effect and the blocked message — exercises
				// Update + the OneOf validator.
				Config: configDataProtectionPolicy(name, "redact", "updated message"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_data_protection_policy.test", "default_effect", "redact"),
					resource.TestCheckResourceAttr("ferentin_data_protection_policy.test", "blocked_message", "updated message"),
				),
			},
			{
				ResourceName:      "ferentin_data_protection_policy.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDataProtectionPolicy_a2aAndToolInput covers the A2A scopes and
// tool_input_unscanned_action, which keep the stored value when omitted: set on
// create, kept when removed from config (the provider does not send them and the
// platform treats an omitted field as unchanged), then turned off explicitly.
func TestAccDataProtectionPolicy_a2aAndToolInput(t *testing.T) {
	name := "tf-acc-dp-a2a-" + randomSuffix(t)
	cfg := func(fields string) string {
		return providerBlock() + fmt.Sprintf(`
resource "ferentin_data_protection_policy" "a2a" {
  name             = %q
  enabled_profiles = ["SECRETS"]
  default_effect   = "redact"
%s
}
`, name, fields)
	}
	check := func(input, output, fileAction, toolInput string) resource.TestCheckFunc {
		const r = "ferentin_data_protection_policy.a2a"
		return resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(r, "apply_to_a2a_input", input),
			resource.TestCheckResourceAttr(r, "apply_to_a2a_output", output),
			resource.TestCheckResourceAttr(r, "a2a_unscannable_file_action", fileAction),
			resource.TestCheckResourceAttr(r, "tool_input_unscanned_action", toolInput),
		)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(`
  apply_to_a2a_input          = true
  apply_to_a2a_output         = true
  a2a_unscannable_file_action = "pass_and_count"
  apply_to_tool_input         = true
  tool_input_unscanned_action = "block"
`),
				Check: check("true", "true", "pass_and_count", "block"),
			},
			{
				// Removed from config, with another field changed so the apply really writes.
				Config: cfg(`  description = "a2a fields omitted"`),
				Check:  check("true", "true", "pass_and_count", "block"),
			},
			{
				Config: cfg(`
  apply_to_a2a_input          = false
  apply_to_a2a_output         = false
  a2a_unscannable_file_action = "block"
  tool_input_unscanned_action = "allow"
`),
				Check: check("false", "false", "block", "allow"),
			},
			{
				ResourceName:      "ferentin_data_protection_policy.a2a",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDataProtectionPolicy_effectOnDetectorNotEnabled checks the platform
// refuses an effect on a detector the policy does not enable
// (ferentin-platform#3197): on create, and on an update that drops the profile
// enabling a detector the effects still name.
func TestAccDataProtectionPolicy_effectOnDetectorNotEnabled(t *testing.T) {
	name := "tf-acc-dp-inert-" + randomSuffix(t)
	cfg := func(profiles, effects string) string {
		return providerBlock() + fmt.Sprintf(`
resource "ferentin_data_protection_policy" "inert" {
  name             = %q
  enabled_profiles = %s
  default_effect   = "log"
  effects          = %s
}
`, name, profiles, effects)
	}
	refused := regexp.MustCompile(`(?s)US_SSN.*does\s+not\s+enable`)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// US_SSN is in US_PII, not SECRETS.
				Config:      cfg(`["SECRETS"]`, `{ "US_SSN" = "block" }`),
				ExpectError: refused,
			},
			{
				Config: cfg(`["SECRETS", "US_PII"]`, `{ "US_SSN" = "block" }`),
				Check: resource.TestCheckResourceAttr(
					"ferentin_data_protection_policy.inert", "effects.US_SSN", "block"),
			},
			{
				Config:      cfg(`["SECRETS"]`, `{ "US_SSN" = "block" }`),
				ExpectError: refused,
			},
		},
	})
}

// TestAccDataProtectionCatalog_basic reads the built-in profile/detector
// catalog data sources and asserts a few stable facts.
func TestAccDataProtectionCatalog_basic(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configDataProtectionCatalog(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.ferentin_data_protection_profiles.all", "profiles.#"),
					resource.TestCheckResourceAttr("data.ferentin_data_protection_profile.us_pii", "id", "US_PII"),
					resource.TestCheckResourceAttr("data.ferentin_data_protection_profile.us_pii", "category", "PII"),
					resource.TestCheckResourceAttr("data.ferentin_data_protection_profile.us_pii", "region", "US"),
					resource.TestCheckResourceAttrSet("data.ferentin_data_protection_detectors.all", "detectors.#"),
					resource.TestCheckResourceAttr("data.ferentin_data_protection_detector.ssn", "category", "PII"),
					resource.TestCheckResourceAttr("data.ferentin_data_protection_detector.ssn", "fpe_safe", "true"),
				),
			},
		},
	})
}

// --- HCL fixtures ---------------------------------------------------------

func configDataProtectionPolicy(name, defaultEffect, blockedMessage string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_data_protection_policy" "test" {
  name            = %q
  priority        = 100
  default_effect  = %q
  blocked_message = %q

  enabled_profiles = ["US_PII", "EXFILTRATION_DEFENSE"]

  effects = {
    "US_SSN"           = "tokenize"
    "EXFILTRATION_URL" = "log"
  }
  detector_thresholds = { "US_SSN" = 0.95 }
  disabled_detectors  = { "EU_VAT" = true }
  detector_configs = {
    "EXFILTRATION_URL" = jsonencode({ minConfidenceScore = 0.5 })
  }

  fpe_key_id  = "tf-acc-fpe"
  tweak_scope = "conversation"

  apply_to_llm_input  = true
  apply_to_llm_output = true
  apply_to_mcp_output = true

  criteria = [
    {
      operator = "AND"
      type     = "claims"
      conditions = [
        {
          field    = "department"
          operator = "equals"
          value    = jsonencode("legal")
        }
      ]
    }
  ]
}
`, name, defaultEffect, blockedMessage)
}

func configDataProtectionCatalog() string {
	return providerBlock() + `
data "ferentin_data_protection_profiles" "all" {}

data "ferentin_data_protection_profile" "us_pii" {
  name = "US_PII"
}

data "ferentin_data_protection_detectors" "all" {}

data "ferentin_data_protection_detector" "ssn" {
  id = "US_SSN"
}
`
}
