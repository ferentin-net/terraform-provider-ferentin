# MCP server from a server-card.json

Stamp out a tenant-custom `ferentin_mcp_provider` + `ferentin_mcp_server`
pair from a discovered [MCP server-card](https://modelcontextprotocol.io)
in a single resource. The card is the canonical, machine-readable
description of an MCP server — identity, transport, credentials,
capabilities — so driving Terraform off it keeps the two in sync without
copy-paste.

## Layout

```
examples/mcp-server-from-card/
├── README.md
├── main.tf            # the resource block + edge_site + outputs
└── server-card.json   # the reference threat-intel server card
```

## How it works

[`ferentin_mcp_server_from_card`](../../docs/resources/mcp_server_from_card.md)
wraps the platform's
`POST /admin/tenants/{tenantId}/mcp-providers/import-server-card`
endpoint:

1. Operator commits the card JSON next to the Terraform.
2. The platform parses the card server-side, maps the MCP-spec transport /
   auth-type fields to Ferentin's `transport_type` /
   `upstream_auth_strategy` enums, creates the provider catalog entry,
   and binds an instance.
3. The resource owns BOTH the provider entry and the instance binding as
   one lifecycle unit — `terraform destroy` removes both.

Re-applying with the same card is cheap: the platform computes a
checksum over the raw card bytes, compares with the previous import, and
returns `action = "unchanged"` with zero writes. Bumping the card (new
version, new auth shape, new tools) propagates on the next apply and the
`import_result.tools_added` / `tools_updated` / `tools_removed` outputs
tell CI what changed.

## When NOT to use this resource

When you need to override fields the card can't express — custom slug,
different `transport_type`, more than one instance per provider — drop
to the standalone `ferentin_mcp_provider` + `ferentin_mcp_server`
resources. The `ferentin_mcp_server_from_card` resource is the
shortest-path for the common workflow; not a general replacement.

## Credentials

The card declares the credential field names the upstream expects under
`_meta.net.ferentin.curation.credential_fields[]`. Pass them via the
resource's `env` map:

```hcl
env = {
  BEARER_TOKEN = var.threat_intel_bearer_token
}
```

`env` is marked sensitive and never appears in plan output.

## Try it

```sh
export TF_VAR_threat_intel_bearer_token=dev-secret
terraform init
terraform plan
terraform apply
```

The default config in `main.tf` uses the `prod` shared profile for
auth — edit the `provider` block to use whichever auth path fits your
environment (see [../../README.md](../../README.md) for options).
