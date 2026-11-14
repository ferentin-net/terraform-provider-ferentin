# One MCP policy model covers remote MCP servers reached through the gateway
# and local MCP servers started on managed devices. A policy selects servers by
# provider instance, by device target, or both.

# `provider_instances` takes MCP server UUIDs, not names. The platform stores
# and echoes UUIDs, so a name would drift on every plan; the provider rejects a
# non-UUID at plan time rather than letting that reach the API. In a real
# config this is `ferentin_mcp_server.<name>.server_id`.
variable "internal_search_server_id" {
  type        = string
  description = "UUID of the MCP server these policies govern."
}

# Allow, by provider instance: engineering-only access to internal search.
resource "ferentin_mcp_policy" "engineering_search" {
  name        = "engineering-internal-search"
  description = "Engineering users can query internal search"
  priority    = 50

  provider_instances = [var.internal_search_server_id]

  criteria = [{
    operator = "AND"
    type     = "claims"
    conditions = [{
      field    = "groups"
      operator = "contains"
      value    = jsonencode("Engineering")
    }]
  }]

  effect = {
    type                  = "allow"
    grant_toolsets        = ["search:read", "search:cite"]
    rate_limit_per_minute = 30
    message               = "Internal search granted via engineering policy."
  }
}

# Default deny that catches everything else (a higher priority number is a
# lower precedence).
resource "ferentin_mcp_policy" "default_deny" {
  name     = "default-deny-internal-search"
  priority = 1000

  provider_instances = [var.internal_search_server_id]

  effect = {
    type    = "deny"
    message = "Access to internal search requires the Engineering group."
  }
}

# Deny, by device target: a remote host and a local package nobody may start
# on a managed device. The host target also reaches the device network filter.
# The Security group is exempt.
resource "ferentin_mcp_policy" "block_unvetted" {
  name     = "block-unvetted-mcp"
  priority = 10

  targets = [
    { kind = "host", values = ["mcp.unvetted.example"] },
    { kind = "package", values = ["npm:@evil/mcp-server"] },
  ]

  effect = {
    type    = "deny"
    message = "This MCP server has not been vetted. Ask #security for a review."
  }

  unless = {
    groups = [{
      operator = "AND"
      type     = "claims"
      conditions = [{
        field    = "groups"
        operator = "contains"
        value    = jsonencode("Security")
      }]
    }]
  }
}

# Allow, by device target: let one internal package start on devices. With
# targets and no provider instance, applies_at defaults to "admit": the policy
# lifts the server default where the server starts.
resource "ferentin_mcp_policy" "allow_internal_package" {
  name     = "allow-internal-mcp-package"
  priority = 20

  targets = [
    { kind = "package", values = ["npm:@acme/internal-mcp"] },
    # Only in this MCP client.
    { kind = "app_bundle_id", values = ["com.anthropic.claudefordesktop"] },
  ]

  effect = {
    type = "allow"
  }
}

# Deny named tools, by device target: the internal package may start (above),
# but its write tools are refused at each call. A deny refuses only the tools
# it names. On a target only a bare tool name counts (a target has no slug), and
# a deny that names tools cannot also name app_bundle_id targets.
resource "ferentin_mcp_policy" "internal_package_read_only" {
  name     = "internal-mcp-package-read-only"
  priority = 15

  targets = [
    { kind = "package", values = ["npm:@acme/internal-mcp"] },
  ]

  effect = {
    type         = "deny"
    denied_tools = ["write_*", "delete_record"]
    message      = "The internal MCP server is read-only on managed devices."
  }
}

# Rehearse before enforcing: report_only records what the policy would have
# refused on the audit trail and decides nothing.
resource "ferentin_mcp_policy" "rehearse_block_remote" {
  name     = "rehearse-block-remote-mcp"
  priority = 30
  mode     = "report_only"

  targets = [
    { kind = "class", values = ["remote"] },
  ]

  effect = {
    type = "deny"
  }
}
