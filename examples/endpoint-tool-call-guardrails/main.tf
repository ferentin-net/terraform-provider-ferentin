# Guardrails for coding agents on managed endpoints: block the tool calls you
# never want an agent to make, count them first, then deny them, one device
# group at a time.
#
# Tool call rules are decided at call time, through the agent hook, and their
# action is always `block`. Whether a match DENIES the call or only COUNTS it
# is `hook_rule_enforcement` on the posture of the device's scope:
#
#   shadow  — count what the rules would deny; the call runs (the default)
#   enforce — deny the call before it runs (Claude Code and Cursor today)
#
# The rules, first match wins by ascending priority:
#
#   10  block  exfiltration commands (curl, wget, …)   every device
#   20  block  the shell tool                            pilot group
#
# The pilot group gets its own posture row, so it can move to `enforce` while
# the rest of the tenant keeps counting.
#
# `terraform apply` STAGES the change. Devices pick it up only after someone
# activates the pending changes in the admin console.

terraform {
  required_version = ">= 1.11"

  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

provider "ferentin" {
  # endpoint + tenant_id auto-default. See the root README for auth options.
  # Needs `policy:rw` for the rules and posture, and `devices:groups:rw` for
  # the device group.
  profile = var.profile
}

resource "ferentin_device_group" "pilot" {
  name        = var.pilot_group_name
  description = "Devices that move to enforce first"
  source      = "manual"
}

# 10. Commands an agent should never run, whichever agent runs them. Matched
#     however the line spells them: `curl`, `/usr/bin/curl`, `sudo curl`,
#     `cd /tmp && curl`.
resource "ferentin_endpoint_mcp_rule" "block_exfil_commands" {
  name         = "Block exfiltration commands from AI agents"
  priority     = 10
  match_kind   = "command_binary"
  match_values = var.blocked_commands
  action       = "block"
}

# 20. The agent's whole shell tool, for the pilot group only. A behaviour is
#     resolved into the tool name each client actually sends, so one rule
#     covers every client that can express it. Narrowing a block by device
#     group fails safe, so it is accepted.
resource "ferentin_endpoint_mcp_rule" "block_shell_pilot" {
  name             = "Block the shell tool on ${var.pilot_group_name}"
  priority         = 20
  match_kind       = "tool_behavior"
  match_values     = ["shell"]
  action           = "block"
  device_group_ids = [ferentin_device_group.pilot.group_id]
}

locals {
  # A group with its own posture row ignores the tenant default for every
  # setting, so the override restates the tenant posture and differs only in
  # hook_rule_enforcement. On a new row, an attribute left out takes the
  # platform's default (unapproved_mcp_action = quarantine), not the tenant's.
  shared_posture = {
    unapproved_mcp_action      = var.unapproved_mcp_action
    default_destination_action = var.default_destination_action
    mcp_gateway_url            = var.mcp_gateway_url
  }
}

# The tenant-default posture row. Only one Terraform configuration should own
# it: if another config already manages it, set hook_rule_enforcement there
# instead of applying both.
resource "ferentin_endpoint_policy_settings" "default" {
  unapproved_mcp_action      = local.shared_posture.unapproved_mcp_action
  default_destination_action = local.shared_posture.default_destination_action
  mcp_gateway_url            = local.shared_posture.mcp_gateway_url

  hook_rule_enforcement = var.tenant_hook_rule_enforcement
}

# The pilot group's override. Moving it to `enforce` is refused (409) until
# every active device in the group runs an agent build that can deny.
resource "ferentin_endpoint_policy_settings" "pilot" {
  device_group_id = ferentin_device_group.pilot.group_id

  unapproved_mcp_action      = local.shared_posture.unapproved_mcp_action
  default_destination_action = local.shared_posture.default_destination_action
  mcp_gateway_url            = local.shared_posture.mcp_gateway_url

  hook_rule_enforcement = var.pilot_hook_rule_enforcement
}

output "enforcement" {
  description = "What a matching tool call rule does in each scope, as applied (devices follow after activation)."
  value = {
    tenant                 = ferentin_endpoint_policy_settings.default.hook_rule_enforcement
    (var.pilot_group_name) = ferentin_endpoint_policy_settings.pilot.hook_rule_enforcement
  }
}

output "rule_enforcement" {
  description = "Whether the fleet enforces each rule (active, pending, not_enforced, never_matched), as of the last refresh."
  value = {
    (ferentin_endpoint_mcp_rule.block_exfil_commands.name) = ferentin_endpoint_mcp_rule.block_exfil_commands.enforcement
    (ferentin_endpoint_mcp_rule.block_shell_pilot.name)    = ferentin_endpoint_mcp_rule.block_shell_pilot.enforcement
  }
}
