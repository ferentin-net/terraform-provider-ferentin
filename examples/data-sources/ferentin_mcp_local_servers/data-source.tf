# Every local MCP server in the tenant: ones an administrator declared or added
# from the catalog, and ones devices reported.
data "ferentin_mcp_local_servers" "all" {}

# Servers devices are running that nobody has decided on yet.
output "pending_review" {
  value = [for s in data.ferentin_mcp_local_servers.all.servers : s.display_name if s.status == "pending"]
}
