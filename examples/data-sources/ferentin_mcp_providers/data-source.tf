# Plural — lists every provider in the global catalog.
data "ferentin_mcp_providers" "all" {}

output "provider_slugs" {
  value = [for p in data.ferentin_mcp_providers.all.providers : p.slug]
}
