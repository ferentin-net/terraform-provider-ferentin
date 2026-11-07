# Every MCP policy in the tenant, remote and local, by priority then name.
data "ferentin_mcp_policies" "all" {}

# Policies that only record what they would decide.
output "rehearsing" {
  value = [for p in data.ferentin_mcp_policies.all.policies : p.name if p.mode == "report_only"]
}

# Policies created in the console, with the import ID each one takes.
output "console_policies" {
  value = { for p in data.ferentin_mcp_policies.all.policies : p.name => p.import_id if p.managed_by == "console" }
}
