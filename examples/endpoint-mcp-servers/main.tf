# Govern MCP servers on managed endpoints: steer the remote servers you have
# sanctioned through the tenant's MCP gateway, block the ones you have not,
# block known-bad local packages, and decide what happens to everything else.
#
# One MCP policy model covers remote and local servers. On a device, policies
# select servers by `targets`, and a deny is decided before any allow:
#
#   deny   unvetted remote MCP hosts and known-bad local (stdio) packages
#   allow  sanctioned remote servers, which a route also sends through the
#          tenant's MCP gateway
#
# Local servers your developers run are approved with ferentin_mcp_local_server,
# which keeps them governed through `ferentin-mcp`. MCP server configs no
# policy decides and nothing approves get the `server_approval` scope default.
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
  # Needs `policy:rw`: for MCP policies that name device targets, the routes
  # and the posture.
  profile = var.profile
}

# Remote hosts nobody has vetted and local packages you never want started.
# `host` values are bare hostnames: no scheme, port or path. A local deny
# replaces the developer's entry with a refusal that explains the block and
# offers an appeal; the original is kept and restored when the block is lifted.
resource "ferentin_mcp_policy" "block_unvetted" {
  name     = "Block unvetted MCP servers"
  priority = 10

  targets = [
    { kind = "host", values = var.blocked_remote_hosts },
    { kind = "package", values = var.blocked_local_packages },
  ]

  effect = {
    type    = "deny"
    message = "This MCP server has not been vetted. Ask the security team for a review."
  }
}

# Sanctioned remote servers. The allow lifts the server_approval default where
# the server starts (applies_at defaults to "admit" for an allow with targets
# and no provider instance), and the route rewrites a developer's MCP client
# entry for these hosts to the tenant's gateway route for the server, so every
# call crosses the gateway and its MCP policies. The route must belong to an
# enabled MCP server in the tenant (ferentin_mcp_server, or the admin console's
# MCP Servers registry).
resource "ferentin_mcp_policy" "allow_sanctioned_remote" {
  for_each = var.steered_remote_servers

  name     = "Allow ${each.key}"
  priority = 20

  targets = [
    { kind = "host", values = each.value.hosts },
  ]

  effect = {
    type = "allow"
  }
}

resource "ferentin_mcp_route" "steer_sanctioned_remote" {
  for_each = var.steered_remote_servers

  name = "Steer ${each.key} through the MCP gateway"

  target = {
    kind   = "host"
    values = each.value.hosts
  }

  # https://<tenant-short-name>.mcp.ferentin.net/v1/mcp/<server-slug>
  steer_to_url = "${var.mcp_gateway_url}/mcp/${each.value.server_slug}"
}

# Approved local (stdio) servers, by the command line each approval covers.
# `*` matches exactly one argument. Approving keeps the server governed through
# ferentin-mcp.
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
  # Approved server configs are rewritten to point here. Leaving it null
  # CLEARS it, and the agent stops rewriting MCP client configs.
  mcp_gateway_url = var.mcp_gateway_url

  default_destination_action = var.default_destination_action
}

# The tenant default's MCP scope defaults, on the same posture row.
resource "ferentin_mcp_scope_defaults" "default" {
  # What happens to an MCP server config no policy decides and that is not
  # approved in the registry: report_only, quarantine or block.
  server_approval = var.server_approval

  # What developers get from a local MCP tool nobody has vetted, by its risk
  # rating. An ask_once / ask_every_call tier needs approval routing
  # (tool_vetting_approval_config) first.
  tool_vetting_risk_policy = var.tool_vetting_risk_policy
}

output "mcp_policies" {
  description = "The MCP policies this config writes, as priority, effect and name."
  value = sort([
    for p in concat(
      [ferentin_mcp_policy.block_unvetted],
      values(ferentin_mcp_policy.allow_sanctioned_remote),
    ) : format("%04d %-5s %s", p.priority, p.effect.type, p.name)
  ])
}

output "mcp_routes" {
  description = "Where each sanctioned remote server is steered."
  value       = { for k, r in ferentin_mcp_route.steer_sanctioned_remote : k => r.steer_to_url }
}
