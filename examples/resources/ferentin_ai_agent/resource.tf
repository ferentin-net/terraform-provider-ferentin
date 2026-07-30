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

# A machine-to-machine agent — a pipeline or service that authenticates as
# itself, with no user in the loop.
#
# `grant_types` is what distinguishes the two: the platform GENERATES
# `ai_client_type` from it and from nothing else — `client_credentials` present
# -> "agent", otherwise -> "assistant". The platform default omits it, so a
# SERVICE agent without this line is stored as an assistant and cannot mint a
# token on its own. The NATIVE client above is correctly an assistant: it acts
# for a signed-in user via the authorization-code flow.
resource "ferentin_ai_agent" "release_bot" {
  name             = "release-bot"
  agent_platform   = "claude"
  application_type = "SERVICE"

  scopes      = ["llm", "mcp"]
  grant_types = ["client_credentials"]

  token_endpoint_auth_method = "client_secret_basic"
  active                     = true
}

# The retrieved client_id is what the agent presents at runtime.
output "claude_desktop_client_id" {
  value = ferentin_ai_agent.claude_desktop.client_id
}

output "release_bot_client_secret" {
  description = "Only returned on create — capture it on first apply."
  value       = ferentin_ai_agent.release_bot.client_secret
  sensitive   = true
}
