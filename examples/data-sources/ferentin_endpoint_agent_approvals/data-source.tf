# Every AI-agent decision in the tenant, sorted by catalog slug.
data "ferentin_endpoint_agent_approvals" "all" {}

output "blocked_agents" {
  value = [for a in data.ferentin_endpoint_agent_approvals.all.approvals : a.catalog_slug if a.action == "block"]
}
