# Every endpoint MCP rule (server, tool call and behaviour rules), in
# evaluation order (ascending priority).
data "ferentin_endpoint_mcp_rules" "all" {}

# Rules the fleet does not enforce as written.
output "not_enforced" {
  value = [for r in data.ferentin_endpoint_mcp_rules.all.rules : "${r.name}: ${r.enforcement}" if r.enforcement != "active"]
}
