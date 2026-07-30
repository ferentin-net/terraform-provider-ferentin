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
  default_url  = "https://search.internal.example.com/mcp"

  # `http` IS Streamable HTTP, and is the recommended transport — `sse` is the
  # legacy variant, `stdio` a local subprocess. The value is NOT spelled
  # `streamable_http` here; that spelling belongs to the downstream
  # `ferentin_mcp_server.transport_type`, a different enum on a different table.
  transport = "http"
  category  = "search"

  enabled_scopes = [
    "search:read",
    "search:cite",
  ]
}
