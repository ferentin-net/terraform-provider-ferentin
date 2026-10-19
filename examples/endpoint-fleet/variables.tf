variable "profile" {
  type        = string
  description = "Shared-config profile holding the tokens `ferentin login --profile <name>` stashed in the OS keyring."
  default     = "prod"
}

variable "enforcement_stage" {
  type        = string
  description = <<-EOT
    Rollout stage for the fleet-wide posture. The destination rules are the
    same at every stage — only what happens to UNMATCHED traffic changes.

    Note this is not an enforcement kill switch: an explicit `block` or `steer`
    rule enforces from the first apply at every stage, including `observe`.
    Disable individual rules with `enabled = false` if you want them shipped
    but inert.

      observe   — report unapproved MCP configs, rewrite nothing, allow
                  destinations no rule matched.
      steer     — same enforcement, but approved MCP client configs are
                  rewritten to the tenant gateway.
      allowlist — quarantine unapproved MCP configs, block unmatched
                  destinations, close the SNI side-channels. The destination
                  rules become exhaustive; verify they cover every sanctioned
                  provider before selecting this.
  EOT
  default     = "observe"

  validation {
    condition     = contains(["observe", "steer", "allowlist"], var.enforcement_stage)
    error_message = "enforcement_stage must be one of: observe, steer, allowlist."
  }
}

variable "device_groups" {
  type = map(object({
    description = optional(string)
    # Where the group came from: manual, scim, mdm, … Immutable — changing it
    # replaces the group, which re-scopes every rule targeting it.
    source = optional(string, "manual")
    # Identifier in the upstream system (SCIM group id, Jamf smart-group id).
    # Immutable for the same reason.
    external_id = optional(string)
  }))
  description = "Device groups to create, keyed by group name."

  # The destination rules below reference these two keys by name, so a config
  # that drops one would fail deep in a `local.group_ids[...]` lookup. Catch it
  # here instead, where the message says what to do about it.
  validation {
    condition     = length(setsubtract(["engineering", "contractors"], keys(var.device_groups))) == 0
    error_message = "device_groups must define both \"engineering\" and \"contractors\" — the destination rules target them by name. Rename in main.tf if your populations differ."
  }

  default = {
    engineering = {
      description = "Engineering laptops enrolled via MDM"
      source      = "mdm"
      external_id = "jamf-smart-group-42"
    }
    contractors = {
      description = "Third-party contractors on BYOD hardware"
      source      = "manual"
    }
  }
}

variable "strict_groups" {
  type        = set(string)
  description = "Groups that get the strict posture override regardless of enforcement_stage. Must be keys of device_groups."
  default     = ["contractors"]

  validation {
    condition     = length(setsubtract(var.strict_groups, keys(var.device_groups))) == 0
    error_message = "every strict_groups entry must be a key of device_groups — an override can only target a group this config creates."
  }
}

variable "sanctioned_providers" {
  type = map(object({
    priority = number
  }))
  description = "AI-catalog slugs allowed fleet-wide, keyed by slug. Priority sets the rung on the evaluation ladder: keep it below the carve-out (5) and the contractor block (10), and above the catch-all (100)."

  default = {
    anthropic = { priority = 20 }
  }
}

variable "mcp_gateway_url" {
  type        = string
  description = "Tenant MCP gateway that approved on-device MCP client configs are rewritten to: https://<tenant-short-name>.mcp.ferentin.net/v1 on the public edge. Must be https://."
  default     = "https://acme.mcp.ferentin.net/v1"

  validation {
    condition     = startswith(var.mcp_gateway_url, "https://")
    error_message = "mcp_gateway_url must be https:// — http:// downgrades every governed MCP session to cleartext."
  }
}

variable "blocked_hosts" {
  type        = list(string)
  description = "Explicit host / suffix deny list for shadow-AI endpoints with no catalog entry."
  default     = ["api.unsanctioned-ai.example", ".shadow-llm.example"]
}

variable "hook_rule_enforcement" {
  type        = string
  description = <<-EOT
    Call-time enforcement of MCP `block` rules (tool calls and tool
    behaviours), kept separate from enforcement_stage on purpose:

      shadow  — count what a `block` rule would deny, deny nothing.
      enforce — deny the tool call.

    The platform refuses `enforce` (409) until every active device in a scope
    runs an agent build that enforces call-time rules. Check
    hookRuleEnforcementCoverage on the posture fleet status in the console
    before switching.
  EOT
  default     = "shadow"

  validation {
    condition     = contains(["shadow", "enforce"], var.hook_rule_enforcement)
    error_message = "hook_rule_enforcement must be shadow or enforce."
  }
}

variable "blocked_mcp_hosts" {
  type        = list(string)
  description = "Remote MCP server hostnames to block on every device. Bare hostnames: no scheme, port or path."
  default     = ["mcp.unvetted.example"]
}

variable "blocked_local_mcp_packages" {
  type        = list(string)
  description = "Local (stdio) MCP server packages blocked on every device, as exact registry-qualified ids such as npm:@scope/name. To let an internal local server run governed, approve it in the admin console rather than allowing it here."
  default     = ["npm:@example/unvetted-mcp"]
}

variable "approved_agents" {
  type        = set(string)
  description = "AI agents approved to run on every device, as detection-catalog slugs (claude-code, cursor, codex-cli, …). Everything else is reported at observe and blocked at allowlist and on the strict groups."
  default     = ["claude-code", "cursor"]
}
