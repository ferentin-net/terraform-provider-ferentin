# MCP server from a server-card.json

Configure a `ferentin_mcp_provider` + `ferentin_mcp_server` pair from a
discovered [MCP server-card](https://modelcontextprotocol.io). The card is
the canonical, machine-readable description of an MCP server — identity,
transport, credentials, capabilities — so driving Terraform off it keeps
the two in sync without copy-paste.

## Layout

```
examples/mcp-server-from-card/
├── README.md
├── main.tf            # parses server-card.json, stamps out provider + server
└── server-card.json   # the reference threat-intel server card
```

## How it works

1. **Read the card.** `jsondecode(file("./server-card.json"))` gives a single
   source of truth. Replace the file path with the location of your card.
2. **Map MCP-spec fields → Ferentin fields.** Lookup maps handle the two
   convention deltas:
   - Transport: spec uses `streamable-http`, the API uses `streamable_http`.
   - Auth: the card's `_meta.net.ferentin.transport.auth_type` maps to
     Ferentin's `upstream_auth_strategy` enum:

     | Card `auth_type`               | Ferentin `upstream_auth_strategy` |
     | ------------------------------ | --------------------------------- |
     | `none`                         | `none`                            |
     | `bearer`                       | `static_bearer`                   |
     | `oauth2_client_credentials`    | `cc_federated`                    |
     | `oauth2_authorization_code`    | `oauth2_user`                     |

   When the resolved strategy is `cc_federated`, the example conditionally
   provisions a `ferentin_workload_oauth_client` (`count = 0`/`1`) and wires
   its UUID into the MCP server's `cc_federated_workload_client_id`. The
   IdP coordinates (issuer, JWKS, token endpoint, client_id, client_secret)
   are tenant-side concerns the operator supplies via the variables at the
   bottom of `main.tf` — the card doesn't and shouldn't embed them.
3. **Publish the catalog entry.** `ferentin_mcp_provider` records the
   server's identity in the tenant catalog (title, slug, description,
   transport, default endpoint).
4. **Bind the instance.** `ferentin_mcp_server` ties that catalog entry
   to a specific URL — usually the same `card.remotes[0].url`, but you'd
   point at a different per-region endpoint when running multiple
   instances of the same logical server.

## Rotation

When the upstream server bumps a version or adds a tool, run `ferentin mcp
discover <url> > server-card.json` to refresh, then `terraform apply`.
Any fields driven from the card flow through automatically. Fields not in
the card (`enabled`, `priority`, `edge_site_id`, …) keep their HCL values.

## Try it

```sh
terraform init
terraform plan
terraform apply
```

The default config in `main.tf` uses the `prod` shared profile for auth —
edit the `provider` block to use whichever auth path fits your environment
(see [../../README.md](../../README.md) for options).
