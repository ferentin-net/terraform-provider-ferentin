# Every endpoint MCP rule, in evaluation order (ascending priority). This lists
# server rules written in the console too, although ferentin_endpoint_mcp_rule
# authors tool call and behaviour rules only.
data "ferentin_endpoint_mcp_rules" "all" {}

# Rules the fleet does not enforce as written.
output "not_enforced" {
  value = [for r in data.ferentin_endpoint_mcp_rules.all.rules : "${r.name}: ${r.enforcement}" if r.enforcement != "active"]
}
