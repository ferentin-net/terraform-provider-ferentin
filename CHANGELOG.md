# Changelog

All notable changes to the Ferentin Terraform provider are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
the provider adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Cross-attribute ConfigValidators** at plan time:
  - Provider: `token` is `Conflicting` with `client_id` / `client_secret`;
    `client_id` and `client_secret` are `RequiredTogether`. Catches
    mis-shapen auth blocks before any HTTP request.
  - `ferentin_llm_provider_instance`: `aws_region` and `role_arn` are
    `RequiredTogether` (IAM cross-account auth must be specified as a pair).
- **Rich SDK error diagnostics** via `addSDKError` helper. Translates the
  SDK's typed sentinel errors into actionable Terraform diagnostics:
  `ErrPreconditionFailed` → "run terraform refresh", `ErrUnauthorized` →
  "token expired / wrong credentials", `ErrForbidden` → "principal lacks
  scope", `ErrConflict` → "name already taken", `ErrRateLimited` → "rerun
  with -parallelism=1", `ErrServer` → "transient platform error". Replaces
  flat `err.Error()` text on every Create/Read/Update/Delete path.
- **`examples/complete/`** end-to-end composition (8 resources + 2 data
  sources) that the registry can render as a multi-resource walkthrough.
- **Acceptance tests for all 9 resources**: added `mcp_provider`,
  `mcp_policy`, `llm_policy`, `otel_policy`, and `ai_agent` to the existing
  `edge_site`, `llm_provider_instance`, `mcp_server`, `otel_sink` set.
  Total coverage: 9/9. All gated on `TF_ACC=1`.
- **CI/CD scaffolding**:
  - `.github/workflows/ci.yml` — build, lint, unit tests, docs-drift check on
    every PR.
  - `.github/workflows/acceptance.yml` — manually-triggered acceptance run
    against a live platform.
  - `.github/workflows/release.yml` — tag-driven goreleaser release flow
    publishing signed multi-arch binaries.
  - `.goreleaser.yml` — multi-arch (linux/darwin/windows/freebsd ×
    amd64/arm64/arm/386 minus invalid combos) build + GPG-signed checksums.
  - `terraform-registry-manifest.json` — declares protocol v6.
  - `.golangci.yml` — minimal but opinionated lint config (errcheck, govet,
    staticcheck, gocritic, revive, copyloopvar, bodyclose, errorlint).
  - `RELEASE.md` — registry-publishing runbook.
- **OAuth2 client_credentials auth** on the provider block — `client_id` /
  `client_secret` (and optional `auth_url`) opt into automatic token refresh
  via `adminapi.NewWithClientCredentials`. Tokens are minted on first request
  and refreshed ~60s before expiry; suitable for long-running Terraform
  applies and CI jobs where a pre-minted 15-minute token would expire
  mid-flight. Static `token` auth remains supported for tests and ad-hoc
  use.
- **WriteOnly secret attributes** (Terraform 1.11+) on
  `ferentin_llm_provider_instance`: `api_key`, `credentials`, `external_id`.
  Values flow from config to the platform during apply but never enter
  Terraform state — a leaked state file does NOT expose them. Each is paired
  with a companion `<attr>_wo_version` integer; bump it to force the secret
  to be re-sent on the next apply.
- **Explicit defaults via plan modifiers** for boolean attributes (no more
  reliance on opaque server-side defaults). `enabled` defaults to `true` on
  all policy / sink / server / instance / agent resources; edge-site
  `allow_http_upstream` is `false`, `verify_upstream_tls` is `true`,
  `bundle_cloud_mcp` / `llm_enabled` / `mcp_enabled` are `true`,
  `monitoring_enabled` is `false`; `mcp_provider.allow_endpoint_override` is
  `false`; `mcp_policy.validate_arguments` is `true`. Defaults are surfaced
  in the schema docs.
- **Enum validators** on `mcp_server.deployment_mode` /
  `.upstream_auth_strategy` / `.transport_type`,
  `otel_sink.sink_type` / `.protocol` / `.compression`,
  `mcp_provider.transport`, `otel_policy.signals` (per-element), and
  `llm_policy.criteria.operator`. Invalid values are caught at plan time
  with a clear error instead of surfacing as opaque platform 400s during
  apply.
- **`## Import` section** in every resource's schema documentation,
  showing the exact `terraform import` command and the
  `<tenant_id>/<entity_id>` (or `<entity_id>` alone) syntax.

### Changed
- Provider-level `token` and `client_secret` are marked `Sensitive` only
  (not `WriteOnly`). Provider config is never persisted to Terraform state,
  so the resource-level WriteOnly mechanic doesn't apply; Sensitive is the
  correct knob and gives the same practical protection (redacted in logs
  and plan output).
- Auth routing in `Configure()`: setting both `token` and
  `client_id`/`client_secret` now fails with a clear "conflicting auth
  configuration" diagnostic instead of silently preferring one. Setting
  only one half of the client-credentials pair fails with "incomplete
  client_credentials configuration".

### Fixed
- `llm_provider_instance.Update` no longer round-trips the secret
  attributes through state on every plan refresh — secrets are sent only
  when the user bumps the corresponding `*_wo_version` companion, matching
  the documented rotation contract.

## [0.1.0] - 2026-04-XX (planned)

Initial public release covering the v1 entity surface:

### Resources
- `ferentin_edge_site` — logical region/datacenter for service-edge.
- `ferentin_llm_provider_instance` — tenant binding for an LLM provider.
- `ferentin_llm_policy` — ABAC governance for LLM traffic.
- `ferentin_mcp_provider` — tenant-custom MCP provider definition.
- `ferentin_mcp_server` — tenant binding of an MCP provider.
- `ferentin_mcp_policy` — ABAC governance for MCP traffic (allow/deny on
  tools/toolsets, optional rate limits).
- `ferentin_otel_sink` — tenant telemetry destination.
- `ferentin_otel_policy` — selects signals → sinks routing.
- `ferentin_ai_agent` — AI-agent OIDC client (macro-only scope allowlist).

### Data sources
- `ferentin_llm_provider` — global LLM provider catalog lookup.
- `ferentin_mcp_provider` — single global MCP provider catalog entry.
- `ferentin_mcp_providers` — global MCP provider catalog (list).
- `ferentin_mcp_server_card` — community MCP server-card catalog.
- `ferentin_otel_sink_provider` — global OTEL sink catalog entry.

### SDK
- Bundled `github.com/ferentin-net/ferentin-cli-app/pkg/adminapi` SDK with
  per-entity sub-clients, transport-level retry, RFC 9745 rate-limit
  observability, `Idempotency-Key` (platform #650) and `If-Match` /
  optimistic-concurrency (platform #649) support, and `managed_by`
  provenance propagation (platform #651).

[Unreleased]: https://github.com/ferentin-net/terraform-provider-ferentin/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/ferentin-net/terraform-provider-ferentin/releases/tag/v0.1.0
