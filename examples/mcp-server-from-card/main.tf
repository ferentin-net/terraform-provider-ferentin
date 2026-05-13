# Configure an MCP server from a discovered server-card.json.
#
# The MCP spec (https://modelcontextprotocol.io) defines a standard
# server-card.json that exposes a server's identity, transport, capabilities,
# and credential requirements. `ferentin mcp discover <url>` writes one for
# any reachable MCP server.
#
# This example reads that file as the single source of truth and stamps out
# the matching `ferentin_mcp_provider` (tenant-custom catalog entry) +
# `ferentin_mcp_server` (binding to a specific endpoint). Bumping the card
# (new version, new auth shape, new tools) propagates on next apply.

terraform {
  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

provider "ferentin" {
  # endpoint + tenant_id auto-default — see the README for auth options.
  profile = "prod"
}

locals {
  # Source of truth. Replace path with the location of the discovered card.
  card = jsondecode(file("${path.module}/server-card.json"))

  # The card's name is a slash-separated namespace like
  # "net.ferentin.mcp/threat-intel" — use the last segment as the slug.
  slug = element(split("/", local.card.name), length(split("/", local.card.name)) - 1)

  # Two different enums between the catalog-level transport hint
  # (`ferentin_mcp_provider.transport`: `stdio`/`sse`/`http`) and the
  # per-instance selection (`ferentin_mcp_server.transport_type`:
  # `stdio_tunnel`/`sse`/`streamable_http`). The MCP spec's
  # kebab-case form needs to map to both.
  provider_transport_lookup = {
    "stdio"           = "stdio"
    "sse"             = "sse"
    "streamable-http" = "http"
  }
  server_transport_lookup = {
    "stdio"           = "stdio_tunnel"
    "sse"             = "sse"
    "streamable-http" = "streamable_http"
  }
  card_transport     = local.card.remotes[0].type
  provider_transport = local.provider_transport_lookup[local.card_transport]
  server_transport   = local.server_transport_lookup[local.card_transport]

  # The card's _meta.net.ferentin.transport.auth_type tells the platform
  # how to authenticate upstream. Map each MCP-discovery value to the
  # matching Ferentin upstream_auth_strategy:
  #
  #   none                        → none           (public server)
  #   bearer                      → static_bearer  (static token in Authorization header)
  #   oauth2_client_credentials   → cc_federated   (platform mints CC tokens at the customer's IdP)
  #   oauth2_authorization_code   → oauth2_user    (per-user OAuth flow, typically with DCR)
  #
  # For `cc_federated`, the platform also needs a `ferentin_workload_oauth_client`
  # holding the IdP credentials — see the conditional block below.
  auth_strategy_lookup = {
    "none"                      = "none"
    "bearer"                    = "static_bearer"
    "oauth2_client_credentials" = "cc_federated"
    "oauth2_authorization_code" = "oauth2_user"
  }
  card_auth_type         = local.card._meta["net.ferentin"].transport.auth_type
  upstream_auth_strategy = local.auth_strategy_lookup[local.card_auth_type]
}

# Publish the server's identity into the tenant catalog.
resource "ferentin_mcp_provider" "from_card" {
  display_name = local.card.title
  slug         = local.slug
  description  = local.card.description
  default_url  = local.card.remotes[0].url
  transport    = local.provider_transport
  category     = local.card._meta["net.ferentin"].curation.category
}

# Conditionally provision a workload OAuth client when the card calls for
# client-credentials federation. The card identifies the *protocol*
# (oauth2_client_credentials); the actual IdP coordinates (issuer, client_id,
# client_secret, …) are tenant-side concerns the operator supplies via vars.
# count=0 when the card doesn't need cc_federated; the resource simply
# doesn't get created.
resource "ferentin_workload_oauth_client" "from_card" {
  count = local.upstream_auth_strategy == "cc_federated" ? 1 : 0

  name                    = "${local.slug}-cc"
  description             = "Workload OAuth client for ${local.card.title} (cc_federated)"
  idp_type                = var.idp_type
  auth_method             = "client_secret_basic"
  audience_param_strategy = "audience_param"

  oauth_client_id = var.workload_oauth_client_id
  issuer          = var.workload_oauth_issuer
  jwks_uri        = var.workload_oauth_jwks_uri
  token_endpoint  = var.workload_oauth_token_endpoint

  client_secret            = var.workload_oauth_client_secret # WriteOnly
  client_secret_wo_version = 1
}

# Bind a specific server instance to the catalog provider. For an air-gapped
# threat-intel server like the reference example, this is the only instance.
# For SaaS providers (Salesforce, GitHub), you'd typically have one server
# per region / environment.
resource "ferentin_mcp_server" "from_card" {
  provider_id = ferentin_mcp_provider.from_card.provider_id

  name        = local.slug
  endpoint    = local.card.remotes[0].url
  description = local.card.description

  transport_type         = local.server_transport
  upstream_auth_strategy = local.upstream_auth_strategy

  # Only set when the card asked for cc_federated. Use an explicit
  # length() check rather than try() so genuine evaluation errors aren't
  # swallowed alongside the "index 0 doesn't exist when count=0" case.
  cc_federated_workload_client_id = length(ferentin_workload_oauth_client.from_card) > 0 ? ferentin_workload_oauth_client.from_card[0].client_id_resource : null

  # Most local-dev MCP servers route through public mode; production
  # deployments behind a private VPC route through edge_routed with an
  # edge_site_id. See examples/resources/ferentin_mcp_server/resource.tf
  # for the edge-routed shape.
  deployment_mode = "public"
}

output "mcp_url" {
  description = "Client-facing URL Terraform-managed agents will connect to."
  value       = ferentin_mcp_server.from_card.client_facing_url
}

output "card_tools" {
  description = "Tool names the server exposes (informational, not pushed to the platform here)."
  value       = [for t in local.card._meta["net.ferentin"].capabilities.tools : t.name]
}

# --- Variables for the cc_federated path --------------------------------
# Only used when the card's auth_type = "oauth2_client_credentials". Leave
# at the defaults (empty strings) for bearer / none / oauth2_user cards —
# the workload_oauth_client resource's count=0 will skip creation.

variable "idp_type" {
  type        = string
  description = "Identity-provider type. One of: auth0, entra, generic_oidc, okta."
  default     = "generic_oidc"
}

variable "workload_oauth_issuer" {
  type        = string
  description = "OIDC issuer URL of the IdP that mints upstream tokens."
  default     = ""
}

variable "workload_oauth_jwks_uri" {
  type    = string
  default = ""
}

variable "workload_oauth_token_endpoint" {
  type    = string
  default = ""
}

variable "workload_oauth_client_id" {
  type        = string
  description = "OAuth client_id at the IdP. NOT a Ferentin UUID."
  default     = ""
}

variable "workload_oauth_client_secret" {
  type        = string
  description = "OAuth client_secret at the IdP. WriteOnly — never enters state."
  sensitive   = true
  default     = ""
}
