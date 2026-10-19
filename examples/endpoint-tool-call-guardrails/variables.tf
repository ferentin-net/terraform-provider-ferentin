variable "profile" {
  type        = string
  description = "Shared-config profile holding the tokens `ferentin login --profile <name>` stashed in the OS keyring."
  default     = "prod"
}

variable "pilot_group_name" {
  type        = string
  description = "Name of the device group that moves to enforce first. Add devices to it in the admin console, or from your MDM."
  default     = "ai-guardrails-pilot"
}

variable "blocked_commands" {
  type        = list(string)
  description = "Command names AI agents may not run on any device. Matched however a command line spells them."
  default     = ["curl", "wget", "nc", "scp"]
}

variable "tenant_hook_rule_enforcement" {
  type        = string
  description = "What a matching tool call rule does on devices outside the pilot group: shadow (count, the call runs) or enforce (deny)."
  default     = "shadow"

  validation {
    condition     = contains(["shadow", "enforce"], var.tenant_hook_rule_enforcement)
    error_message = "tenant_hook_rule_enforcement must be shadow or enforce."
  }
}

variable "pilot_hook_rule_enforcement" {
  type        = string
  description = <<-EOT
    What a matching tool call rule does in the pilot group: shadow or enforce.

    The platform refuses `enforce` (409) until every active device in the
    group runs an agent build that can deny. Check the posture's fleet status
    in the admin console first.
  EOT
  default     = "shadow"

  validation {
    condition     = contains(["shadow", "enforce"], var.pilot_hook_rule_enforcement)
    error_message = "pilot_hook_rule_enforcement must be shadow or enforce."
  }
}

variable "unapproved_mcp_action" {
  type        = string
  description = "Posture both scopes share: what the agent does with an unapproved MCP server config (report_only, quarantine or block)."
  default     = "report_only"

  validation {
    condition     = contains(["report_only", "quarantine", "block"], var.unapproved_mcp_action)
    error_message = "unapproved_mcp_action must be report_only, quarantine or block."
  }
}

variable "default_destination_action" {
  type        = string
  description = "Posture both scopes share: what happens to AI traffic no destination rule matches (allow or block)."
  default     = "allow"

  validation {
    condition     = contains(["allow", "block"], var.default_destination_action)
    error_message = "default_destination_action must be allow or block."
  }
}

variable "mcp_gateway_url" {
  type        = string
  description = "Posture both scopes share: the tenant's MCP gateway on the public edge, https://<tenant-short-name>.mcp.ferentin.net/v1. Null clears it, and the agent stops rewriting MCP client configs."
  default     = "https://acme.mcp.ferentin.net/v1"

  validation {
    condition     = var.mcp_gateway_url == null || startswith(coalesce(var.mcp_gateway_url, "https://"), "https://")
    error_message = "mcp_gateway_url must be https:// — http:// downgrades every governed MCP session to cleartext."
  }
}
