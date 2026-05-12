# Local development

## Prerequisites

- Go 1.26+ (Go 1.24+ for the `tool` directive in `go.mod`)
- Terraform 1.10+ for any `terraform` commands
- A local checkout of [`ferentin-cli-app`](https://github.com/ferentin-net/ferentin-cli-app) **alongside** this repo (`../ferentin-cli-app/`) — the provider's `go.mod` has a `replace` directive pointing at it. The SDK has not yet been published as a standalone module.

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

## Module path quirks

The cli-app SDK module is `github.com/ferentin-net/ferentin-cli-app/pkg/adminapi`. The `replace` directive in `go.mod` makes Go pick up your local checkout instead of resolving against `github.com`. Drop the `replace` line when both repos are publicly tagged and CI builds from upstream.
