# Stand up two MCP servers under one tenant and bind them under a single
# allow-all mcp_policy:
#
#   1. Salesforce — instantiated from the global MCP catalog by slug.
#   2. Threat Intel — published from a discovered server-card.json into
#      the tenant's private catalog.
#
# The policy grants access to every tool both servers expose. Narrow it in
# production by setting `allowed_tools` / `grant_toolsets` on the effect.

terraform {
  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

provider "ferentin" {
  # endpoint + tenant_id auto-default. See the README for auth options.
  profile = "prod"
}

# --- 1. Salesforce from the global catalog -------------------------------
# The global catalog is read-only and identified by UUID, but operators
# don't memorize UUIDs — they remember slugs. List the catalog and key it
# by slug for typo-safe lookup downstream.

data "ferentin_mcp_providers" "catalog" {}

locals {
  catalog_by_slug = {
    for p in data.ferentin_mcp_providers.catalog.providers : p.slug => p
  }
  salesforce_catalog = local.catalog_by_slug["salesforce"]
}

resource "ferentin_mcp_server" "salesforce_prod" {
  provider_id = local.salesforce_catalog.provider_id

  name        = "salesforce-prod-us"
  endpoint    = "https://mcp.salesforce.example.com/sse"
  description = "Production Salesforce MCP — US region."

  transport_type         = "sse"
  deployment_mode        = "public"
  upstream_auth_strategy = "static_bearer"

  enabled  = true
  priority = 100

  # Production Salesforce typically uses cc_federated against the customer's
  # IdP — see examples/resources/ferentin_mcp_server/resource.tf for the
  # workload_oauth_client wiring. static_bearer keeps this example focused.
}

# --- 2. Threat Intel from a discovered server-card.json ------------------
# server-card.json is the MCP-spec canonical description of a server.
# Drive the catalog entry + binding off it so a refreshed card propagates
# on the next apply.

locals {
  card = jsondecode(file("${path.module}/server-card.json"))

  # The card's name is a slash-separated namespace like
  # "net.ferentin.mcp/threat-intel" — use the last segment as the slug.
  card_slug = element(split("/", local.card.name), length(split("/", local.card.name)) - 1)

  # Two enums between the catalog-level transport hint and the per-instance
  # selection (see examples/mcp-server-from-card/main.tf for the longer
  # version with auth-type mapping too).
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
}

resource "ferentin_mcp_provider" "threat_intel" {
  display_name = local.card.title
  slug         = local.card_slug
  description  = local.card.description
  default_url  = local.card.remotes[0].url
  transport    = local.provider_transport
  category     = local.card._meta["net.ferentin"].curation.category
}

resource "ferentin_mcp_server" "threat_intel" {
  provider_id = ferentin_mcp_provider.threat_intel.provider_id

  name        = local.card_slug
  endpoint    = local.card.remotes[0].url
  description = local.card.description

  transport_type         = local.server_transport
  upstream_auth_strategy = "static_bearer" # card._meta.net.ferentin.transport.auth_type == "bearer"
  deployment_mode        = "public"

  enabled  = true
  priority = 100
}

# --- 3. One policy that allows every tool on both servers ----------------
# An `allow` effect with no `allowed_tools` / `grant_toolsets` grants access
# to every tool the upstream MCP exposes. To narrow this in production, set
# either of those lists on the effect (or layer a higher-priority `deny`).
#
# `provider_instances` references the *names* of the bound servers, not
# their UUIDs — that's the contract on this resource.

resource "ferentin_mcp_policy" "mcp_full_access" {
  name        = "mcp-full-access"
  description = "Grants access to all tools on the Salesforce and Threat Intel MCP servers."
  priority    = 100
  enabled     = true

  provider_instances = [
    ferentin_mcp_server.salesforce_prod.name,
    ferentin_mcp_server.threat_intel.name,
  ]

  effect = {
    type    = "allow"
    message = "Full tool access granted by mcp-full-access."
  }
}

# --- Outputs -------------------------------------------------------------

output "salesforce_url" {
  description = "Client-facing URL agents will connect to for Salesforce."
  value       = ferentin_mcp_server.salesforce_prod.client_facing_url
}

output "threat_intel_url" {
  description = "Client-facing URL agents will connect to for Threat Intel."
  value       = ferentin_mcp_server.threat_intel.client_facing_url
}

output "policy_id" {
  description = "Server-generated UUID for the mcp-full-access policy."
  value       = ferentin_mcp_policy.mcp_full_access.policy_id
}

output "threat_intel_tools" {
  description = "Tool names the Threat Intel card advertises (informational)."
  value       = [for t in local.card._meta["net.ferentin"].capabilities.tools : t.name]
}
