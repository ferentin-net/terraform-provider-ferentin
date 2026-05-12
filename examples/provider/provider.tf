terraform {
  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

# Production / CI configuration with OAuth2 client_credentials.
# Tokens mint on first request and refresh ~60s before expiry — the
# recommended path for long-running applies and scheduled jobs.
provider "ferentin" {
  endpoint  = "https://api.ferentin.net"
  tenant_id = var.tenant_id

  client_id     = var.ferentin_client_id
  client_secret = var.ferentin_client_secret
  # auth_url defaults to "https://auth.ferentin.net" when endpoint is
  # "https://api.ferentin.net" (api. → auth. substitution). Override here
  # if your tenant uses a per-tenant subdomain.
  # auth_url = "https://acme-sso.auth.ferentin.net"
}

variable "tenant_id" {
  type        = string
  description = "Target tenant UUID"
}

variable "ferentin_client_id" {
  type        = string
  description = "OAuth2 service-account client_id"
}

variable "ferentin_client_secret" {
  type        = string
  description = "OAuth2 service-account client_secret"
  sensitive   = true
}

# Alternative: pre-minted bearer token (tests / one-off applies). Mutually
# exclusive with the client_credentials block above. Tokens typically live
# ~15 minutes; if your apply is longer, prefer client_credentials.
#
# provider "ferentin" {
#   endpoint  = "https://api.ferentin.net"
#   tenant_id = var.tenant_id
#   token     = var.ferentin_token
# }
#
# variable "ferentin_token" {
#   type      = string
#   sensitive = true
# }
