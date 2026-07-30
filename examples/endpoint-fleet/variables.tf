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

variable "steered_providers" {
  type = map(object({
    priority = number
  }))
  description = "AI-catalog slugs steered through service-edge, keyed by slug. Priority sets the rung on the evaluation ladder."

  default = {
    anthropic = { priority = 20 }
    openai    = { priority = 30 }
  }
}

variable "service_edge_url" {
  type        = string
  description = "service-edge base URL steered traffic is rewritten to. Must be https://."
  default     = "https://edge.example.com/v1"

  validation {
    condition     = startswith(var.service_edge_url, "https://")
    error_message = "service_edge_url must be https:// — http:// downgrades every steered flow on the fleet to cleartext."
  }
}

variable "mcp_gateway_url" {
  type        = string
  description = "Tenant MCP gateway base URL that approved on-device MCP client configs are rewritten to. Must be https://."
  default     = "https://mcp.example.com"

  validation {
    condition     = startswith(var.mcp_gateway_url, "https://")
    error_message = "mcp_gateway_url must be https:// — http:// downgrades every governed MCP session to cleartext."
  }
}

variable "blocked_hosts" {
  type        = list(string)
  description = "Explicit host / suffix deny list for shadow-AI endpoints with no catalog entry."
  default     = ["api.unsanctioned-ai.example", "*.shadow-llm.example"]
}
