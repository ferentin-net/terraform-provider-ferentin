data "ferentin_mcp_server_card" "internal_search" {
  slug = "internal-search"
}

# v1.1+ pattern: jsondecode the manifest and for_each over its tools.
locals {
  manifest_tools = try(jsondecode(data.ferentin_mcp_server_card.internal_search.card_content_json).tools, [])
}

output "tool_names" {
  value = [for t in local.manifest_tools : t.name]
}
