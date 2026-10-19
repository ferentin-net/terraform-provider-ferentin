# Endpoint MCP rules govern MCP servers and tool calls on managed devices.
# They are evaluated on-device, FIRST MATCH WINS by ascending priority.

# The device group the scoped rules target. Declared here so the snippet stands
# on its own; see ferentin_device_group for the full resource.
resource "ferentin_device_group" "contractors" {
  name   = "contractors"
  source = "manual"
}

# Remote MCP server: block a host outright for contractors. Narrowing a block
# by device group fails safe, so it is accepted.
resource "ferentin_endpoint_mcp_rule" "block_unvetted_remote" {
  name             = "block-unvetted-remote"
  priority         = 10
  match_kind       = "host"
  match_values     = ["mcp.unvetted.example"]
  action           = "block"
  device_group_ids = [ferentin_device_group.contractors.group_id]
}

# Local (stdio) MCP server: allow one npm package. An allow puts the entry
# back exactly as written, so the server runs outside Ferentin and nothing sees
# its tool calls; to keep a local server governed, approve it with
# ferentin_mcp_local_server instead. A local allow must also be tenant-wide
# for now: devices carry no authenticated identity when a local server is
# admitted, so device_group_ids or criteria here are refused.
resource "ferentin_endpoint_mcp_rule" "allow_internal_package" {
  name         = "allow-internal-mcp-package"
  priority     = 20
  match_kind   = "package"
  match_values = ["npm:@acme/internal-mcp"]
  action       = "allow"

  # Only for this MCP client.
  app_bundle_ids = ["com.anthropic.claudefordesktop"]
}

# Tool behaviour: block shell execution from AI tools, for every vendor that
# can express it. Call-time and behaviour rules support "block" only, and
# whether they deny or only count is set by hook_rule_enforcement on
# ferentin_endpoint_policy_settings.
resource "ferentin_endpoint_mcp_rule" "block_shell" {
  name         = "block-agent-shell"
  priority     = 30
  match_kind   = "tool_behavior"
  match_values = ["shell"]
  action       = "block"
}
