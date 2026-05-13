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
