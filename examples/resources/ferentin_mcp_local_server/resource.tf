# Approve a local (stdio) MCP server from the command line that runs it. `*`
# matches exactly one argument, so this covers okta-mcp-server from any
# checkout directory. The template ships verbatim to every managed device, so
# never put anything private in it. A `*` may stand for an argument after the
# package or program, or for a --directory, --project, -w or --tag value.
resource "ferentin_mcp_local_server" "okta" {
  display_name  = "Okta MCP"
  argv_template = ["uv", "run", "--directory", "*", "okta-mcp-server"]
  reason        = "Approved for the identity team's runbooks"
}

# A server launched with no arguments: one absolute path is enough. Any other
# template needs at least two literal tokens, and its first token can never
# be `*`.
resource "ferentin_mcp_local_server" "internal_tools" {
  display_name  = "Internal tools MCP"
  argv_template = ["/opt/acme/bin/tools-mcp"]
  reason        = "Platform team's internal tooling server"
}

# Approve a server from the local MCP catalog, keyed on its packages.
resource "ferentin_mcp_local_server" "playwright" {
  catalog_slug = "local-playwright"
}

# Block a server outright. A template names one command line, so it suits a
# bare binary or a script; to block a package wherever it runs, use a
# ferentin_mcp_policy deny whose target names the package. A server a device reported can
# be imported to manage its verdict this way; destroying it leaves it in place.
resource "ferentin_mcp_local_server" "unvetted" {
  display_name  = "Unvetted scraper"
  argv_template = ["/opt/tools/scraper-mcp", "--stdio"]
  status        = "blocked"
}
