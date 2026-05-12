variable "endpoint" {
  type        = string
  description = "Ferentin admin-api endpoint, e.g. https://api.ferentin.net"
}

variable "tenant_id" {
  type        = string
  description = "Target tenant UUID"
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
