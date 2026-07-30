# Complete example

End-to-end Ferentin tenant configuration: edge site + LLM provider + LLM
policy + MCP server + MCP policy + data protection policy + OTEL sink + OTEL
policy + AI agent, plus the endpoint surface (device groups + on-device
destination rules + posture) for managed laptops. Apply order is implicit from
attribute references.

For an endpoint-only rollout — staged from observe to enforce, with the
priority ladder spelled out — see [`../endpoint-fleet/`](../endpoint-fleet/).

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
| `ferentin_device_group`             | `engineering` (MDM-sourced) + `contractors` (manual)   |
| `ferentin_endpoint_destination_rule` | Block ChatGPT for contractors, steer Claude to service-edge, deny unsanctioned hosts |
| `ferentin_endpoint_policy_settings` | Tenant-default observe posture + strict contractor override |

The `agent_client_secret` output is the only value you need to capture
after first apply — the platform doesn't echo it on subsequent reads.

## Two things to know before you apply the endpoint half

**Destination-rule `priority` is policy semantics, not cosmetics.** Rules are
evaluated on-device, first match wins, ascending. The admin console's reorder
arrows write the same field, so don't split ownership of ordering between
Terraform and the console — they will fight on every plan.

**`ferentin_endpoint_policy_settings` is an upsert, and destroying the
tenant-default row does not reset it.** The platform refuses to delete a row
every device resolves through, so Terraform drops it from state and the fleet
keeps enforcing whatever was last applied. That is fail-closed on purpose. To
genuinely stand enforcement down, apply the permissive posture
(`report_only` / `allow`) first, then destroy. It also means an apply against
a tenant whose posture was configured in the console **adopts** that row
rather than failing — `managed_by` still names the original creator, which is
what the `endpoint_posture_drift` output surfaces.

## Requires the `devices` scope

The endpoint resources need either `devices:groups:rw` (narrow — group CRUD
only) or the broad `devices:rw` on top of the `policy:rw` the rest of the
config uses. Prefer the narrow one: `devices:rw` also grants device status
transitions, per-serial certificate revocation, and forced re-enrollment,
which a pipeline that only creates groups has no business holding.
