# Stamp out a tenant-scoped MCP provider + server instance from an
# MCP-spec server-card.json in a single resource. The platform owns the
# slug / transport / auth-mode mapping; the resource owns BOTH halves of
# the imported pair as one lifecycle unit (destroy drops both).

# Minimal — just the card. The provider+instance land with platform
# defaults; client_facing_url is exported as a computed output.
resource "ferentin_mcp_server_from_card" "threat_intel" {
  card_json = file("${path.module}/server-card.json")
}

# Common case — bind to an edge site (required when the card's remote
# URL is HTTP, .local/.internal, or unresolvable), set the bearer token
# the upstream MCP expects, and override the platform's default instance
# name.
resource "ferentin_mcp_server_from_card" "threat_intel_edge" {
  card_json     = file("${path.module}/server-card.json")
  edge_site_id  = ferentin_edge_site.local_dev.site_id
  instance_name = "threat-intel-prod"

  # Sugar for env = { BEARER_TOKEN = ... } — the common case where the
  # card declares a single BEARER_TOKEN credential field. Drop to `env`
  # when the upstream's credential names something else (e.g. API_KEY).
  bearer_token = var.threat_intel_bearer_token

  enabled  = true
  priority = 100
}

variable "threat_intel_bearer_token" {
  type      = string
  sensitive = true
}

resource "ferentin_edge_site" "local_dev" {
  site_id   = "local-dev"
  site_name = "Local Dev"
}

# Read the provider's tools list (informational — what the card shipped)
# from the import_result delta. `action` is `created` on first import,
# `refreshed` when tools changed, or `unchanged` when the checksum
# matched (no writes happened).
output "import_action" {
  description = "What changed on the last import — created / refreshed / unchanged."
  value       = ferentin_mcp_server_from_card.threat_intel_edge.import_action
}

output "client_facing_url" {
  description = "Client-facing URL Terraform-managed agents will connect to."
  value       = ferentin_mcp_server_from_card.threat_intel_edge.client_facing_url
}
