# A Claude Desktop agent OIDC client. Scopes are constrained to the
# platform's AgentClientScopeAllowlist (ferentin-platform#648):
# llm / mcp / summarizer + OIDC standards.
resource "ferentin_ai_agent" "claude_desktop" {
  name             = "claude-desktop-prod"
  agent_platform   = "claude"
  application_type = "NATIVE"

  scopes = ["llm", "mcp", "summarizer"]

  token_endpoint_auth_method = "private_key_jwt"
  jwks_uri                   = "https://agents.example.com/.well-known/jwks.json"
  access_token_lifetime      = 900

  active = true
}

# The retrieved client_id is what the agent presents at runtime.
output "claude_desktop_client_id" {
  value = ferentin_ai_agent.claude_desktop.client_id
}
