# Two MCP servers + one allow-all policy

Stand up two MCP servers from different sources and bind them to a single
[`ferentin_mcp_policy`](../../docs/resources/mcp_policy.md) that allows
access to every tool both servers expose.

## Layout

```
examples/mcp-multi-server/
├── README.md
├── main.tf            # catalog lookup + card import + policy
└── server-card.json   # the reference threat-intel server card
```

## What it shows

1. **Salesforce from the global catalog.** The
   [`ferentin_mcp_providers`](../../docs/data-sources/mcp_providers.md)
   data source lists the read-only global catalog; we key it by slug and
   reference the Salesforce row's `provider_id` from a
   [`ferentin_mcp_server`](../../docs/resources/mcp_server.md).

2. **Threat Intel from a discovered server-card.json.** Drives a
   [`ferentin_mcp_provider`](../../docs/resources/mcp_provider.md) +
   `ferentin_mcp_server` pair off the card. See
   [`examples/mcp-server-from-card/`](../mcp-server-from-card/) for the
   longer variant with conditional `cc_federated` workload-client wiring.

3. **One allow-all policy spanning both servers.** An `allow` effect with
   no `allowed_tools` / `grant_toolsets` grants access to everything the
   upstream MCPs expose. Narrow in production by setting either list or
   layering a higher-priority `deny`.

## Try it

```sh
terraform init
terraform plan
terraform apply
```

The default config uses the `prod` shared profile for auth — edit the
`provider` block to use whichever auth path fits your environment (see
[../../README.md](../../README.md) for options).

## Catalog-row not found?

`local.salesforce_catalog = local.catalog_by_slug["salesforce"]` errors if
the global catalog doesn't ship a `salesforce` row. List what *is*
available with:

```hcl
output "catalog_slugs" {
  value = [for p in data.ferentin_mcp_providers.catalog.providers : p.slug]
}
```

…then substitute whichever slug matches the upstream you actually want
to instantiate.
