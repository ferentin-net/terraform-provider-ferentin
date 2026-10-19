# AI-agent decisions: which catalogued agents may run on managed devices.
# An agent with no decision falls through to unapproved_agent_action on the
# device's posture.

resource "ferentin_device_group" "contractors" {
  name   = "contractors"
  source = "manual"
}

# Approve Claude Code on every device. `report_only` approves: the agent runs
# and its presence is reported.
resource "ferentin_endpoint_agent_approval" "claude_code" {
  catalog_slug = "claude-code"
  action       = "report_only"
  description  = "Sanctioned coding agent"
}

# Deny OpenClaw for contractors only. Devices outside the group fall through
# to unapproved_agent_action, so this needs no second decision for everyone
# else.
resource "ferentin_endpoint_agent_approval" "openclaw_contractors" {
  catalog_slug     = "openclaw"
  action           = "block"
  device_group_ids = [ferentin_device_group.contractors.group_id]
  description      = "Denied pending security review"
}
