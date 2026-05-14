# Configure an MCP server from a discovered server-card.json.
#
# The MCP spec (https://modelcontextprotocol.io) defines a standard
# server-card.json that exposes a server's identity, transport, capabilities,
# and credential requirements. `ferentin mcp discover <url>` writes one for
# any reachable MCP server.
#
# `ferentin_mcp_server_from_card` wraps the platform's
# `POST /admin/tenants/{tid}/mcp-providers/import-server-card` endpoint so a
# single resource owns the provider catalog entry + the server-instance
# binding. Bumping the card (new version, new auth shape, new tools) and
# re-applying refreshes the pair; the platform performs a checksum match
# and skips writes when the bytes are unchanged.

terraform {
  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

provider "ferentin" {
  # endpoint + tenant_id auto-default — see the README for auth options.
  profile = "prod"
}

resource "ferentin_mcp_server_from_card" "threat_intel" {
  card_json = file("${path.module}/server-card.json")

  # When the card's remote URL is private (RFC1918, .local, .internal,
  # unresolvable DNS), the platform creates the provider in `draft`
  # status unless an edge_site_id is supplied — assigning one promotes
  # the row to `published`. Most local-dev cards need this.
  edge_site_id = ferentin_edge_site.local_dev.site_id

  # Bearer token the upstream MCP expects. Sugar for
  # `env = { BEARER_TOKEN = ... }` — the common single-token case.
  # Encrypted server-side at rest; sensitive — redacted in plan output.
  bearer_token = var.threat_intel_bearer_token
}

resource "ferentin_edge_site" "local_dev" {
  site_id   = "local-dev"
  site_name = "Local Dev"
}

variable "threat_intel_bearer_token" {
  type        = string
  description = "Bearer token the upstream threat-intel MCP expects. WriteOnly."
  sensitive   = true
}

output "client_facing_url" {
  description = "Client-facing URL Terraform-managed agents will connect to."
  value       = ferentin_mcp_server_from_card.threat_intel.client_facing_url
}

output "import_action" {
  description = "What the last import did — created / refreshed / unchanged."
  value       = ferentin_mcp_server_from_card.threat_intel.import_action
}

output "import_unchanged" {
  description = "True iff the last import was a checksum-matched no-op."
  value       = ferentin_mcp_server_from_card.threat_intel.import_unchanged
}
