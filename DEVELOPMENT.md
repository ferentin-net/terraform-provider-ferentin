# Local development

## Prerequisites

- Go 1.26+ (Go 1.24+ for the `tool` directive in `go.mod`)
- Terraform 1.10+ for any `terraform` commands
- A local checkout of [`ferentin-cli-app`](https://github.com/ferentin-net/ferentin-cli-app) **alongside** this repo (`../ferentin-cli-app/`). The SDK is not published as a standalone module; see [Module path quirks](#module-path-quirks) for the `go.work` setup that wires the two together.
- `GOPRIVATE` configured for the org, once per machine:

  ```sh
  go env -w GOPRIVATE='github.com/ferentin-net/*'
  ```

  Without it Go tries to verify the private SDK module against `sum.golang.org` and fails with a 404 — **even in workspace mode**, where it never needs to download the module at all. CI sets the same value (see the `GOPRIVATE` env block in `.github/workflows/ci.yml`).

## Build & install for local Terraform

```sh
make install
```

That builds the binary and drops it at:

```
~/.terraform.d/plugins/registry.terraform.io/ferentin-net/ferentin/dev/<os_arch>/terraform-provider-ferentin
```

Add this to `~/.terraformrc` so `terraform` finds the dev build instead of the registry:

```hcl
provider_installation {
  dev_overrides {
    "ferentin-net/ferentin" = "/Users/<you>/.terraform.d/plugins/registry.terraform.io/ferentin-net/ferentin/dev/<os_arch>"
  }
  direct {}
}
```

Now `terraform init` skips registry resolution for this provider, and every `make install` is a hot-reload.

## Run an example

```sh
cd examples/resources/ferentin_edge_site
terraform init     # no-op with dev_overrides
terraform plan -var "tenant_id=$FERENTIN_TENANT_ID" -var "ferentin_token=$FERENTIN_TOKEN"
```

The provider expects `endpoint`, `tenant_id`, and `token` either on the block or via `FERENTIN_*` env vars.

## Acceptance tests

```sh
make testacc
```

Acceptance tests need real platform endpoints; set:

```sh
export TF_ACC=1
export FERENTIN_ENDPOINT=https://api.local.ferentin.test
export FERENTIN_TENANT_ID=...
export FERENTIN_TOKEN=$(... mint via curl, see ferentin-cli-app/CLAUDE.md ...)
export FERENTIN_INSECURE_SKIP_VERIFY=1
```

For the moment, only smoke acceptance is wired (Phase 2 MVP); full coverage follows in subsequent batches per §5 of the design doc.

## Provider runtime debugging

Run with `--debug`:

```sh
./terraform-provider-ferentin --debug
# Copy the printed TF_REATTACH_PROVIDERS env var and use:
TF_REATTACH_PROVIDERS='...' terraform apply
```

Attach delve to the running process by PID.

## Running acceptance tests

The acceptance suite (`TestAcc*`, gated by `TF_ACC=1`) creates and destroys real
resources against a live platform. **Run it against local dev, never production.**

```sh
feri a                                    # admin-api-server on the nginx profile
export FERENTIN_TENANT_ID=<tenant-uuid>
export FERENTIN_CLIENT_ID=<cc-client>     # or export FERENTIN_TOKEN=<jwt>
export FERENTIN_CLIENT_SECRET=<secret>
make testacc-local
```

`testacc-local` defaults `FERENTIN_ENDPOINT` to `https://api.local.ferentin.test`
and sets `FERENTIN_INSECURE_SKIP_VERIFY=1`, since local dev serves a self-signed
certificate.

### Scopes the test principal needs

A missing scope shows up as a bare `403` on one test while everything else
passes, which reads like a code bug and is not one. Each row is the `:rw` scope
the resource's controller requires; the matching `:r` (or `:admin`) covers the
read-only data sources.

| Resource(s) | Scope | Controller |
| --- | --- | --- |
| `ferentin_edge_site` | `edge:rw` | `EdgeSiteController` |
| `ferentin_llm_provider` | `provider:rw` | `ProviderInstanceController` |
| `ferentin_mcp_provider`, `ferentin_mcp_server`, `ferentin_mcp_server_from_card` | `mcp:servers:rw` | `McpTenantProviderController`, `McpProviderInstanceAdminController` |
| `ferentin_llm_policy`, `ferentin_mcp_policy`, `ferentin_endpoint_destination_rule`, `ferentin_endpoint_policy_settings` | `policy:rw` | `LlmPolicyController`, `McpPolicyController`, `EndpointPolicyController` |
| `ferentin_data_protection_policy` | `policy:rw` (+ `policy:t` to test) | `DataProtectionPolicyController` |
| `ferentin_otel_sink`, `ferentin_otel_policy` | `otel:rw` | `OtelSinkController`, `OtelPolicyController` |
| `ferentin_ai_agent` | `clients:agent:rw` | `AdminOidcClientController` |
| `ferentin_workload_oauth_client`, `ferentin_workload_identity_provider` | `idps:rw` | `WorkloadOAuthClientController`, `WorkloadIdentityProviderController` |
| `ferentin_device_group` | `devices:groups:rw` *or* `devices:rw` | `DeviceGroupController` |

`hasWriteAccess('x')` accepts `x:rw` or `x:admin`; `hasReadAccess('x')` also
accepts `x:r`. There is no global `admin` scope that covers all of these — the
workload tests need `idps:rw` specifically, not a blanket grant.

The seeded `ferentin.iac.operator` role carries the policy and device scopes. A
role-bound `client_credentials` client on a platform older than migration 1215
has no `devices:groups:rw` and will 403 on the device-group tests.

**`idps:rw` is a deliberate exclusion, not a gap.** Platform migration 783 keeps
identity scopes (`idps:rw`, `users:rw`, `scim:rw`) out of `ferentin.iac.operator`
— it calls them "the highest blast-radius IaC mistake" — along with
`policy:activate` (separation of duties) and the self-escalation denylist
(`clients:rw`, `keys:rw`, …). Do **not** widen that role to turn the workload
tests green; that dissolves a boundary the platform team drew on purpose. Those
two tests skip themselves unless the principal demonstrably holds the scope, or
you opt in explicitly:

```sh
FERENTIN_ACC_IDENTITY_SCOPES=1 make testacc-local RUN='TestAccWorkload'
```

Run them as a principal that legitimately carries `idps:rw` — a tenant admin, or
a purpose-built role — rather than by editing the IaC one.

### Running a subset

Each test, and each step within one, builds a fresh provider — so under
`FERENTIN_CLIENT_ID` auth each mints its own token, and a full suite trips the
auth server's `client_credentials` burst limit (10 per 10s). Scope the run:

```sh
make testacc-local RUN='TestAccDeviceGroup|TestAccEndpointDestinationRule'
```

`FERENTIN_TOKEN` avoids minting entirely and is the better choice for a full
run.

### Test rows are not cleaned up

A run killed part-way (rate limit, `^C`, a panic) leaves whatever it created
behind, and nothing sweeps it. Fixture names carry a per-process random tag so a
re-run does not collide with those leftovers — see `randomSuffix`. Rows named
`tf-acc-*` in local dev are safe to delete by hand.

**Why not CI.** The platform has exactly two environments — `nginx` (local dev)
and `aws-secure` (production). A GitHub-hosted runner cannot reach local dev, and
`.github/workflows/acceptance.yml` hard-blocks production. It is not a policy
preference: `TestAccEndpointPolicySettings_tenantDefault` upserts the
tenant-default endpoint posture, which on production changes what every managed
device in the tenant enforces, and destroy of that row is deliberately a no-op —
so an interrupted run leaves production posture at whatever the test last set,
with no cleanup path. The workflow is retained for the day a staging environment
exists.

## Module path quirks

The cli-app SDK module is `github.com/ferentin-net/ferentin-cli-app/pkg/adminapi`.

### Use `go.work`, never a committed `replace`

To develop against your local SDK checkout, create a Go workspace **inside this repo**:

```sh
go work init . ../ferentin-cli-app
```

`go.work` and `go.work.sum` are gitignored. Nothing else changes — `go build`, `go test`, and `make install` all pick up the sibling checkout automatically. `GOWORK=off` restores proxy resolution if you need to reproduce a CI failure locally.

**Do not add `replace github.com/ferentin-net/ferentin-cli-app => ../ferentin-cli-app` to `go.mod`.** A committed `replace`:

- breaks CI, where `../ferentin-cli-app` does not exist in the checkout;
- breaks the registry build the same way, and that failure only surfaces at publish time;
- is easy to forget, because it makes local builds pass.

The `no-replace` job in `.github/workflows/ci.yml` fails the build if one is present, and also fails if `go.work` is ever committed.

### Landing a change that spans both repos

When a provider change consumes SDK code that doesn't exist upstream yet, the provider branch **cannot** go green until the SDK side lands. That is the intended signal, not something to route around. The merge train:

1. Merge the `ferentin-cli-app` PR.
2. Here: `go get github.com/ferentin-net/ferentin-cli-app@<merge-sha>`. This produces a pseudo-version (e.g. `v0.4.6-0.20260616165901-4f510243e4fe`) — **no tag is required**, which is how the current requirement was pinned.
3. Commit the `go.mod` / `go.sum` change and push. CI now resolves the real module and goes green.
4. Merge the provider PR.
