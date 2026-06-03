package provider

import (
	"fmt"
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
