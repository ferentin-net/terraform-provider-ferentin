# Approved AI on managed endpoints: allow the sanctioned providers, pin one of
# them to its signed desktop app, and block the rest.
#
# Destination rules are evaluated ON-DEVICE, first match wins by ascending
# priority:
#
#   10  allow  Anthropic, from any app
#   20  allow  OpenAI, only from the ChatGPT desktop app (by Apple Team ID)
#   30  block  OpenAI from everything else (browsers, scripts, other apps)
#   40  block  DeepSeek
#
# Everything no rule matches falls through to the tenant-default posture's
# `default_destination_action` (allow, unless you set it to block).
#
# One tenant, no device groups and no service-edge: this is the starting point.
# ../endpoint-fleet/ adds groups, steering through service-edge, MCP rules and
# a staged rollout.
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
  # Needs `policy:rw` for the rules and the posture.
  profile = var.profile
}

locals {
  # One host list for the allow and the block that follows it, so the two
  # rules cannot drift apart. A bare host matches only itself; a leading dot
  # matches the domain and every host under it. Prefer ".example.com" to
  # "*.example.com": agents without ferentin-endpoint#1246 refuse the `*.`
  # form, and one refused host makes an agent refuse the whole policy.
  openai_hosts = ["api.openai.com", ".chatgpt.com"]
}

resource "ferentin_endpoint_destination_rule" "approved_anthropic" {
  name              = "Approved AI: Anthropic"
  priority          = 10
  action            = "allow"
  destination_kind  = "host"
  destination_hosts = [".anthropic.com", ".claude.ai"]
}

# The allow is narrowed to one signed app; the block below catches every other
# app reaching the same hosts. Swap the order and the block wins for everyone.
resource "ferentin_endpoint_destination_rule" "approved_chatgpt_desktop" {
  name              = "Approved AI: ChatGPT desktop"
  priority          = 20
  action            = "allow"
  destination_kind  = "host"
  destination_hosts = local.openai_hosts
  app_team_ids      = var.chatgpt_team_ids
}

resource "ferentin_endpoint_destination_rule" "block_openai_elsewhere" {
  name              = "Block OpenAI outside approved apps"
  priority          = 30
  action            = "block"
  destination_kind  = "host"
  destination_hosts = local.openai_hosts
}

resource "ferentin_endpoint_destination_rule" "block_unapproved" {
  name              = "Block unapproved AI"
  priority          = 40
  action            = "block"
  destination_kind  = "host"
  destination_hosts = var.blocked_hosts
}

# The tenant-default posture row, which every device (grouped or not) resolves
# through. This is an upsert: on a tenant that already has posture, apply
# ADOPTS the row instead of failing. Read the first plan before approving it,
# because the attributes below are written on every apply.
resource "ferentin_endpoint_policy_settings" "default" {
  default_destination_action = var.default_destination_action

  # Port-443 flows to bare public IPs have no hostname for a rule to match.
  # Off by default: on a working Mac it also catches VPNs and other
  # infrastructure reached by IP, so list those in the exemptions first.
  unnamed_flow_block_enabled = var.unnamed_flow_block_enabled
  unnamed_flow_exempt_cidrs  = var.unnamed_flow_exempt_cidrs

  # Leaving this null CLEARS a gateway URL the tenant already has, and the
  # agent stops rewriting MCP client configs. Set it to keep one.
  mcp_gateway_url = var.mcp_gateway_url
}

output "rule_ladder" {
  description = "Destination rules in evaluation order. First match wins, so read top down."
  value = sort([
    for r in [
      ferentin_endpoint_destination_rule.approved_anthropic,
      ferentin_endpoint_destination_rule.approved_chatgpt_desktop,
      ferentin_endpoint_destination_rule.block_openai_elsewhere,
      ferentin_endpoint_destination_rule.block_unapproved,
    ] : format("%04d %-5s %s", r.priority, r.action, r.name)
  ])
}

output "posture_provenance" {
  description = <<-EOT
    Provenance of the tenant-default posture row. `managed_by = "console"`
    means Terraform adopted a row first configured in the admin console;
    `last_modified_by = "console"` on a Terraform-managed row means someone
    edited it by hand since the last apply.
  EOT
  value = {
    managed_by       = ferentin_endpoint_policy_settings.default.managed_by
    last_modified_by = ferentin_endpoint_policy_settings.default.last_modified_by
    version          = ferentin_endpoint_policy_settings.default.version
  }
}
