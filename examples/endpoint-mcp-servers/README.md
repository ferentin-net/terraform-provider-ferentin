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
├── main.tf        # the MCP rule ladder, the tenant-default posture, two outputs
└── variables.tf   # gateway URL, steered servers, blocked hosts and packages, posture
```

## The rule ladder

[`ferentin_endpoint_mcp_rule`](../../docs/resources/endpoint_mcp_rule.md)
rules are evaluated **on the device, first match wins by ascending
`priority`**:

| Priority | Action | Matches | Variable |
| --- | --- | --- | --- |
| 10 | `block` | remote hosts | `blocked_remote_hosts` |
| 2x | `steer` | remote hosts → `<mcp_gateway_url>/mcp/<server_slug>` | `steered_remote_servers` |
| 30 | `block` | local packages | `blocked_local_packages` |

Everything else that is not approved gets `unapproved_mcp_action`
(`report_only` by default).

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
stages them. After activation, the `rule_enforcement` output says whether the
fleet enforces each rule; refresh to read it again.

When the inventory looks right, tighten the fallthrough:

```sh
terraform apply -var unapproved_mcp_action=quarantine
```

## Sharp edges

**A local `allow` runs the server outside Ferentin.** Nothing sees its tool
calls. To let a local server run and keep it governed, approve it under
Registries → MCP Servers → Local servers in the admin console (the provider
does not manage local-server approval yet). A local `steer` keeps an entry
wrapped, but the server is still refused until it is approved.

**One configuration owns the tenant-default posture row.** If another config
(such as [`../endpoint-approved-ai/`](../endpoint-approved-ai/)) already
manages `ferentin_endpoint_policy_settings.default`, merge these attributes
into it instead of applying both: the two would overwrite each other.

**`mcp_gateway_url` left null clears it**, and the agent stops rewriting MCP
client configs.

**`local_mcp_risk_policy` is tenant default only**, and an `ask_once` /
`ask_every_call` tier needs approval routing (`local_mcp_approval_config`)
first.

## Required scopes

`policy:rw` for the rules and the posture.
