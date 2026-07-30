# MCP policy with allow effect — engineering-only access to internal search.
resource "ferentin_mcp_policy" "engineering_search" {
  name        = "engineering-internal-search"
  description = "Engineering users can query internal search"
  priority    = 50
  enabled     = true

  provider_instances = ["internal-search-us"]

  effect = {
    type                  = "allow"
    grant_toolsets        = ["search:read", "search:cite"]
    rate_limit_per_minute = 30
    message               = "Internal search granted via engineering policy."
  }
}

# Default-deny that catches everything else (higher priority number = lower precedence).
resource "ferentin_mcp_policy" "default_deny" {
  name     = "default-deny-internal-search"
  priority = 1000
  enabled  = true

  provider_instances = ["internal-search-us"]

  effect = {
    type    = "deny"
    message = "Access to internal search requires engineering role."
  }
}
