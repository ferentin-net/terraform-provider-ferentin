# LLM governance policy with ABAC criteria + per-request limits.
# See §6.4 of the design doc for the policy model.

resource "ferentin_llm_policy" "engineering_default" {
  name        = "engineering-default"
  description = "Default LLM policy for engineering users"
  priority    = 100
  enabled     = true

  # Routing — references an existing ferentin_llm_provider.
  provider_instances = ["anthropic-prod-us"]

  # Prompt injection.
  system_prompt = file("${path.module}/prompts/system.md")

  # Per-request limits.
  limits = {
    max_tokens             = 100000
    max_request_kb         = 1024
    request_timeout_ms     = 60000
    stream_timeout_ms      = 120000
    enforce_model_limits   = true
    max_images_per_request = 4
  }

  # ABAC criteria — multiple entries are AND-ed; conditions within one
  # entry combine under the parent `operator`. The `value` field is a
  # JSON-encoded string, so `jsonencode("engineering")` is a single string,
  # `jsonencode(["a","b"])` is a JSON array, etc.
  criteria = [
    {
      operator = "AND"
      conditions = [
        {
          field    = "user.department"
          operator = "in"
          value    = jsonencode(["engineering", "data"])
        },
        {
          field    = "request.token_count"
          operator = "lt"
          value    = jsonencode(100000)
        },
      ]
    },
  ]
}
