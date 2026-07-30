variable "anthropic_instance_id" {
  type        = string
  description = "UUID of the ferentin_llm_provider instance this policy routes to."
}

# LLM governance policy with ABAC criteria + per-request limits.
# See §6.4 of the design doc for the policy model.

resource "ferentin_llm_policy" "engineering_default" {
  name        = "engineering-default"
  description = "Default LLM policy for engineering users"
  priority    = 100
  enabled     = true

  # Routing. This takes instance UUIDs, not names: the platform accepts a name
  # for backward compatibility but stores and returns the resolved UUID, so a
  # name here fails the apply with "Provider produced inconsistent result after
  # apply" — after the policy has already been written. The provider rejects a
  # non-UUID at plan time to keep that from reaching the API. In a real config
  # this is `ferentin_llm_provider.<name>.instance_id`.
  provider_instances = [var.anthropic_instance_id]

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
