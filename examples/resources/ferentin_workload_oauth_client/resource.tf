# Outbound workload OAuth client — the credential the platform uses when an
# MCP server's upstream_auth_strategy = cc_federated. The platform mints a
# token at the customer's IdP and forwards it upstream.

# client_secret_basic example (Okta).
resource "ferentin_workload_oauth_client" "salesforce_cc" {
  name                    = "salesforce-prod-cc"
  description             = "Outbound creds for Salesforce MCP upstream"
  idp_type                = "okta"
  auth_method             = "client_secret_basic"
  audience_param_strategy = "audience_param"

  oauth_client_id = "0oa1abcd2efgh3ijkl"
  issuer          = "https://acme.okta.com"
  jwks_uri        = "https://acme.okta.com/.well-known/jwks.json"
  token_endpoint  = "https://acme.okta.com/oauth2/v1/token"

  # WriteOnly — value flows to the platform during apply, never enters state.
  # Bump client_secret_wo_version to rotate.
  client_secret            = var.salesforce_client_secret
  client_secret_wo_version = 1

  default_audience = "https://api.salesforce.com"
  default_scopes   = "api refresh_token"
}

# private_key_jwt example (Auth0).
resource "ferentin_workload_oauth_client" "snowflake_cc" {
  name                    = "snowflake-prod-cc"
  idp_type                = "auth0"
  auth_method             = "private_key_jwt"
  audience_param_strategy = "audience_param"

  oauth_client_id = "snowflake-mcp-client"
  issuer          = "https://acme.auth0.com"
  jwks_uri        = "https://acme.auth0.com/.well-known/jwks.json"
  token_endpoint  = "https://acme.auth0.com/oauth/token"

  private_key_jwt_alg                    = "RS256"
  private_key_jwt_kid                    = "kid-2026-q1"
  private_key_jwt_jwks_url               = "https://example.com/.well-known/jwks.json"
  private_key_jwt_private_key            = var.snowflake_private_key_pem
  private_key_jwt_private_key_wo_version = 1

  default_audience = "https://acme.snowflakecomputing.com"
}

variable "salesforce_client_secret" {
  type      = string
  sensitive = true
}

variable "snowflake_private_key_pem" {
  type        = string
  description = "PKCS#8 PEM private key for private_key_jwt auth"
  sensitive   = true
}
