# The MCP scope defaults of the tenant default and of every device group with a
# posture row of its own.
data "ferentin_mcp_scope_defaults_scopes" "all" {}

# Scopes still rehearsing their server approval default.
output "rehearsing_server_approval" {
  value = [for s in data.ferentin_mcp_scope_defaults_scopes.all.scopes : coalesce(s.device_group_id, "tenant default") if s.server_approval_mode == "report_only"]
}
