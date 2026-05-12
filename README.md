# terraform-provider-ferentin

Terraform provider for the [Ferentin](https://ferentin.net) AI authorization platform. Manages tenant-admin resources (edge sites, LLM / MCP / OTEL policies, OIDC clients, …) declaratively.

**Status:** Phase 2 — alpha. Currently ships `ferentin_edge_site`; remaining resources follow per the [design doc in `ferentin-cli-app`](https://github.com/ferentin-net/ferentin-cli-app/blob/main/docs/terraform_provider/ferentin_terraform_provider.md).

## Usage

```hcl
terraform {
  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

provider "ferentin" {
  endpoint  = "https://api.ferentin.net"
  tenant_id = "<tenant-uuid>"
  token     = var.ferentin_token  # pre-minted bearer; sensitive
}

resource "ferentin_edge_site" "us_east" {
  site_id   = "prod-us-east-1a"
  site_name = "US East 1A"
}
```

Every provider attribute has a `FERENTIN_*` env-var fallback (`FERENTIN_ENDPOINT`, `FERENTIN_TENANT_ID`, `FERENTIN_TOKEN`, `FERENTIN_INSECURE_SKIP_VERIFY`).

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md) for local dev setup. The provider depends on the SDK in [`ferentin-cli-app/pkg/adminapi`](https://github.com/ferentin-net/ferentin-cli-app/tree/main/pkg/adminapi); local builds use a `replace` directive pointing at `../ferentin-cli-app`.

## License

Mozilla Public License 2.0 — see [LICENSE](LICENSE).
