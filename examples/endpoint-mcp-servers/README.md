# Govern MCP servers on managed endpoints

Steer the remote MCP servers you have sanctioned through the tenant's MCP
gateway, block the ones you have not, block known-bad local packages, and
decide what happens to every other MCP server config on a managed device.

The walk-through, including how the agent treats each kind of entry, is the
[Govern MCP servers on managed endpoints](../../docs/guides/endpoint-mcp-servers.md)
guide.

## Layout

```
examples/endpoint-mcp-servers/
├── README.md
├── main.tf        # MCP policies and routes, approved local servers, the tenant-default posture, two outputs
└── variables.tf   # gateway URL, steered servers, blocked hosts and packages, posture
```

## What each server meets

[`ferentin_mcp_policy`](../../docs/resources/mcp_policy.md) is one policy
model for remote and local MCP servers. These policies select servers on the
device by `targets`, and a deny is decided before any allow:

| Policy | Effect | Targets | Variable |
| --- | --- | --- | --- |
| `block_unvetted` | `deny` | remote hosts and local packages | `blocked_remote_hosts`, `blocked_local_packages` |
| `allow_sanctioned_remote` | `allow` | remote hosts | `steered_remote_servers` |

Each sanctioned server also gets a
[`ferentin_mcp_route`](../../docs/resources/mcp_route.md), which rewrites the
developer's entry to `<mcp_gateway_url>/mcp/<server_slug>`. The allow lifts the
`server_approval` default for the server; the route sends it through the
gateway. A route never grants.

Local servers in `approved_local_servers` are approved with
[`ferentin_mcp_local_server`](../../docs/resources/mcp_local_server.md) and run
governed. Everything else that is not approved gets the `server_approval`
scope default (`report_only` by default), set on `ferentin_mcp_scope_defaults`.

## Running it

Each steered server must be an **enabled MCP server in the tenant**, with an
MCP policy that grants it. Set its slug in `steered_remote_servers`, and set
`mcp_gateway_url` to your tenant's `https://<tenant-short-name>.mcp.ferentin.net/v1`.

```sh
terraform init
terraform plan
terraform apply
```

Then **activate the pending changes in the admin console**: `apply` only
stages them.

When the inventory looks right, tighten the fallthrough:

```sh
terraform apply -var server_approval=quarantine
```

## Sharp edges

**An allow on a local package releases the server.** Like the endpoint MCP
rule allow it replaces, it puts the entry back as written, so the server runs
outside Ferentin and nothing sees its tool calls. To let a local server run and
keep it governed, approve it, as `approved_local_servers` does with
`ferentin_mcp_local_server`. A local route keeps an entry wrapped, but the
server is still refused until it is approved.

**One configuration owns the tenant-default posture row.** If another config
(such as [`../endpoint-approved-ai/`](../endpoint-approved-ai/)) already
manages `ferentin_endpoint_policy_settings.default`, merge these attributes
into it instead of applying both: the two would overwrite each other.

**`mcp_gateway_url` left null clears it**, and the agent stops rewriting MCP
client configs.

**An `ask_once` / `ask_every_call` tier in `tool_vetting_risk_policy` needs
approval routing** (`tool_vetting_approval_config`) first.

## Required scopes

`policy:rw` for the MCP policies (they name device targets), the routes and
the posture, and `mcp:servers:rw` for the local server approvals.
