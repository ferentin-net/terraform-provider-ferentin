# Changelog

All notable changes to the Ferentin Terraform provider are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
the provider adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### BREAKING CHANGES
- **`ferentin_llm_provider_instance` renamed to `ferentin_llm_provider`.**
  The longer name was awkward in CLI/HCL alike; the new noun matches
  the `admin llm-providers` CLI surface and the same parallel as
  `ferentin_mcp_provider` (resource) vs `data "ferentin_mcp_provider"`
  (data source). Companion to ferentin-cli-app commit
  [`f7cc682`](https://github.com/ferentin-net/ferentin-cli-app/commit/f7cc682),
  which renames the CLI noun `admin llm-provider-instances` →
  `admin llm-providers` and frees the MCP tool `list_llm_providers` for
  tenant-scoped use (the global catalog tool is now
  `list_catalog_llm_providers`).

  The new name collides with the existing data source
  `ferentin_llm_provider` only at the symbol level — Terraform's block
  type disambiguates `resource` from `data` (same pattern AWS uses for
  `aws_iam_policy`).

  Migration:
  1. Update HCL blocks from `resource "ferentin_llm_provider_instance"`
     to `resource "ferentin_llm_provider"`.
  2. Update interpolation refs from `ferentin_llm_provider_instance.X`
     to `ferentin_llm_provider.X`.
  3. Run `terraform state mv 'ferentin_llm_provider_instance.X'
     'ferentin_llm_provider.X'` for each renamed resource.
     - For resources with `count =`, append `[i]` on both sides:
       `terraform state mv 'ferentin_llm_provider_instance.X[0]'
       'ferentin_llm_provider.X[0]'`.
     - For `for_each =`, append `["key"]` on both sides.
     - For module-nested resources, prefix `module.NAME.` on both
       sides.
  4. Run `terraform plan` to confirm no diff.
  5. Maintainers / fork operators: re-run `tfplugindocs generate` after
     pulling this change so the stale `docs/resources/llm_provider_
     instance.md` ghost is removed from local working copies (the file
     was renamed via `git mv`; a clean checkout doesn't need this, but
     in-place rebases can leave the old path behind).

  No schema attributes changed; the underlying REST URL
  (`/admin/tenants/{tid}/provider-instances`) and DB table
  (`llm_provider_instances`) are unchanged. SDK type
  `LLMProviderInstancesAPI` keeps its name for platform alignment.

### Security
- **Upgraded two modules with reachable vulnerabilities**, both confirmed by
  `govulncheck` as reachable from this provider's own call graph (not merely
  present in the module graph):
  - `google.golang.org/grpc` 1.81.1 → 1.82.1 — [GO-2026-6061](https://pkg.go.dev/vuln/GO-2026-6061),
    xDS RBAC authorization engine + HTTP/2 handling. Reached via
    `providerserver.Serve` → `transport.http2Server`, i.e. the plugin's own
    gRPC listener.
  - `golang.org/x/text` 0.38.0 → 0.40.0 — [GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970),
    infinite loop on invalid input in `norm`. Reached via `fmt.Sprintf` in the
    DLP profile data source.

  `golang.org/x/net` (0.56.0 → 0.57.0) and `golang.org/x/crypto`
  (0.53.0 → 0.54.0) came along as transitive requirements of those two.
  `govulncheck ./...` now reports **no vulnerabilities**.

### Added
- **`ferentin_ai_agent` now exposes `version` and uses optimistic concurrency.**
  The attribute is threaded as `If-Match` on Update and Delete, so a concurrent
  admin-console edit is rejected with 412 instead of being silently clobbered.
  This was the last IaC-managed resource carrying provenance attributes but no
  optimistic-concurrency guard.

  Requires an admin-api carrying the platform fix that added `version` to the
  OIDC-client projection (ferentin-platform#2061). Against an older admin-api
  every read of an OIDC client returned `version = 0`, which is why this could
  not be wired before: the second update of any agent would have failed with a
  permanent 412 — the same failure `ferentin_mcp_policy` hit when it sent a
  hardcoded `W/"0"`.

  Existing state gains the attribute on the next refresh; no configuration
  change is needed. One edge: applying with `-refresh=false` on the very first
  run after upgrading reads `version` as `0` from pre-upgrade state, which will
  412 for any agent that has already been updated once. Refresh once (the
  default) and it resolves itself.

- **Three endpoint-policy resources** — governance for AI traffic on managed
  devices via the macOS endpoint agent. Platform #2038 (built on #2010 / #2018).
  - **`ferentin_device_group`** — the policy-scoping unit the other two target.
    Added first so `device_group_ids` can be a reference
    (`ferentin_device_group.contractors.group_id`) rather than a hardcoded UUID
    that is unreferenceable, unimportable, and stale the moment a group is
    recreated. Exposes `version` plus the four `managed_by*` provenance
    attributes and threads the version through Update/Delete as `If-Match`,
    once platform migration 1217 gave `device_groups` the IaC-readiness columns
    (platform #2040 item 2 — before that, writes were last-write-wins).
    Requires `devices:groups:rw` (narrow, group-CRUD-only — held by the
    seeded `ferentin.iac.operator` role as of platform migration 1215) or the
    broad `devices:rw`. Prefer the narrow scope: `devices:rw` also grants
    device status transitions, per-serial certificate revocation, and forced
    re-enrollment.
  - **`ferentin_endpoint_destination_rule`** — allow / block / steer per AI
    provider or explicit host, scoped by macOS app code identity
    (`app_bundle_ids` / `app_signing_ids` / `app_team_ids`) and device group.
    Full CRUD with `If-Match` optimistic concurrency and the four `managed_by*`
    provenance attributes. Cross-field rules (`ai_provider` ⇒ `catalog_slug`,
    `host` ⇒ `destination_hosts`, `steer` ⇒ `steer_to_url`, https-only URLs)
    are validated at **plan** time via `ValidateConfig` rather than surfacing as
    an opaque 400 at apply. Subject scoping is authored with a `criteria` block
    (see below).
  - **`ferentin_endpoint_policy_settings`** — endpoint posture, either the
    tenant default (omit `device_group_id`) or a per-group override. The
    platform API is upsert-only, which drives three documented deviations:
    Create may **adopt** a pre-existing row (`managed_by` still reports the
    original creator, so adoption shows as drift); Read lists `/settings` and
    filters on `device_group_id` because there is no GET-by-id; and
    `terraform destroy` on the tenant-default row is a **no-op that drops it
    from state and leaves it enforcing**, because the platform refuses to delete
    a row every device — including ungrouped ones — resolves through. A warning
    diagnostic names the still-active posture and the explicit two-step needed
    to stand enforcement down.

    This is fail-closed on purpose. Resetting to the permissive defaults would
    be reachable by ordinary refactoring — removing a resource block or dropping
    a module runs Delete just as `terraform destroy` does — and would silently
    move a fleet from "quarantine unapproved MCP servers, allowlist AI
    destinations" to "observe only", arriving on-device as a routine bundle
    update. It follows the precedent for adopted singletons a provider cannot
    delete: `aws_default_vpc` drops state and touches nothing;
    `aws_default_security_group` resets to *maximally restrictive*. Neither
    resets to permissive. A group override, which the platform *can* delete, is
    genuinely deleted and the group falls back to the tenant default.

- **`criteria` on `ferentin_endpoint_destination_rule`** — user/department ABAC
  scoping is now authorable from Terraform, replacing the read-only
  `criteria_json` passthrough that only *preserved* what the console wrote
  (platform #2040 item 1). A Terraform-only shop can now express "legal may use
  ChatGPT" without touching the admin console. The block is the same shape as
  the one on `ferentin_llm_policy` / `ferentin_mcp_policy` /
  `ferentin_data_protection_policy`, plus the rule's own `criteria_combinator`
  for how GROUPS combine.

  Two things worth knowing before you write one:
  - **Removing criteria widens the rule.** Criteria narrow a rule to a
    population, so a config that drops the block turns "legal may use ChatGPT"
    into "everyone may". `terraform plan` shows the removal and the provider
    adds a warning naming the rule and the group count. Unlike `criteria_json`,
    criteria authored in the console are no longer preserved for you —
    Terraform owns the field now.
  - **Not yet enforced on-device.** The endpoint agent ships criteria in the
    policy bundle but refuses to match a rule that has them until the on-device
    user principal lands (platform #2014). That is fail-closed for `allow` and
    `steer`; a criteria-scoped `block` does not block.

  `criteria_json` is removed rather than deprecated — it never appeared in a
  tagged release.

- **`model_constraints` on `ferentin_llm_provider`** — nested
  `{ mode = "allowlist", models = [...] }` attribute that pins an
  instance to a specific set of catalog models. Persisted on the
  platform as `provider_config.model_constraints` (JSONB); echoed back
  on Read so drift detection works. See the resource example for a
  GPT-5.5-only configuration.

### Changed
- **One shared `criteria` implementation across all four resources that have
  one.** `ferentin_llm_policy`, `ferentin_mcp_policy`, and
  `ferentin_data_protection_policy` each carried a near-identical private copy
  of the schema, models, and both conversion directions; adding
  `ferentin_endpoint_destination_rule` as the fourth caller made that a copy too
  many (platform #2040). The schema, attribute names, and semantics are
  unchanged for the three existing resources — the extraction is deliberately
  behaviour-preserving, and the only generated-doc changes are wording.

  Three small behaviour changes come with it. Two move a server-side rejection
  to plan time:
  - `conditions` must now have at least one entry, mirroring `@NotEmpty` on the
    platform's `PolicyCriteria`. A group with none has no defined truth value;
    the three policy resources would 400 on it at apply.
  - A criteria block whose `value` fails to convert can no longer be silently
    dropped from the request. Previously the diagnostic was discarded when *no*
    criteria converted, which sent a policy with no criteria at all — i.e. one
    that matches everyone.

  And one is endpoint-only: `criteria[].type` on
  `ferentin_endpoint_destination_rule` is now restricted to the platform's enum
  (`claims`, `context`, `request`, `time`). That column is stored opaquely and
  validated nowhere server-side, so a typo used to survive the write and reach
  the agent, which **drops** a rule whose criteria will not parse — fail-open
  for a `block` rule. The three policy resources are left unvalidated: they go
  through typed DTOs, and their Java DTO enforces only `@NotBlank`, so a
  validator there could reject configs the API accepts today.

### Fixed
- **`client_credentials` auth worked for nobody using the default `auth_url`.**
  The derived default swapped `api.` → `auth.` and stopped there, but the
  platform routes CC token mints **per tenant** and the SDK appends `/token` to
  this value — so the provider posted to the global endpoint and the
  authorization server refused:

  > `invalid_request`: Tenant could not be determined. Use a tenant-specific
  > endpoint for this grant type.

  `auth_url` is consulted *only* on the `client_credentials` path, so its
  default was guaranteed to fail for the one auth mode it exists to serve.
  Anyone following the README with a service-account client hit it on their
  first `terraform plan`; it went unnoticed because token auth
  (`FERENTIN_TOKEN`) skips the mint entirely, and no acceptance test had ever
  been run against a live platform.

  The default now derives `<auth-base>/tenant/<tenant_id>`. Setting `auth_url`
  explicitly still wins, which is how you select the subdomain form
  (`https://<tenant>-sso.auth.<domain>`) — that one cannot be derived, because
  its label is the tenant *slug* and the provider only ever sees the UUID.
  Deriving without a `tenant_id` is now its own diagnostic rather than a
  misleading "endpoint did not contain `api.`".

- **Criteria conditions are sent in the shape the platform actually reads.**
  `ferentin_llm_policy`, `ferentin_mcp_policy`, and
  `ferentin_data_protection_policy` wrapped every condition `value` in a
  `{"value": <actual>}` envelope. Nothing on the platform ever unwrapped it —
  shared-core's `PolicyCriteriaEvaluator` compares `condition.getValue()`
  directly, so `Objects.equals("legal", Map{value=legal})` is `false`.

  **Every criteria-scoped policy this provider wrote silently applied to
  nobody.** For a restrictive policy (deny tools, DLP redaction, limits) scoped
  to a population, that is fail-open: the population it was written to
  constrain was never constrained. In the other direction, a policy whose
  criteria were authored in the admin console could not be read at all — the
  raw value failed SDK decode, so `terraform plan` errored outright.

  Root cause was a generated type, not provider logic: springdoc renders Java
  `Object value` as `{type: object}`, which oapi-codegen turned into
  `*map[string]interface{}` — a Go type that literally cannot hold the string,
  array, or number the field carries. Fixed at the source in
  ferentin-cli-app (`scripts/refresh-openapi.sh` Patch E strips the bogus
  `type` so codegen emits `interface{}`, matching the existing Patch B
  precedent for the same springdoc quirk), then regenerated. The provider now
  sends and reads the value raw, identical to what the admin console writes.

  **Upgrade note — one expected diff per affected condition.** Rows written by
  an older provider still contain the envelope. The first `plan` after
  upgrading shows `{"value":"legal"}` → `"legal"`; applying it repairs the row
  and the policy starts matching. The provider deliberately does **not**
  unwrap legacy envelopes on read: that would make state equal config, produce
  no diff, and leave a policy that matches nobody in place indefinitely. If
  you have criteria-scoped policies, expect that diff and take it.

- **`ferentin_mcp_policy` updates no longer 412 after the first one.** Update
  hardcoded `If-Match: W/"0"` regardless of the row's real version. That is
  correct exactly once — for a freshly-created policy — and a guaranteed 412 for
  every policy that had already been updated, because the platform's
  `McpPolicyService` does enforce the precondition. The version now comes from
  state via a new computed `version` attribute. Found while auditing the same
  pattern for the endpoint-policy resources; platform #2038.

- **`make docs-check` could never pass.** `--rendered-website-dir` is resolved
  relative to the provider directory, so the absolute `mktemp` path made
  `tfplugindocs` write a stray `./var/folders/...` tree into the repo while the
  diff target never existed. Now uses a repo-relative scratch dir and cleans up
  on both success and failure. (The committed docs for
  `data_protection_policy` / `llm_policy` were stale as a result and have been
  regenerated.)

- **`README.md` resource tables were missing shipped resources** —
  `ferentin_data_protection_policy`, `ferentin_mcp_server_from_card`, and the
  four DLP data sources were never added.

- **`managed_by` provenance now reads `"iac"` instead of `"console"`.** The
  SDK transport now stamps `X-Ferentin-Managed-By: iac` and
  `X-Ferentin-Managed-By-Module: terraform-provider-ferentin/<version>` on
  every mutating request (POST/PUT/PATCH/DELETE; reads are skipped — the
  server doesn't persist headers on GETs). Without this, the platform
  fell back to `"console"` and out-of-band edits from the admin console
  didn't surface as drift. Platform #651.

### Added
- **`examples/mcp-server-from-card/`** — drives a `ferentin_mcp_provider` +
  `ferentin_mcp_server` pair from a discovered MCP `server-card.json`
  using `jsondecode(file())`. Shows the kebab-case → underscore mapping
  for transport (the spec's `streamable-http` → the API's `http` /
  `streamable_http`) and `_meta.net.ferentin.transport.auth_type` →
  `upstream_auth_strategy`. Bundles a trimmed copy of the reference
  threat-intel card so `terraform init && terraform validate` works
  out of the box.
- **`ferentin_mcp_server` cc_federated wiring** — four new attributes that
  bind an MCP server to a `ferentin_workload_oauth_client` when
  `upstream_auth_strategy = "cc_federated"`:
  `cc_federated_workload_client_id` (FK), `cc_federated_audience_override`,
  `cc_federated_resource_override`, `cc_federated_scopes_override`. The
  workload client's `default_*` provide the tenant-wide defaults; the
  override fields narrow them per-server. See
  `examples/resources/ferentin_mcp_server/resource.tf` for an
  end-to-end Salesforce example.
- **`ferentin_workload_oauth_client` resource** — outbound OAuth credentials
  used when an `ferentin_mcp_server.upstream_auth_strategy = cc_federated`.
  The platform mints a token at the customer's IdP using these credentials
  and forwards it upstream. WriteOnly secrets (`client_secret`,
  `private_key_jwt_private_key`) with companion `*_wo_version` rotation
  knobs; standard `If-Match` optimistic concurrency. Enum-validated
  `idp_type` (auth0/entra/generic_oidc/okta), `auth_method`
  (client_secret_basic/client_secret_post/private_key_jwt),
  `audience_param_strategy`, and `private_key_jwt_alg`.
- **`ferentin_workload_identity_provider` resource** — inbound trust
  config accepting cloud-issued JWTs (AWS / GCP / Azure / OCI /
  generic_oidc / GitHub) so workloads can authenticate without a
  pre-provisioned client_secret. Enum-validated `cloud_provider` and
  `protocol_type` (OIDC/SAML/WORKLOAD_IDENTITY); server-derived
  discriminator booleans (`aws`, `azure`, `gcp`, etc.) exposed as
  Computed. No `version` field on this entity; Update / Delete are
  last-write-wins.
- **`data "ferentin_workload_oauth_client_test"`** — re-runs the platform's
  IdP-probe action on every plan and exposes `overall_pass`, `error`,
  `error_detail`, `token_type`, `token_endpoint`. Use to gate downstream
  resources on a working IdP binding.
- **`data "ferentin_workload_identity_provider_test"`** — re-runs the
  trust-config probe and returns the platform's raw JSON response (no
  typed shape in the spec). Parse with `jsondecode()`.
- SDK: `pkg/adminapi.WorkloadOAuthClients()` and
  `pkg/adminapi.WorkloadIdentityProviders()` sub-clients exposing CRUD +
  `Test()` (and `VerifyJwks()` on the OAuth client). Tag allowlist in
  `api/oapi-codegen.yaml` expanded to two new tags.

### Changed
- **`endpoint` now defaults to `https://api.ferentin.net`** (production). The
  provider previously errored when no endpoint was configured; it now falls
  back to the production URL after exhausting the attribute / env var /
  shared-profile resolution chain. Local-dev, staging, and air-gapped
  deployments still set `endpoint` explicitly — only the production case
  shortens. Matches the AWS provider's pattern of defaulting to the public
  service URL when nothing more specific is configured.
- **`tenant_id` is now optional and auto-resolves from the JWT's `tid` claim.**
  Every admin-api access token (regardless of grant type — static, CC, or
  shared profile) carries `tid` reflecting the principal's bound tenant; the
  provider decodes the claim and uses it as the default tenant. Set
  `tenant_id` explicitly only to override (e.g. cross-tenant orchestration
  from a single config). The previous "Missing tenant ID" error now appears
  only when both the attribute is unset AND the JWT has no `tid` (which
  usually means an endpoint misconfiguration, not a Ferentin token).

### Added
- **Shared-profile authentication** — third auth path on the provider block,
  alongside `token` and `client_id`/`client_secret`. Reads the tokens that
  `ferentin login --profile <name>` stashes in the OS keyring (or
  `~/.ferentin/profile:<name>` fallback), and transparently refreshes them
  via the stored refresh_token as they expire. Modelled on the AWS provider's
  `profile` + `shared_credentials_files` mechanism.
  - New provider attributes: `profile` (env `FERENTIN_PROFILE`),
    `shared_config_file` (env `FERENTIN_SHARED_CONFIG_FILE`, defaults to
    `~/.ferentin/.ferentin.yaml`).
  - The profile's `endpoint` and `insecure` flag fill in the matching
    provider-block attributes when those are otherwise unset, so a
    `provider "ferentin" { profile = "prod" }` block can be the whole
    configuration.
  - ConfigValidators mutual exclusion: `profile` conflicts with both `token`
    and `client_id`/`client_secret`. Caught at plan time.
  - Backed by a new SDK package `pkg/profileauth` with thread-safe lazy
    refresh, DPoP-aware, persists the rotated token set back to storage so
    parallel processes (CLI + Terraform on the same machine) stay in sync.
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
