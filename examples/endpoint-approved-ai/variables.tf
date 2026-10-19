variable "profile" {
  type        = string
  description = "Shared-config profile holding the tokens `ferentin login --profile <name>` stashed in the OS keyring."
  default     = "prod"
}

variable "chatgpt_team_ids" {
  type        = list(string)
  description = <<-EOT
    Apple Developer Team IDs whose apps may reach OpenAI. The default is the
    team that signs the ChatGPT desktop app. Check it on a Mac with:

      codesign -dv /Applications/ChatGPT.app 2>&1 | grep TeamIdentifier

    An empty list would make the priority-20 allow apply to ANY app, which
    turns the block below it into dead code — refused here for that reason.
  EOT
  default     = ["2DC432GLL2"]

  validation {
    condition     = length(var.chatgpt_team_ids) > 0
    error_message = "chatgpt_team_ids must name at least one team: an empty list allows OpenAI from every app."
  }
}

variable "blocked_hosts" {
  type        = list(string)
  description = "Hosts of unapproved AI services, blocked on every device. A bare host matches only itself; a leading dot (\".example.com\") matches the domain and every host under it."
  default     = [".deepseek.com"]
}

variable "default_destination_action" {
  type        = string
  description = <<-EOT
    What happens to AI traffic no rule matches.

      allow — the rules are a deny list with a few pinned exceptions (default).
      block — the rules become an allowlist: anything they do not allow is
              blocked on the device, including things you did not expect.
              Try it on one machine before the fleet.
  EOT
  default     = "allow"

  validation {
    condition     = contains(["allow", "block"], var.default_destination_action)
    error_message = "default_destination_action must be allow or block."
  }
}

variable "unnamed_flow_block_enabled" {
  type        = bool
  description = "Drop port-443 flows to public IPs that have no hostname (raw IPs, external DoH, ECH-degraded traffic). Needs an agent that implements it; an older agent refuses the posture and keeps its last-known-good policy."
  default     = false
}

variable "unnamed_flow_exempt_cidrs" {
  type        = set(string)
  description = "Addresses or CIDRs exempt from the unnamed-flow block, for infrastructure reached by bare IP on 443 (VPN concentrators, monitoring). At most 64; a /0 is refused."
  default     = []
}

variable "mcp_gateway_url" {
  type        = string
  description = "Tenant MCP gateway that approved on-device MCP client configs are rewritten to: https://<tenant-short-name>.mcp.ferentin.net/v1 on the public edge. Null clears it. Must be https://."
  default     = null

  validation {
    condition     = var.mcp_gateway_url == null || startswith(coalesce(var.mcp_gateway_url, "https://"), "https://")
    error_message = "mcp_gateway_url must be https:// — http:// downgrades every governed MCP session to cleartext."
  }
}
