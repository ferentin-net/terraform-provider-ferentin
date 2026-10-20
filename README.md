# terraform-provider-ferentin

Terraform provider for the [Ferentin](https://ferentin.net) AI authorization platform. Declaratively manage tenant resources: edge sites, LLM / MCP / OTEL policies, MCP servers, data protection policies, AI-agent OIDC clients, workload identity, endpoint policy for managed devices (device groups, destination and MCP rules, posture), and the related catalogs.

**Status:** `v0.1.0` candidate. Covers 19 resources + 17 data sources. See [CHANGELOG.md](CHANGELOG.md).

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

A multi-resource end-to-end composition lives in [`examples/complete/`](examples/complete/). The [`examples/mcp-server-from-card/`](examples/mcp-server-from-card/) example drives an MCP provider + server pair from a discovered [`server-card.json`](https://modelcontextprotocol.io) so the Terraform config stays in sync with the upstream MCP server as it evolves. [`examples/endpoint-approved-ai/`](examples/endpoint-approved-ai/) is the smallest endpoint policy worth deploying — allow the sanctioned AI providers, pin one to its signed desktop app, block the rest — and the [Approve AI apps on managed endpoints](docs/guides/endpoint-approved-ai.md) guide walks through it. [`examples/endpoint-mcp-servers/`](examples/endpoint-mcp-servers/) ([guide](docs/guides/endpoint-mcp-servers.md)) steers sanctioned remote MCP servers through the tenant's gateway and blocks the rest, and [`examples/endpoint-tool-call-guardrails/`](examples/endpoint-tool-call-guardrails/) ([guide](docs/guides/endpoint-tool-calls.md)) counts, then denies, risky tool calls from coding agents, one device group at a time. [`examples/endpoint-data-protection/`](examples/endpoint-data-protection/) ([guide](docs/guides/endpoint-data-protection.md)) logs, then blocks, SSNs, government IDs and card numbers in coding agents' prompts, tool calls and tool output on managed devices. Already configured in the console? [Bring an existing endpoint policy under Terraform](docs/guides/endpoint-adopt-existing.md) imports it with a first apply that changes nothing. [`examples/endpoint-fleet/`](examples/endpoint-fleet/) covers the managed-device surface on its own — device groups, the on-device destination-rule and MCP-rule ladders, posture staged from observe to enforce, and call-time MCP enforcement moved from shadow to enforce.

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
| `ferentin_mcp_server_from_card` | Tenant MCP server installed from a community server-card (`data "ferentin_mcp_server_card"`). |
| `ferentin_mcp_policy` | Allow/deny ABAC governance for MCP traffic with optional rate limits. |
| `ferentin_data_protection_policy` | DLP governance — detector/profile selection, redact / tokenize / block effects — on LLM, MCP and A2A traffic, and on enrolled endpoints (tool input, prompts, tool output). |
| `ferentin_otel_sink` | Telemetry destination (Datadog, Honeycomb, OTLP). |
| `ferentin_otel_policy` | Signals → sinks routing. |
| `ferentin_ai_agent` | AI-agent OIDC client (constrained to the macro-scope allowlist). |
| `ferentin_workload_oauth_client` | Outbound OAuth credentials for `cc_federated` MCP upstreams. WriteOnly secrets. |
| `ferentin_workload_identity_provider` | Inbound trust config for cloud-issued workload JWTs (AWS / GCP / Azure / GitHub …). |
| `ferentin_device_group` | Device group — the policy-scoping unit endpoint policy targets. |
| `ferentin_endpoint_destination_rule` | Allow / block rule for AI traffic on managed devices (macOS endpoint agent), optionally narrowed to compliant devices (`posture_predicate`). Destination `steer` is not available yet. |
| `ferentin_endpoint_mcp_rule` | Allow / block / steer rule for MCP servers and tool calls on managed devices: remote and local servers, tool calls, and tool behaviours. |
| `ferentin_endpoint_agent_approval` | Approve (`report_only`) or deny (`block`) one catalogued AI agent (Claude Code, Cursor, …) on managed devices, optionally per device group. |
| `ferentin_mcp_local_server` | Approve or block a local (stdio) MCP server from a command-line template or catalog entry, so managed devices run it governed. |
| `ferentin_endpoint_policy_settings` | Endpoint posture — unapproved MCP and agent actions, gateway target, DNS/QUIC and unnamed-flow blocks, local MCP block, call-time MCP enforcement, local MCP risk policy and approval routing. Tenant default or per-device-group override; values set in the console survive an apply that doesn't mention them. |

| Data source | Purpose |
| --- | --- |
| `ferentin_llm_provider` | Global LLM provider catalog lookup. |
| `ferentin_mcp_provider` / `ferentin_mcp_providers` | Global MCP provider catalog (single / list). |
| `ferentin_mcp_server_card` | Community MCP server-card catalog. |
| `ferentin_otel_sink_provider` | Global OTEL sink catalog entry. |
| `ferentin_data_protection_profiles` / `ferentin_data_protection_profile` | Built-in DLP profile catalog (list / single). |
| `ferentin_data_protection_detectors` / `ferentin_data_protection_detector` | Built-in DLP detector catalog (list / single). |
| `ferentin_workload_oauth_client_test` | Re-runs the IdP probe on every plan; surfaces `overall_pass` + error categories. |
| `ferentin_workload_identity_provider_test` | Re-runs the trust-config probe; returns raw JSON for `jsondecode()`. |
| `ferentin_device_groups` | Every device group in the tenant, with `import_id` and `managed_by`. |
| `ferentin_endpoint_destination_rules` / `ferentin_endpoint_mcp_rules` | Every endpoint rule of that kind, in evaluation order, with `import_id` and `managed_by`. |
| `ferentin_endpoint_policy_settings_scopes` | Every endpoint posture row: the tenant default, then each group override. |
| `ferentin_endpoint_agent_approvals` | Every AI-agent decision, with `import_id` and `managed_by`. |
| `ferentin_mcp_local_servers` | Every local MCP server, declared or reported by a device, with `import_id` and `managed_by`. |

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

See [DEVELOPMENT.md](DEVELOPMENT.md) for local dev setup, including running the acceptance tests against local dev (`make testacc-local`). The provider depends on the SDK in [`ferentin-cli-app/pkg/adminapi`](https://github.com/ferentin-net/ferentin-cli-app/tree/main/pkg/adminapi), pinned in `go.mod`. A local `go.work` can point at a `../ferentin-cli-app` checkout; CI builds with `GOWORK=off` and rejects `replace` directives.

Releases are tag-driven via [goreleaser](.goreleaser.yml). See [RELEASE.md](RELEASE.md) for the runbook.

## License

Mozilla Public License 2.0 — see [LICENSE](LICENSE).
