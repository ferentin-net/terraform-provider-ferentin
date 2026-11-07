# Endpoint MCP rules block tool calls on managed devices: by MCP tool name, by
# the binary a shell call runs, or by tool behaviour. They are evaluated
# on-device, FIRST MATCH WINS by ascending priority.
#
# Whether a rule denies the call or only counts it is set per scope by
# hook_rule_enforcement on ferentin_endpoint_policy_settings.
#
# Allowing or blocking an MCP server is a ferentin_mcp_policy with targets,
# and steering one is a ferentin_mcp_route.

# The device group the scoped rule targets. Declared here so the snippet stands
# on its own; see ferentin_device_group for the full resource.
resource "ferentin_device_group" "contractors" {
  name   = "contractors"
  source = "manual"
}

# Tool behaviour: block shell execution from AI tools, for every vendor whose
# built-in tools can express it.
resource "ferentin_endpoint_mcp_rule" "block_shell" {
  name         = "block-agent-shell"
  priority     = 10
  match_kind   = "tool_behavior"
  match_values = ["shell"]

  device_group_ids = [ferentin_device_group.contractors.group_id]
}

# Command binary: block shell calls that run these binaries, for everyone.
resource "ferentin_endpoint_mcp_rule" "block_exfil_commands" {
  name         = "block-exfil-commands"
  priority     = 20
  match_kind   = "command_binary"
  match_values = ["curl", "scp", "nc"]
}

# Tool name: block one MCP tool, only in this MCP client.
resource "ferentin_endpoint_mcp_rule" "block_delete_repo" {
  name         = "block-delete-repo"
  priority     = 30
  match_kind   = "tool_name"
  match_values = ["delete_repository"]

  app_bundle_ids = ["com.anthropic.claudefordesktop"]
}
