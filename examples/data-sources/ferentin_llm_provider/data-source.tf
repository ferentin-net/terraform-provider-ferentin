# Look up a provider in the global LLM provider catalog by slug. Distinct from
# the ferentin_llm_provider RESOURCE, which binds credentials for one provider
# in your tenant.
data "ferentin_llm_provider" "anthropic" {
  slug = "anthropic"
}

output "anthropic_catalog_name" {
  value = data.ferentin_llm_provider.anthropic.name
}
