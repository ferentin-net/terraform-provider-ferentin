# Tenant-custom MCP provider — defines an MCP server entry in the tenant's
# private catalog. The global catalog is read-only; this is for "your own
# upstream we want to expose".
resource "ferentin_mcp_provider" "internal_search" {
  display_name = "Internal Document Search"
  description  = "Tenant-private MCP provider for internal search."
  slug         = "internal-search"
  icon         = "search"
  owner        = "platform-team"
  contact      = "platform@example.com"
  default_url  = "https://search.internal.example.com/mcp/sse"
  transport    = "sse"
  category     = "search"

  enabled_scopes = [
    "search:read",
    "search:cite",
  ]
}
