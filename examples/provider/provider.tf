terraform {
  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

# Provider configuration. All attributes are optional; each falls back to
# the corresponding FERENTIN_* environment variable. Production deployments
# should use the upcoming client-credentials refresh path instead of a
# static token — see DEVELOPMENT.md and §4 of the design doc.
provider "ferentin" {
  endpoint  = "https://api.ferentin.net"
  tenant_id = var.tenant_id

  # Pre-minted bearer for tests / CI / dev. Sensitive; never appears in
  # plan/apply output.
  token = var.ferentin_token
}

variable "tenant_id" {
  type        = string
  description = "Target tenant UUID"
}

variable "ferentin_token" {
  type        = string
  description = "Admin-api bearer access token"
  sensitive   = true
}
