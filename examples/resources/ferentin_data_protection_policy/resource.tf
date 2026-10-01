resource "ferentin_data_protection_policy" "prod" {
  name        = "prod-pii-and-exfil"
  description = "Tokenize US PII; log exfiltration URLs on responses"
  priority    = 100

  # Three-layer detector selection.
  enabled_profiles   = ["US_PII", "EXFILTRATION_DEFENSE"]
  enabled_detectors  = { "DATABASE_URL" = true }
  disabled_detectors = { "EU_VAT" = true }

  # Per-detector effect overrides + the default for everything else.
  effects = {
    "US_SSN"           = "tokenize"
    "EXFILTRATION_URL" = "log"
  }
  default_effect = "redact"

  # Per-detector tuning.
  detector_thresholds = { "US_SSN" = 0.95 }
  detector_configs = {
    "EXFILTRATION_URL" = jsonencode({
      minConfidenceScore = 0.5
      hostSuffixDenylist = ["webhook.site"]
    })
  }

  # Format-preserving encryption (required when any effect is "tokenize").
  fpe_key_id  = "dlp-fpe-2026"
  tweak_scope = "conversation"

  # Where the policy runs.
  apply_to_llm_input  = true
  apply_to_llm_output = true
  apply_to_mcp_output = true

  # Optional ABAC: apply only to the legal department.
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

# Endpoint data protection: evaluated on enrolled devices against AI coding
# agents' tool calls, prompts and tool output. Separate from the cloud scopes
# above — a policy can carry both, but opting in is explicit per scope.
resource "ferentin_data_protection_policy" "endpoint_secrets" {
  name             = "endpoint-secrets"
  enabled_profiles = ["SECRETS"]
  default_effect   = "block"
  blocked_message  = "Blocked: this looks like a credential. Ask #security if it is not."

  apply_to_llm_input  = false
  apply_to_llm_output = false

  apply_to_tool_input   = true # shell commands, fetched URLs and bodies, file writes
  apply_to_prompt_input = true # what a developer submits to the agent
  apply_to_tool_output  = true # what a tool returns, before the model sees it

  # Withhold tool output the device could not scan (best effort).
  tool_output_unscanned_action = "withhold"

  # Empty or unset = every enrolled device. Reference
  # ferentin_device_group.<name>.group_id to narrow it.
  device_group_ids = []
}
