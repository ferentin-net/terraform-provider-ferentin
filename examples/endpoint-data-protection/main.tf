# PII and card data on managed endpoints: find SSNs, government IDs and
# payment card numbers in what coding agents are given, run and read on a
# developer's machine, log every match first, then block the ones that matter.
#
# One data protection policy, evaluated ON-DEVICE in three places:
#
#   prompts      what a developer submits to the agent, before the model sees it
#   tool calls   shell commands, fetched URLs and bodies, file writes, before they run
#   tool output  what a tool returns, before the model sees it (best effort)
#
# Detectors come from two catalog profiles, US_PII and FINANCIAL. The ones in
# `local.enforced_detectors` take `var.enforcement` (log, then block); every
# other match is logged.
#
# The gateway scopes are off: this policy covers the endpoint only.
#
# `terraform apply` STAGES the change. Devices pick it up only after someone
# activates the pending changes in the admin console.

terraform {
  required_version = ">= 1.11"

  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

provider "ferentin" {
  # endpoint + tenant_id auto-default. See the root README for auth options.
  # Needs `policy:rw` for the policy.
  profile = var.profile
}

locals {
  # Hard identifiers and cardholder data. Email, phone, date of birth, EIN and
  # SWIFT are in the same profiles and are everywhere in ordinary code, so they
  # stay on the default effect (log).
  #
  # US_ITIN is in US_PII but runs at the gateway only: its validator has no
  # check digit, so devices drop it rather than guess. Naming it here would
  # read as coverage the endpoint does not have.
  enforced_detectors = [
    "US_SSN",
    "US_PASSPORT",
    "US_DRIVERS_LICENSE",
    "CREDIT_CARD",
    "CVV",
    "US_BANK_ROUTING",
    "IBAN",
  ]

  profiles = ["US_PII", "FINANCIAL"]

  # What this policy enables: its profiles' detectors, minus the disabled ones.
  # An effect applies only to these.
  enabled_detectors = setsubtract(
    flatten([for p in data.ferentin_data_protection_profile.selected : p.detectors]),
    var.disabled_detectors,
  )
}

# Fails the plan if a detector ID is not in the catalog, instead of saving a
# policy that silently never matches it.
data "ferentin_data_protection_detector" "enforced" {
  for_each = toset(local.enforced_detectors)
  id       = each.key
}

data "ferentin_data_protection_profile" "selected" {
  for_each = toset(local.profiles)
  name     = each.key
}

resource "ferentin_data_protection_policy" "endpoint_pii_pci" {
  name        = var.policy_name
  description = "SSNs, government IDs and card data in coding agents' prompts, tool calls and tool output"
  priority    = var.priority

  enabled_profiles = local.profiles

  # FINANCIAL detectors that rarely matter to a US PII/PCI policy and match
  # often in code: Indian bank codes and crypto wallet addresses.
  disabled_detectors = { for d in var.disabled_detectors : d => true }

  effects        = { for id, d in data.ferentin_data_protection_detector.enforced : id => var.enforcement }
  default_effect = "log"

  blocked_message = var.blocked_message

  # Endpoint only. Both gateway scopes default to true, so turn them off
  # explicitly.
  apply_to_llm_input  = false
  apply_to_llm_output = false

  apply_to_prompt_input = true
  apply_to_tool_input   = true
  apply_to_tool_output  = var.scan_tool_output

  # Empty = every enrolled device. Narrow it to a pilot group first.
  device_group_ids = var.device_group_ids

  # No `criteria`: a policy with criteria is withheld from devices, and
  # enforces nothing there.

  lifecycle {
    # The platform refuses an effect on a detector the policy does not enable.
    # Fail the plan instead, naming it.
    precondition {
      condition     = length(setsubtract(local.enforced_detectors, local.enabled_detectors)) == 0
      error_message = "These enforced detectors are not enabled by this policy: ${join(", ", setsubtract(local.enforced_detectors, local.enabled_detectors))}. Add the profile that has them to local.profiles, take them out of disabled_detectors, or take them out of local.enforced_detectors."
    }
  }
}

output "policy_id" {
  value = ferentin_data_protection_policy.endpoint_pii_pci.policy_id
}

output "enforced_detectors" {
  description = "What each enforced detector does, as applied (devices follow after activation)."
  value       = ferentin_data_protection_policy.endpoint_pii_pci.effects
}

output "endpoint_effect_mapping" {
  description = "Per device scope, the authored effects a device applies differently (authored → applied)."
  value       = ferentin_data_protection_policy.endpoint_pii_pci.endpoint_effect_mapping
}
