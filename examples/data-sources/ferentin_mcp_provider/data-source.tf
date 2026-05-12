data "ferentin_mcp_provider" "salesforce" {
  provider_id = "fbe4-..."
}

output "salesforce_slug" {
  value = data.ferentin_mcp_provider.salesforce.slug
}
