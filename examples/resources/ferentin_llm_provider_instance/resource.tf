# Look up the Anthropic catalog entry by slug — gives a typo-safe handle
# we can reference downstream.
data "ferentin_llm_provider" "anthropic" {
  slug = "anthropic"
}

resource "ferentin_llm_provider_instance" "anthropic_prod_us" {
  provider_type = data.ferentin_llm_provider.anthropic.slug
  instance_name = "anthropic-prod-us"
  display_name  = "Anthropic Production (US)"
  description   = "Primary US-region Anthropic credentials"

  # Sensitive — never appears in plan/apply output. In production, source
  # from a vault: api_key = data.vault_kv_secret_v2.anthropic.data["api_key"]
  api_key   = var.anthropic_api_key
  auth_type = "api_key"

  enabled  = true
  priority = 100
}

variable "anthropic_api_key" {
  type      = string
  sensitive = true
}

output "anthropic_instance_id" {
  value = ferentin_llm_provider_instance.anthropic_prod_us.instance_id
}

# --- Pinning a single model via model_constraints -----------------------
# Common pattern in production: a BYOK key with scope limited to one model
# upstream, mirrored by an allowlist on the Ferentin side so runtime
# requests for any other model are denied before they leave the platform.

resource "ferentin_llm_provider_instance" "openai_gpt55_only" {
  provider_type = "openai"
  instance_name = "openai-gpt55-only"
  display_name  = "OpenAI (GPT-5.5 only)"

  api_key   = var.openai_api_key
  auth_type = "API_KEY"

  enabled  = true
  priority = 100

  model_constraints = {
    mode   = "allowlist"
    models = ["gpt-5.5"]
  }
}

variable "openai_api_key" {
  type      = string
  sensitive = true
}
