# End-to-end example: a single tenant configured with one edge site, one
# Anthropic LLM provider instance, one MCP server federating an OAuth-backed
# upstream, matching governance policies, and the endpoint surface (device
# groups, on-device destination rules, posture) that governs AI traffic on
# managed laptops. Apply order is implicit from attribute references — no
# `depends_on` needed.

terraform {
  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

provider "ferentin" {
  # endpoint defaults to https://api.ferentin.net; override only for non-prod.
  # tenant_id auto-resolves from the JWT's tid claim — set only to override.
  endpoint      = var.endpoint
  client_id     = var.client_id
  client_secret = var.client_secret
}

# --- Edge site -----------------------------------------------------------
resource "ferentin_edge_site" "primary" {
  site_id     = "prod-us-east-1a"
  site_name   = "US East 1A"
  description = "Primary US-East edge for LLM and MCP traffic"
  time_zone   = "America/New_York"

  # Strict defaults are already the schema defaults; surfaced here for
  # clarity. Override `verify_upstream_tls = false` only for local-dev.
  allow_http_upstream = false
  verify_upstream_tls = true
  bundle_cloud_mcp    = true
}

# --- LLM provider (tenant-scoped binding) -------------------------------
# Note: same noun as the data source above — Terraform's `resource` vs
# `data` block types disambiguate. The data source returns the global
# catalog entry (read-only); the resource manages the tenant's binding.
data "ferentin_llm_provider" "anthropic" {
  slug = "anthropic"
}

resource "ferentin_llm_provider" "anthropic_prod" {
  provider_type = data.ferentin_llm_provider.anthropic.slug
  instance_name = "anthropic-prod-us"
  display_name  = "Anthropic (US prod)"
  priority      = 100

  # WriteOnly. The value flows to the platform during apply but never
  # enters state. To rotate the key, bump api_key_wo_version.
  api_key            = var.anthropic_api_key
  api_key_wo_version = 1
}

# --- LLM governance policy ----------------------------------------------
resource "ferentin_llm_policy" "default" {
  name        = "default-llm-policy"
  description = "Baseline LLM governance for all internal users"
  priority    = 100
  enabled     = true

  criteria = [
    {
      operator    = "AND"
      type        = "user"
      description = "All internal employees"
      conditions = [
        {
          # `value` is JSON-encoded on every resource that carries criteria —
          # Terraform has no "any" type, so a JSON string is how a condition
          # compares against non-string claims too. A bare "@example.com" is
          # not valid JSON and fails at apply.
          field    = "email"
          operator = "endswith"
          value    = jsonencode("@example.com")
        }
      ]
    }
  ]

  provider_instances = [ferentin_llm_provider.anthropic_prod.instance_id]
}

# --- MCP server (federated OAuth upstream) ------------------------------
# The singular ferentin_mcp_provider data source looks up by provider_id
# (a UUID); to resolve the catalog entry by its stable slug, filter the
# plural data source instead.
data "ferentin_mcp_providers" "all" {}

resource "ferentin_mcp_server" "salesforce_us" {
  name        = "salesforce-prod-us"
  provider_id = one([for p in data.ferentin_mcp_providers.all.providers : p.provider_id if p.slug == "salesforce"])
  endpoint    = "https://mcp.salesforce.example.com/sse"

  transport_type         = "streamable_http"
  deployment_mode        = "edge_routed"
  upstream_auth_strategy = "oauth2_user"
  edge_site_id           = ferentin_edge_site.primary.site_id
  priority               = 100
}

# --- MCP allow-policy with rate limit -----------------------------------
resource "ferentin_mcp_policy" "salesforce_allow" {
  name        = "salesforce-read-allow"
  description = "Allow Salesforce read operations, deny mutations"
  priority    = 100
  enabled     = true

  effect = {
    type                  = "allow"
    message               = "Read-only Salesforce access granted"
    allowed_tools         = ["query", "describe", "list_records"]
    rate_limit_per_minute = 120
  }

  provider_instances = [ferentin_mcp_server.salesforce_us.server_id]
}

# --- Data & Content Protection (DLP) policy -----------------------------
# Tokenize US PII and log exfiltration URLs across LLM and MCP traffic. The
# profile name is pulled from the catalog data source for typo-safety.
data "ferentin_data_protection_profile" "us_pii" {
  name = "US_PII"
}

resource "ferentin_data_protection_policy" "baseline" {
  name        = "baseline-data-protection"
  description = "Tokenize US PII; log exfiltration URLs on responses"
  priority    = 100
  enabled     = true

  enabled_profiles = [
    data.ferentin_data_protection_profile.us_pii.name,
    "EXFILTRATION_DEFENSE",
  ]

  effects = {
    "US_SSN"           = "tokenize"
    "EXFILTRATION_URL" = "log"
  }
  default_effect = "redact"

  # FPE key is required by the platform whenever an effect is "tokenize".
  fpe_key_id  = "dlp-fpe-prod"
  tweak_scope = "conversation"

  apply_to_llm_input  = true
  apply_to_llm_output = true
  apply_to_mcp_output = true
}

# --- OTEL sink + policy --------------------------------------------------
resource "ferentin_otel_sink" "honeycomb" {
  name        = "honeycomb-prod"
  endpoint    = "https://api.honeycomb.io"
  sink_type   = "OTLP_HTTP"
  protocol    = "HTTP"
  compression = "gzip"
}

resource "ferentin_otel_policy" "all_traces" {
  name        = "trace-everything"
  description = "Forward all traces to Honeycomb"
  priority    = 100
  enabled     = true
  signals     = ["traces"]
  sink_ids    = [ferentin_otel_sink.honeycomb.sink_id]
}

# --- AI agent OIDC client -----------------------------------------------
resource "ferentin_ai_agent" "claude_assistant" {
  name                       = "claude-team-assistant"
  agent_platform             = "claude"
  application_type           = "SERVICE"
  token_endpoint_auth_method = "client_secret_basic"
  description                = "Claude-based team assistant"
  scopes                     = ["llm", "mcp"]

  # Required for a machine-to-machine agent. The platform generates
  # `ai_client_type` from this alone — client_credentials present -> "agent",
  # otherwise -> "assistant" — and its own default omits it, so a SERVICE agent
  # without this line is stored as an assistant that cannot mint a token
  # without a user.
  grant_types = ["client_credentials"]
}

# --- Endpoint: device groups --------------------------------------------
# Groups are the targeting dimension for everything below. Declaring them here
# means the rules and posture overrides reference an attribute instead of a
# hardcoded UUID.
resource "ferentin_device_group" "engineering" {
  name        = "engineering"
  description = "Engineering laptops enrolled via MDM"
  source      = "mdm"
  external_id = "jamf-smart-group-42"
}

resource "ferentin_device_group" "contractors" {
  name        = "contractors"
  description = "Third-party contractors on BYOD hardware"
  source      = "manual"
}

# --- Endpoint: destination rules ----------------------------------------
# Evaluated on-device, FIRST MATCH WINS by ascending priority. Priority is
# policy semantics here, so keep ownership of it in Terraform — the console's
# reorder arrows rewrite the same field and the two will fight.

# Contractors may not use the ChatGPT desktop app at all.
resource "ferentin_endpoint_destination_rule" "block_chatgpt_contractors" {
  name        = "block-chatgpt-contractors"
  description = "Contractors may not use the ChatGPT desktop app"
  priority    = 10

  destination_kind = "ai_provider"
  catalog_slug     = "openai"
  action           = "block"

  app_bundle_ids   = ["com.openai.chat"]
  device_group_ids = [ferentin_device_group.contractors.group_id]
}

# Everyone else's Anthropic traffic is steered through service-edge, where the
# llm_policy and data_protection_policy above actually apply. The device never
# holds the provider key — service-edge presents it.
resource "ferentin_endpoint_destination_rule" "steer_anthropic" {
  name        = "steer-anthropic-to-edge"
  description = "Route Claude traffic through service-edge so LLM + DLP policy applies"
  priority    = 20

  destination_kind = "ai_provider"

  # Deliberately a literal, NOT data.ferentin_llm_provider.anthropic.slug.
  # These are two different catalogs: the LLM data source reads the LLM
  # provider catalog (what a ferentin_llm_provider binding consumes), while
  # this field is an `ai_platform_catalog` slug the endpoint agent resolves
  # hosts from. They happen to agree on "anthropic"; wiring one to the other
  # would assert a guarantee the platform does not make.
  catalog_slug = "anthropic"
  action       = "steer"

  # Must be https:// — http:// would downgrade every steered flow on the fleet.
  steer_to_url = var.service_edge_url
}

# Explicit deny list, fleet-wide. No device_group_ids = every device in the
# tenant, including ungrouped ones.
resource "ferentin_endpoint_destination_rule" "block_unsanctioned" {
  name     = "block-unsanctioned-hosts"
  priority = 100

  destination_kind  = "host"
  destination_hosts = ["api.unsanctioned-ai.example", "*.shadow-llm.example"]
  action            = "block"
}

# --- Endpoint: posture ---------------------------------------------------
# Tenant default (no device_group_id): observe and report, enforce nothing.
# This is the visibility-first posture — tightening is a deliberate act.
#
# NOTE: this resource is an upsert, and `terraform destroy` on the tenant
# default row does NOT reset it — the fleet keeps enforcing the last applied
# posture. Apply a permissive posture first if you mean to stand enforcement
# down.
resource "ferentin_endpoint_policy_settings" "default" {
  unapproved_mcp_action      = "report_only"
  default_destination_action = "allow"

  # Approved MCP client configs on the device are rewritten to point here.
  mcp_gateway_url = var.mcp_gateway_url
}

# Contractors get the strict posture: quarantine unapproved MCP configs, treat
# the destination rules as an allowlist, and close the SNI side-channels.
resource "ferentin_endpoint_policy_settings" "contractors" {
  device_group_id = ferentin_device_group.contractors.group_id

  unapproved_mcp_action = "quarantine"
  mcp_gateway_url       = var.mcp_gateway_url

  # With "block", the rules above become an allowlist for this group — verify
  # they cover every sanctioned provider before flipping this.
  default_destination_action = "block"

  ech_strip_enabled  = true
  doh_block_enabled  = true
  quic_block_enabled = true
}

# --- Outputs (handy for downstream Terraform / CI) ----------------------
output "edge_site_synthetic_id" {
  description = "Server-generated UUID for the primary edge site."
  value       = ferentin_edge_site.primary.synthetic_id
}

output "agent_client_id" {
  description = "fc_* client_id the agent presents at runtime."
  value       = ferentin_ai_agent.claude_assistant.client_id
}

output "agent_client_secret" {
  description = "Agent's OIDC client_secret. Only valid until next destroy/recreate."
  value       = ferentin_ai_agent.claude_assistant.client_secret
  sensitive   = true
}

output "device_group_ids" {
  description = "Group UUIDs, keyed by name — what endpoint policy targets."
  value = {
    engineering = ferentin_device_group.engineering.group_id
    contractors = ferentin_device_group.contractors.group_id
  }
}

output "endpoint_posture_drift" {
  description = <<-EOT
    Provenance of the tenant-default posture row. `managed_by` is the original
    creator, `last_modified_by` the most recent writer — "iac" + "console"
    means somebody changed a Terraform-managed row in the admin console.
  EOT
  value = {
    managed_by       = ferentin_endpoint_policy_settings.default.managed_by
    last_modified_by = ferentin_endpoint_policy_settings.default.last_modified_by
    version          = ferentin_endpoint_policy_settings.default.version
  }
}
