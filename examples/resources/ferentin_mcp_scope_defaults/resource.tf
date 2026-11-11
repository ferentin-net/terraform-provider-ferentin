# MCP scope defaults: what a local MCP server or tool meets when no MCP policy
# applies to it. Omit device_group_id for the tenant default; set it for one
# device group.
#
# Each default is sent only when it is set here; one left out keeps what the
# tenant has. A default's mode is `enforce`, or `report_only` to rehearse it:
# devices act as if it were report_only and the platform records what it would
# have refused. Unset, the mode is enforce for a value that refuses.
#
# NOTE: these defaults live on the scope's endpoint posture row, the same row
# ferentin_endpoint_policy_settings writes. `terraform destroy` leaves them in
# force (the platform cannot delete them); apply report_only values first to
# stand them down.

# The device group the second block targets. Declared here so the snippet
# stands on its own; see ferentin_device_group for the full resource.
resource "ferentin_device_group" "contractors" {
  name   = "contractors"
  source = "manual"
}

# Tenant default: observe first. Rehearse quarantining unapproved local MCP
# servers, and record what blocking unattributed ones would refuse.
resource "ferentin_mcp_scope_defaults" "tenant" {
  server_approval      = "quarantine"
  server_approval_mode = "report_only"

  unattributed      = "block"
  unattributed_mode = "report_only"

  # Unvetted tools on approved local servers, by risk rating. An ask_once /
  # ask_every_call tier needs approval routing.
  tool_vetting_mode = "enforce"
  tool_vetting_risk_policy = {
    low    = "allow"
    medium = "ask_once"
    high   = "deny"
  }
  tool_vetting_approval_config = jsonencode({
    step_up_mode    = "approve"
    timeout_seconds = 900
    channels        = [{ type = "invocation_context" }]
  })

  # Credit a ferentin_mcp_policy grant that names an AI agent (subject.agents)
  # on a local server only when the device verified the agent's code
  # signature. Off by default, which credits no such grant. A deny naming an
  # agent applies whatever this says.
  agent_evidence_floor = "lineage_signature"
}

# Contractors: enforce. Tool vetting follows the tenant default's.
resource "ferentin_mcp_scope_defaults" "contractors" {
  device_group_id = ferentin_device_group.contractors.group_id

  server_approval   = "block"
  unattributed      = "block"
  tool_vetting_mode = "inherit"
}
