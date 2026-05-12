# MCP server — a tenant binding of an MCP provider (from the catalog or
# a tenant-custom ferentin_mcp_provider) to a specific endpoint and
# routing/auth config. Multiple servers per provider are allowed.

resource "ferentin_mcp_server" "internal_search_us" {
  # provider_id references the MCP provider being instantiated. Pull from
  # ferentin_mcp_provider.<your_provider>.id or the global catalog.
  provider_id = "ad8a8c1f-1234-4567-89ab-cdef01234567"

  name        = "internal-search-us"
  endpoint    = "https://search.internal.example.com/mcp/sse"
  description = "US-region internal document search MCP server"

  enabled  = true
  priority = 100

  # Restrict to a specific scope subset of the provider's catalog.
  enabled_scopes = [
    "search:read",
    "search:cite",
  ]

  # Routing.
  deployment_mode        = "edge_routed"
  upstream_auth_strategy = "oauth2_shared"
  transport_type         = "sse"
  edge_site_id           = "prod-us-east-1a"

  tags = {
    env  = "prod"
    team = "platform"
  }
}

output "internal_search_url" {
  description = "Client-facing URL Terraform-managed agents will connect to."
  value       = ferentin_mcp_server.internal_search_us.client_facing_url
}
