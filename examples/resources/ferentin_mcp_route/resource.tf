# MCP routes send an MCP server a managed device starts through Ferentin. In the
# unified MCP policy model a route is what an endpoint MCP rule's "steer" became;
# allowing or denying a server is ferentin_mcp_policy's job, with device targets.

# The device group the scoped route targets. Declared here so the snippet stands
# on its own; see ferentin_device_group for the full resource.
resource "ferentin_device_group" "engineering" {
  name   = "engineering"
  source = "manual"
}

# Remote MCP server: rewrite it to a route on this tenant's MCP gateway, for
# engineering's devices only. steer_to_url must be this tenant's gateway or one
# of its edge sites; leave it unset to use the scope's own gateway.
resource "ferentin_mcp_route" "github_through_gateway" {
  name = "github-through-gateway"

  target = {
    kind   = "host"
    values = ["api.githubcopilot.com"]
  }
  steer_to_url     = "https://acme.mcp.ferentin.net/v1/github"
  device_group_ids = [ferentin_device_group.engineering.group_id]
}

# Local (stdio) MCP server: wrap one npm package in ferentin-mcp, so its tool
# calls pass through Ferentin. A local route has no URL.
resource "ferentin_mcp_route" "wrap_internal_package" {
  name        = "wrap-internal-mcp-package"
  description = "Govern the internal MCP server wherever it runs"

  target = {
    kind   = "package"
    values = ["npm:@acme/internal-mcp"]
  }

  # Only in this MCP client's config files.
  app_bundle_ids = ["com.anthropic.claudefordesktop"]
}
