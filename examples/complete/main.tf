# End-to-end example: a single tenant configured with one edge site, one
# Anthropic LLM provider instance, one MCP server federating an OAuth-backed
# upstream, and matching governance policies. Apply order is implicit from
# attribute references — no `depends_on` needed.

terraform {
  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

provider "ferentin" {
  endpoint      = var.endpoint
  tenant_id     = var.tenant_id
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

# --- LLM provider instance ----------------------------------------------
data "ferentin_llm_provider" "anthropic" {
  slug = "anthropic"
}

resource "ferentin_llm_provider_instance" "anthropic_prod" {
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
          field    = "email"
          operator = "endswith"
          value    = "@example.com"
        }
      ]
    }
  ]

  provider_instances = [ferentin_llm_provider_instance.anthropic_prod.instance_name]
}

# --- MCP server (federated OAuth upstream) ------------------------------
data "ferentin_mcp_provider" "salesforce" {
  slug = "salesforce"
}

resource "ferentin_mcp_server" "salesforce_us" {
  name        = "salesforce-prod-us"
  provider_id = data.ferentin_mcp_provider.salesforce.id
  endpoint    = "https://mcp.salesforce.example.com/sse"

  transport_type         = "streamable_http"
  deployment_mode        = "edge_routed"
  upstream_auth_strategy = "oauth2_user"
  edge_site_id           = ferentin_edge_site.primary.synthetic_id
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

  provider_instances = [ferentin_mcp_server.salesforce_us.name]
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
