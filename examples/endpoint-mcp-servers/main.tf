# Govern MCP servers on managed endpoints: steer the remote servers you have
# sanctioned through the tenant's MCP gateway, block the ones you have not,
# block known-bad local packages, and decide what happens to everything else.
#
# MCP rules are evaluated ON-DEVICE, first match wins by ascending priority:
#
#   10  block  unvetted remote MCP hosts
#   2x  steer  sanctioned remote servers → the tenant's MCP gateway route
#   30  block  known-bad local (stdio) packages
#
# Local servers your developers run are approved with ferentin_mcp_local_server,
# which keeps them governed through `ferentin-mcp`. MCP server configs no
# rule matches and nothing approves get the posture's `unapproved_mcp_action`.
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

# 10. Remote MCP servers nobody has vetted. `host` values are bare hostnames:
#     no scheme, port or path.
resource "ferentin_endpoint_mcp_rule" "block_unvetted_remote" {
  name         = "Block unvetted remote MCP servers"
  priority     = 10
  match_kind   = "host"
  match_values = var.blocked_remote_hosts
  action       = "block"
}

# 2x. Sanctioned remote servers. The agent rewrites a developer's MCP client
#     entry for these hosts to the tenant's gateway route for the server, so
#     every call crosses the gateway and its MCP policy. The route must belong
#     to an enabled MCP server in the tenant (ferentin_mcp_server, or the
#     admin console's MCP Servers registry).
resource "ferentin_endpoint_mcp_rule" "steer_sanctioned_remote" {
  for_each = var.steered_remote_servers

  name         = "Steer ${each.key} through the MCP gateway"
  priority     = each.value.priority
  match_kind   = "host"
  match_values = each.value.hosts
  action       = "steer"

  # https://<tenant-short-name>.mcp.ferentin.net/v1/mcp/<server-slug>
  steer_to_url = "${var.mcp_gateway_url}/mcp/${each.value.server_slug}"
}

# 30. Local (stdio) packages you never want started. A local block replaces
#     the developer's entry with a refusal that explains the block and offers
#     an appeal; the original is kept and restored when the block is lifted.
resource "ferentin_endpoint_mcp_rule" "block_local_packages" {
  name         = "Block known-bad local MCP packages"
  priority     = 30
  match_kind   = "package"
  match_values = var.blocked_local_packages
  action       = "block"
}

# Approved local (stdio) servers, by the command line each approval covers.
# `*` matches exactly one argument. Approving keeps the server governed; an
# endpoint `allow` rule would run it outside Ferentin instead.
resource "ferentin_mcp_local_server" "approved" {
  for_each = var.approved_local_servers

  display_name  = each.value.display_name
  argv_template = each.value.argv_template
}

# The tenant-default posture row, which every device resolves through. Only one
# Terraform configuration should own it: if another config (such as
# ../endpoint-approved-ai) already manages it, move these attributes there
# instead of applying both.
resource "ferentin_endpoint_policy_settings" "default" {
  # What happens to an MCP server config no rule matches and that is not
  # approved in the registry: report_only, quarantine or block.
  unapproved_mcp_action = var.unapproved_mcp_action

  # Approved server configs are rewritten to point here. Leaving it null
  # CLEARS it, and the agent stops rewriting MCP client configs.
  mcp_gateway_url = var.mcp_gateway_url

  # What developers get from a local MCP tool nobody has vetted, by its risk
  # rating. Tenant default only. An ask_once / ask_every_call tier needs
  # approval routing (local_mcp_approval_config) first.
  local_mcp_risk_policy = var.local_mcp_risk_policy

  default_destination_action = var.default_destination_action
}

output "mcp_rule_ladder" {
  description = "MCP rules in evaluation order. First match wins, so read top down."
  value = sort([
    for r in concat(
      [
        ferentin_endpoint_mcp_rule.block_unvetted_remote,
        ferentin_endpoint_mcp_rule.block_local_packages,
      ],
      values(ferentin_endpoint_mcp_rule.steer_sanctioned_remote),
    ) : format("%04d %-5s %s", r.priority, r.action, r.name)
  ])
}

output "rule_enforcement" {
  description = "Whether the fleet enforces each rule (active, pending, not_enforced, never_matched), as of the last refresh."
  value = merge(
    {
      (ferentin_endpoint_mcp_rule.block_unvetted_remote.name) = ferentin_endpoint_mcp_rule.block_unvetted_remote.enforcement
      (ferentin_endpoint_mcp_rule.block_local_packages.name)  = ferentin_endpoint_mcp_rule.block_local_packages.enforcement
    },
    { for r in ferentin_endpoint_mcp_rule.steer_sanctioned_remote : r.name => r.enforcement },
  )
}
