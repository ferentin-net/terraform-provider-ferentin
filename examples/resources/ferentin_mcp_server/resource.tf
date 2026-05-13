# MCP server — a tenant binding of an MCP provider (from the catalog or
# a tenant-custom ferentin_mcp_provider) to a specific endpoint and
# routing/auth config. Multiple servers per provider are allowed.

resource "ferentin_mcp_server" "internal_search_us" {
  # provider_id references the MCP provider being instantiated. Pull from
  # ferentin_mcp_provider.<your_provider>.id or the global catalog.
  provider_id = "ad8a8c1f-1234-4567-89ab-cdef01234567"

  name        = "internal-search-us"
  endpoint    = "https://search.internal.example.com/mcp/sse"
  description = "US-region internal document search MCP server"

  enabled  = true
  priority = 100

  # Restrict to a specific scope subset of the provider's catalog.
  enabled_scopes = [
    "search:read",
    "search:cite",
  ]

  # Routing.
  deployment_mode        = "edge_routed"
  upstream_auth_strategy = "oauth2_shared"
  transport_type         = "sse"
  edge_site_id           = "prod-us-east-1a"

  tags = {
    env  = "prod"
    team = "platform"
  }
}

output "internal_search_url" {
  description = "Client-facing URL Terraform-managed agents will connect to."
  value       = ferentin_mcp_server.internal_search_us.client_facing_url
}

# --- Client Credentials Federation (cc_federated) -----------------------
# Pair an MCP server with a `ferentin_workload_oauth_client` so the platform
# mints upstream tokens at the customer's IdP and forwards them to the
# upstream MCP. The `default_*` on the workload OAuth client provide the
# defaults; the `cc_federated_*_override` fields on the MCP server narrow
# them per-server.

resource "ferentin_workload_oauth_client" "salesforce_cc" {
  name                    = "salesforce-prod-cc"
  idp_type                = "okta"
  auth_method             = "client_secret_basic"
  audience_param_strategy = "audience_param"

  oauth_client_id = "0oa1abcd2efgh3ijkl"
  issuer          = "https://acme.okta.com"
  jwks_uri        = "https://acme.okta.com/.well-known/jwks.json"
  token_endpoint  = "https://acme.okta.com/oauth2/v1/token"

  client_secret            = var.salesforce_client_secret # WriteOnly
  client_secret_wo_version = 1

  # Tenant-wide defaults. Each MCP server bound to this client may narrow
  # via the cc_federated_*_override fields below.
  default_audience = "https://api.salesforce.com"
  default_scopes   = "api refresh_token"
}

resource "ferentin_mcp_server" "salesforce_read_only" {
  provider_id = "11111111-1111-1111-1111-111111111111" # ferentin_mcp_provider.salesforce.id

  name        = "salesforce-read-only-us"
  endpoint    = "https://mcp.salesforce.example.com/sse"
  description = "Read-only Salesforce MCP — narrower scopes than the default."

  transport_type         = "streamable_http"
  deployment_mode        = "public"
  upstream_auth_strategy = "cc_federated"

  # The federation knob: which workload client mints the upstream token.
  cc_federated_workload_client_id = ferentin_workload_oauth_client.salesforce_cc.client_id_resource

  # Narrow the client's defaults for this specific server. Omit any of these
  # to inherit the workload client's default_*.
  cc_federated_scopes_override   = "api" # drop refresh_token for read-only
  cc_federated_audience_override = "https://api.salesforce.com"
}

variable "salesforce_client_secret" {
  type        = string
  description = "OAuth client_secret for the Salesforce IdP. WriteOnly."
  sensitive   = true
}

# Optional: gate the MCP server on the workload-client probe so the plan
# fails fast when the IdP binding is broken.
data "ferentin_workload_oauth_client_test" "salesforce_check" {
  client_id = ferentin_workload_oauth_client.salesforce_cc.client_id_resource
}

output "salesforce_idp_reachable" {
  value = data.ferentin_workload_oauth_client_test.salesforce_check.overall_pass
}
