# Complete example

End-to-end Ferentin tenant configuration: edge site + LLM provider + LLM
policy + MCP server + MCP policy + data protection policy + OTEL sink + OTEL
policy + AI agent. Apply order is implicit from attribute references.

## Try it

```sh
# endpoint and tenant_id auto-default — only the credentials are required.
export TF_VAR_client_id="<service-account-client-id>"
export TF_VAR_client_secret="<service-account-client-secret>"
export TF_VAR_anthropic_api_key="<your-anthropic-key>"

terraform init
terraform plan
terraform apply
```

### CI / hot-loop tip

In `client_credentials` mode, the provider mints a fresh IdP token during
`Configure()` to auto-resolve `tenant_id` from the JWT's `tid` claim. That
adds one IdP round-trip per `terraform plan` and per `apply`. For
scheduled CI pipelines that re-plan every few minutes, set the
`FERENTIN_TENANT_ID` env var (or the `tenant_id` attribute on the
provider block) explicitly to skip the lookup:

```sh
export FERENTIN_TENANT_ID="<your-tenant-uuid>"
```

## What it builds

| Resource                          | Purpose                                                |
| --------------------------------- | ------------------------------------------------------ |
| `ferentin_edge_site`              | US-East edge site for routing                          |
| `ferentin_llm_provider`           | Anthropic prod binding (WriteOnly api_key)             |
| `ferentin_llm_policy`             | Internal-employees-only governance                     |
| `ferentin_mcp_server`             | Federated Salesforce MCP, edge-routed via the site    |
| `ferentin_mcp_policy`             | Read-only Salesforce allowlist with 120 req/min limit  |
| `ferentin_data_protection_policy` | Tokenize US PII + log exfiltration URLs (DLP)          |
| `ferentin_otel_sink`              | Honeycomb OTLP/HTTP destination                        |
| `ferentin_otel_policy`            | Trace forwarding to Honeycomb                          |
| `ferentin_ai_agent`               | Claude-platform team assistant (OIDC client)           |

The `agent_client_secret` output is the only value you need to capture
after first apply — the platform doesn't echo it on subsequent reads.
