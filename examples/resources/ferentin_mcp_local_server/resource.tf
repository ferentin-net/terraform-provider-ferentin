# Approve a local (stdio) MCP server from the command line that runs it. `*`
# matches exactly one argument, so this covers okta-mcp-server from any
# checkout directory. The template ships verbatim to every managed device:
# put `*` where anything private would go.
resource "ferentin_mcp_local_server" "okta" {
  display_name  = "Okta MCP"
  argv_template = ["uv", "run", "--directory", "*", "okta-mcp-server"]
  reason        = "Approved for the identity team's runbooks"
}

# Approve a server from the local MCP catalog, keyed on its packages.
resource "ferentin_mcp_local_server" "playwright" {
  catalog_slug = "local-playwright"
}

# Block a server outright. Devices consult a template only for a command line
# that names no package, so a template suits a bare binary or a script; block
# a package with an endpoint MCP rule instead. A server a device reported can
# be imported to manage its verdict this way; destroying it leaves it in place.
resource "ferentin_mcp_local_server" "unvetted" {
  display_name  = "Unvetted scraper"
  argv_template = ["/opt/tools/scraper-mcp", "--stdio"]
  status        = "blocked"
}
