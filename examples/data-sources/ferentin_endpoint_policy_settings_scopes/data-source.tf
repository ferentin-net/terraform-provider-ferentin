# Every endpoint posture row: the tenant default, then each device-group
# override. A group with no override is not listed.
data "ferentin_endpoint_policy_settings_scopes" "all" {}

# Where call-time MCP blocks deny rather than only count.
output "enforcing_scopes" {
  value = [for s in data.ferentin_endpoint_policy_settings_scopes.all.scopes : coalesce(s.device_group_id, "tenant default") if s.hook_rule_enforcement == "enforce"]
}
