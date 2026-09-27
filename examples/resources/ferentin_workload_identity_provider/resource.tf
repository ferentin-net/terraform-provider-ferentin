# Inbound workload identity trust — accepts cloud-issued JWTs (AWS / GCP /
# Azure / OCI / GitHub / Cursor) so workloads in those environments authenticate
# without a pre-provisioned client_secret.

# AWS EKS pod identity → Ferentin.
resource "ferentin_workload_identity_provider" "aws_prod_eks" {
  name           = "aws-prod-eks"
  description    = "Accept AWS STS / EKS pod-identity tokens"
  cloud_provider = "aws"
  protocol_type  = "WORKLOAD_IDENTITY"

  jwks_uri           = "https://sts.amazonaws.com/.well-known/jwks.json"
  allowed_issuers    = ["https://sts.amazonaws.com"]
  expected_audiences = ["sts.amazonaws.com"]
  identity_claim     = "sub"

  required_claims = ["sub", "aud", "iss"]
}

# GCP workload identity federation → Ferentin.
resource "ferentin_workload_identity_provider" "gcp_prod" {
  name           = "gcp-prod"
  cloud_provider = "gcp"
  protocol_type  = "WORKLOAD_IDENTITY"

  jwks_uri           = "https://www.googleapis.com/oauth2/v3/certs"
  allowed_issuers    = ["https://accounts.google.com"]
  expected_audiences = ["https://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/ferentin"]
  identity_claim     = "email" # GCP convention
}

# GitHub Actions OIDC → Ferentin (one-config-fits-all-repos via wildcard sub).
resource "ferentin_workload_identity_provider" "github_actions" {
  name           = "github-actions"
  cloud_provider = "github"
  protocol_type  = "OIDC"

  jwks_uri           = "https://token.actions.githubusercontent.com/.well-known/jwks"
  allowed_issuers    = ["https://token.actions.githubusercontent.com"]
  expected_audiences = ["api.ferentin.net"]

  required_claims = ["repository", "ref"]
}

# Cursor Cloud Agents → Ferentin.
#
# Cursor mints a token for any audience a caller asks for, so `aud` alone does
# not tie a token to your organisation — `team_id` does. cloud_config must list
# your team in allowed_team_ids (a bare "*" is refused); the provider fails the
# plan otherwise, because the platform would reject every token.
resource "ferentin_workload_identity_provider" "cursor_agents" {
  name           = "cursor-agents"
  cloud_provider = "cursor"
  protocol_type  = "WORKLOAD_IDENTITY"

  jwks_uri           = "https://api.cursor.com/keys"
  allowed_issuers    = ["https://api.cursor.com"]
  expected_audiences = ["https://auth.ferentin.net"]

  # Cursor's `sub` is the human who launched the agent; key on the agent run.
  identity_claim  = "cloud_agent_id"
  required_claims = ["sub", "iss", "aud", "cloud_agent_id", "team_id", "agent_runtime"]

  cloud_config = jsonencode({
    allowed_team_ids     = ["team_abc123"]
    allowed_repositories = ["github.com/acme/*"]
  })
}
