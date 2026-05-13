terraform {
  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

# `endpoint` defaults to https://api.ferentin.net — only set it for local-dev,
# staging, or air-gapped deployments.
# `tenant_id` auto-resolves from the access token's `tid` claim — only set it
# to override (e.g. multi-tenant orchestration from a single config).

# --- Auth option 1: shared profile (recommended for interactive users) ---
# Reads tokens that `ferentin login --profile prod` stashed in the OS
# keyring (or ~/.ferentin/profile:<name> fallback). The provider refreshes
# them transparently as they expire — same UX as `aws` shared credentials.
provider "ferentin" {
  profile = "prod"
}

# --- Auth option 2: OAuth2 client_credentials (recommended for CI / service accounts) ---
# Tokens mint on first request and refresh ~60s before expiry.
#
# provider "ferentin" {
#   client_id     = var.ferentin_client_id
#   client_secret = var.ferentin_client_secret
# }

# --- Auth option 3: pre-minted bearer token (tests / one-off applies) ---
# Tokens typically live ~15 minutes; longer applies should prefer the
# profile or client_credentials block above.
#
# provider "ferentin" {
#   token = var.ferentin_token
# }

variable "ferentin_client_id" {
  type        = string
  description = "OAuth2 service-account client_id (only used with the CC auth option)"
  default     = ""
}

variable "ferentin_client_secret" {
  type        = string
  description = "OAuth2 service-account client_secret (only used with the CC auth option)"
  sensitive   = true
  default     = ""
}
