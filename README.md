# terraform-provider-ferentin

Terraform provider for the [Ferentin](https://ferentin.net) AI authorization platform. Declaratively manage tenant resources: edge sites, LLM / MCP / OTEL policies, MCP servers, AI-agent OIDC clients, and the related catalogs.

**Status:** `v0.1.0` candidate. Covers 11 resources + 7 data sources across the v1 entity surface. See [CHANGELOG.md](CHANGELOG.md).

## Quick start

```hcl
terraform {
  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

# Reads the tokens `ferentin login --profile prod` stashed in the OS keyring,
# refreshes them transparently as they expire. Same UX as `aws` shared
# credentials. endpoint defaults to https://api.ferentin.net; tenant_id
# auto-resolves from the access token's `tid` claim.
provider "ferentin" {
  profile = "prod"
}

resource "ferentin_edge_site" "us_east" {
  site_id   = "prod-us-east-1a"
  site_name = "US East 1A"
}
```

A multi-resource end-to-end composition lives in [`examples/complete/`](examples/complete/). The [`examples/mcp-server-from-card/`](examples/mcp-server-from-card/) example drives an MCP provider + server pair from a discovered [`server-card.json`](https://modelcontextprotocol.io) so the Terraform config stays in sync with the upstream MCP server as it evolves.

## Authentication

Three mutually exclusive auth modes — pick whichever fits the caller. All three derive `tenant_id` from the JWT's `tid` claim, so production configs rarely set it explicitly.

| Mode | Provider attributes | When to use |
| --- | --- | --- |
| **Shared profile** *(recommended for interactive users)* | `profile`, optional `shared_config_file` | Local development. Reuses the `ferentin` CLI's stored tokens; auto-refreshes via the stored refresh_token. |
| **OAuth2 client_credentials** *(recommended for CI / service accounts)* | `client_id`, `client_secret`, optional `auth_url` | Long-running automation. Mints fresh tokens on demand; refreshes ~60s before expiry. |
| **Pre-minted bearer token** | `token` | Tests, one-off applies, environments that pre-provision a short-lived token. |

Full provider attribute reference at [`docs/index.md`](docs/index.md).

## Resources & data sources

| Resource | Purpose |
| --- | --- |
| `ferentin_edge_site` | Logical region/datacenter for service-edge enrollment. |
| `ferentin_llm_provider` | Tenant binding for an LLM provider (Anthropic, OpenAI, Vertex, …). WriteOnly `api_key`. Distinct from the `data "ferentin_llm_provider"` global-catalog source — same noun, different block type. |
| `ferentin_llm_policy` | ABAC governance for LLM traffic with nested criteria + conditions. |
| `ferentin_mcp_provider` | Tenant-custom MCP provider definition. |
| `ferentin_mcp_server` | Tenant binding of an MCP provider to a specific endpoint / credential set. |
| `ferentin_mcp_policy` | Allow/deny ABAC governance for MCP traffic with optional rate limits. |
| `ferentin_otel_sink` | Telemetry destination (Datadog, Honeycomb, OTLP). |
| `ferentin_otel_policy` | Signals → sinks routing. |
| `ferentin_ai_agent` | AI-agent OIDC client (constrained to the macro-scope allowlist). |
| `ferentin_workload_oauth_client` | Outbound OAuth credentials for `cc_federated` MCP upstreams. WriteOnly secrets. |
| `ferentin_workload_identity_provider` | Inbound trust config for cloud-issued workload JWTs (AWS / GCP / Azure / GitHub …). |

| Data source | Purpose |
| --- | --- |
| `ferentin_llm_provider` | Global LLM provider catalog lookup. |
| `ferentin_mcp_provider` / `ferentin_mcp_providers` | Global MCP provider catalog (single / list). |
| `ferentin_mcp_server_card` | Community MCP server-card catalog. |
| `ferentin_otel_sink_provider` | Global OTEL sink catalog entry. |
| `ferentin_workload_oauth_client_test` | Re-runs the IdP probe on every plan; surfaces `overall_pass` + error categories. |
| `ferentin_workload_identity_provider_test` | Re-runs the trust-config probe; returns raw JSON for `jsondecode()`. |

## Environment variables

Every provider-block attribute has a matching env-var fallback. Attribute wins when both are set.

| Env var | Provider attribute |
| --- | --- |
| `FERENTIN_ENDPOINT` | `endpoint` *(defaults to `https://api.ferentin.net`)* |
| `FERENTIN_TENANT_ID` | `tenant_id` *(defaults to JWT `tid` claim)* |
| `FERENTIN_PROFILE` | `profile` |
| `FERENTIN_SHARED_CONFIG_FILE` | `shared_config_file` *(defaults to `~/.ferentin/.ferentin.yaml`)* |
| `FERENTIN_TOKEN` | `token` |
| `FERENTIN_CLIENT_ID` | `client_id` |
| `FERENTIN_CLIENT_SECRET` | `client_secret` |
| `FERENTIN_AUTH_URL` | `auth_url` |
| `FERENTIN_INSECURE_SKIP_VERIFY` | `insecure_skip_verify` |

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md) for local dev setup. The provider depends on the SDK in [`ferentin-cli-app/pkg/adminapi`](https://github.com/ferentin-net/ferentin-cli-app/tree/main/pkg/adminapi); local builds use a `replace` directive pointing at `../ferentin-cli-app`.

Releases are tag-driven via [goreleaser](.goreleaser.yml). See [RELEASE.md](RELEASE.md) for the runbook.

## License

Mozilla Public License 2.0 — see [LICENSE](LICENSE).
