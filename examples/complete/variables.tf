variable "endpoint" {
  type        = string
  description = "Ferentin admin-api endpoint. Empty (the default) uses https://api.ferentin.net; set for local-dev or staging."
  default     = ""
}

variable "client_id" {
  type        = string
  description = "OAuth2 service-account client_id"
}

variable "client_secret" {
  type        = string
  description = "OAuth2 service-account client_secret"
  sensitive   = true
}

variable "anthropic_api_key" {
  type        = string
  description = "Anthropic API key for the prod instance"
  sensitive   = true
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
