# Every MCP route in the tenant, by name.
data "ferentin_mcp_routes" "all" {}

# Where each remote route sends its server.
output "remote_routes" {
  value = { for r in data.ferentin_mcp_routes.all.routes : r.name => r.steer_to_url if r.mechanism == "rewrite_to_gateway" }
}
