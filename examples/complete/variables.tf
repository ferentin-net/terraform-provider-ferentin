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

variable "service_edge_url" {
  type        = string
  description = "service-edge base URL that managed devices are steered to. Must be https://."
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
