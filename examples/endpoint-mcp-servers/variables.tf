variable "profile" {
  type        = string
  description = "Shared-config profile holding the tokens `ferentin login --profile <name>` stashed in the OS keyring."
  default     = "prod"
}

variable "mcp_gateway_url" {
  type        = string
  description = "The tenant's MCP gateway on the public edge: https://<tenant-short-name>.mcp.ferentin.net/v1, with no trailing slash. Approved MCP client configs are rewritten to it, and steer rules append /mcp/<server-slug>."
  default     = "https://acme.mcp.ferentin.net/v1"

  validation {
    condition     = startswith(var.mcp_gateway_url, "https://") && !endswith(var.mcp_gateway_url, "/")
    error_message = "mcp_gateway_url must be https:// (http:// downgrades every governed MCP session to cleartext) and have no trailing slash."
  }
}

variable "steered_remote_servers" {
  type = map(object({
    # Hostnames developers' MCP clients connect to for this server.
    hosts = list(string)
    # The slug of the enabled MCP server in this tenant the hosts are steered
    # to (ferentin_mcp_server.<name>.slug, or the registry in the console).
    server_slug = string
    # Rung on the ladder, between the remote block (10) and the local block (30).
    priority = number
  }))
  description = "Sanctioned remote MCP servers, keyed by a short name."

  default = {
    github = {
      hosts       = ["api.githubcopilot.com"]
      server_slug = "github"
      priority    = 20
    }
  }

  validation {
    condition     = alltrue([for s in values(var.steered_remote_servers) : s.priority > 10 && s.priority < 30])
    error_message = "steered_remote_servers priorities must sit between the remote block (10) and the local block (30)."
  }
}

variable "blocked_remote_hosts" {
  type        = list(string)
  description = "Remote MCP server hostnames blocked on every device. Bare hostnames: no scheme, port or path."
  default     = ["mcp.unvetted.example"]
}

variable "blocked_local_packages" {
  type        = list(string)
  description = "Local (stdio) MCP server packages blocked on every device, as exact registry-qualified ids: npm:@scope/name, pypi:name or oci:…. No wildcards."
  default     = ["npm:@example/unvetted-mcp"]
}

variable "unapproved_mcp_action" {
  type        = string
  description = <<-EOT
    What the agent does with an MCP server config that no rule matches and
    that is not approved in the registry:

      report_only — report it, change nothing on the machine (default here)
      quarantine  — move the entry aside so the client cannot load it; the
                    developer can appeal
      block       — the server does not start
  EOT
  default     = "report_only"

  validation {
    condition     = contains(["report_only", "quarantine", "block"], var.unapproved_mcp_action)
    error_message = "unapproved_mcp_action must be report_only, quarantine or block."
  }
}

variable "local_mcp_risk_policy" {
  type = object({
    low    = string
    medium = string
    high   = string
  })
  description = "What developers get from an unvetted local MCP tool at each risk rating: allow, ask_once, ask_every_call or deny. An ask_* tier needs approval routing configured first."
  default = {
    low    = "allow"
    medium = "deny"
    high   = "deny"
  }
}

variable "default_destination_action" {
  type        = string
  description = "What happens to AI traffic no destination rule matches: allow or block. Declared because this config owns the tenant-default posture row, and the provider writes its default (allow) when it is left out."
  default     = "allow"

  validation {
    condition     = contains(["allow", "block"], var.default_destination_action)
    error_message = "default_destination_action must be allow or block."
  }
}

variable "approved_local_servers" {
  type = map(object({
    display_name = string
    # The command line the approval covers, as the client's config spells it.
    # `*` matches exactly one argument. Shipped verbatim to every device: put
    # `*` where anything private would go.
    argv_template = list(string)
  }))
  description = "Local (stdio) MCP servers approved tenant-wide, keyed by a short name."
  default = {
    okta = {
      display_name  = "Okta MCP"
      argv_template = ["uv", "run", "--directory", "*", "okta-mcp-server"]
    }
  }
}
