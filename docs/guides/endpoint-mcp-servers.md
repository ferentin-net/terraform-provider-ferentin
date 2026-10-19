---
page_title: "Govern MCP servers on managed endpoints"
subcategory: "Guides"
description: |-
  Steer sanctioned remote MCP servers through the tenant's MCP gateway, block unvetted remote servers and local packages, and decide what happens to every other MCP server config on a managed device.
---

# Govern MCP servers on managed endpoints

Steer the remote MCP servers you have sanctioned through your tenant's MCP gateway, block the ones you have not, block known-bad local packages, and decide what happens to every other MCP server a developer configures. The policy is three kinds of [`ferentin_endpoint_mcp_rule`](../resources/endpoint_mcp_rule.md) and the tenant-default [`ferentin_endpoint_policy_settings`](../resources/endpoint_policy_settings.md) row.

The configuration is the [`endpoint-mcp-servers`](https://github.com/ferentin-net/terraform-provider-ferentin/tree/main/examples/endpoint-mcp-servers) example. It pairs with [Approve AI apps on managed endpoints](../guides/endpoint-approved-ai.md), which covers network destinations, and [Block risky tool calls from coding agents](../guides/endpoint-tool-calls.md), which covers what agents do once a server runs.

## What you build

MCP rules are evaluated **on the device**, in ascending `priority`, and the **first match wins**:

| Priority | Action | Matches | Effect |
| --- | --- | --- | --- |
| 10 | `block` | remote host `mcp.unvetted.example` | the server is blocked |
| 20 | `steer` | remote host `api.githubcopilot.com` | the developer's entry is rewritten to the tenant's gateway route for the server |
| 30 | `block` | local package `npm:@example/unvetted-mcp` | the entry becomes a refusal that explains the block |
| — | `unapproved_mcp_action` | any other MCP server config that is not approved | `report_only`, until you choose `quarantine` or `block` |

## Before you start

- Devices enrolled and running the Ferentin endpoint agent, **with the Ferentin CLI package deployed alongside it**. Every local entry the agent rewrites runs `ferentin`, so a Mac without the CLI gets entries that cannot start.
- For each remote server you steer: an **enabled MCP server in the tenant**, from [`ferentin_mcp_server`](../resources/mcp_server.md), [`ferentin_mcp_server_from_card`](../resources/mcp_server_from_card.md) or the admin console, and an [`ferentin_mcp_policy`](../resources/mcp_policy.md) that grants it to the people who use it. You need its `slug`.
- A provider configured for the tenant, with the `policy:rw` scope. See the [provider overview](../index.md).

## What the agent does with each entry

The endpoint agent reads every MCP client configuration on the Mac and rewrites each entry to match your policy. It re-reads them on every reconcile pass and after every policy change.

| Your decision | Remote server | Local (stdio) server |
| --- | --- | --- |
| `block` rule | Blocked | Replaced by a refusal entry. It explains the block and offers an appeal. |
| `steer` rule | Rewritten to the gateway route, so every call crosses the gateway and its MCP policy | Wrapped in `ferentin mcp local`, which still refuses the server until it is approved (below) |
| `allow` rule | Left as written | Left as written: it runs, and **nothing sees its tool calls** |
| No rule, not approved | `unapproved_mcp_action` | `unapproved_mcp_action` |

Nothing is deleted. The agent keeps the developer's original entry in a root-owned backup and puts it back when the block is lifted or the agent is removed.

## 1. Block unvetted remote servers

```terraform
resource "ferentin_endpoint_mcp_rule" "block_unvetted_remote" {
  name         = "Block unvetted remote MCP servers"
  priority     = 10
  match_kind   = "host"
  match_values = ["mcp.unvetted.example"]
  action       = "block"
}
```

A `host` value is a bare hostname, with no scheme, port or path; `plan` refuses anything else. To match part of a URL, use `match_kind = "url_prefix"`.

## 2. Steer sanctioned remote servers through the gateway

```terraform
resource "ferentin_endpoint_mcp_rule" "steer_github" {
  name         = "Steer github through the MCP gateway"
  priority     = 20
  match_kind   = "host"
  match_values = ["api.githubcopilot.com"]
  action       = "steer"
  steer_to_url = "https://acme.mcp.ferentin.net/v1/mcp/github"
}
```

`steer_to_url` is the tenant's MCP gateway on the public edge, `https://<tenant-short-name>.mcp.ferentin.net/v1`, followed by `/mcp/<server-slug>` for an enabled MCP server. The agent rewrites a developer's entry for any of the listed hosts to that route, so the call reaches the upstream server through the gateway, under its MCP policy and with the credentials the tenant holds. The console's Shadow AI view reports these entries as **Sanctioned**.

-> If you later remove the gateway route a server was steered to, the agent re-steers the entry to another approved instance of the same server, restores the developer's original URL when nothing withholds it, or blocks it.

`app_bundle_ids` narrows a rule to an MCP **client**, such as Claude Desktop or Cursor, by bundle identifier. It never names the server.

## 3. Local servers: block, and approve rather than allow

```terraform
resource "ferentin_endpoint_mcp_rule" "block_local_packages" {
  name         = "Block known-bad local MCP packages"
  priority     = 30
  match_kind   = "package"
  match_values = ["npm:@example/unvetted-mcp"]
  action       = "block"
}
```

A `package` value is one exact registry-qualified id: `npm:@scope/name`, `pypi:name` or `oci:…`, with no wildcards. A pinned npm value ends in a version, such as `npm:@acme/server@1.2.3`. To block a git, file or URL source, match `argv_contains` instead.

To let a local server run, prefer **approving** it to writing an `allow` rule:

- **Approve it** under Registries → MCP Servers → Local servers in the admin console. The agent wraps the entry in `ferentin mcp local`, so admission, tool filtering and every call are governed. The provider does not manage local-server approval yet.
- **An `allow` rule** puts the entry back exactly as written. The server runs outside Ferentin and nothing sees its tool calls. A local `allow` is also tenant-wide for now: `plan` refuses `device_group_ids` or `criteria` on it, because devices do not yet carry an authenticated identity when a local server is admitted.
- **A local `steer` rule** keeps an entry wrapped, but `ferentin mcp local` still refuses the server until it is approved. Use one for a command line too long to approve on its own: the agent reads at most 64 arguments of an entry, so name the first 60 in a steer rule, then approve the server.

~> `local_mcp_block_enabled = true` on a posture row turns off every local MCP server in that scope, approved ones included, and `allow` rules on `package` and `argv_contains` stop having any effect. Remote servers are unaffected.

## 4. Decide what happens to everything else

```terraform
resource "ferentin_endpoint_policy_settings" "default" {
  unapproved_mcp_action = "report_only"
  mcp_gateway_url       = "https://acme.mcp.ferentin.net/v1"
}
```

`unapproved_mcp_action` applies to an MCP server config that no rule matches and that is not approved:

| Value | What happens |
| --- | --- |
| `report_only` | Reported in inventory. Nothing on the machine changes. |
| `quarantine` | The entry is moved aside so the client cannot load it, and the developer can appeal. |
| `block` | The server does not start. |

Start at `report_only` and read what the fleet reports before moving to `quarantine`.

`mcp_gateway_url` is where the agent points approved server configs. Leaving it out of the config **clears** it, and the agent stops rewriting MCP client configs.

## 5. Unvetted local tools

Approving a local server does not approve its tools. Each tool stays unvetted until an admin approves or denies it, and `local_mcp_risk_policy` decides what developers get meanwhile, by the risk rating Ferentin gives each tool from its schema:

| Rating | Means |
| --- | --- |
| `low` | Reads only: no parameter names a file, URL, command or account. |
| `medium` | Touches files, the network or an account, or Ferentin cannot tell what it does. |
| `high` | Runs commands, writes or deletes, or tries to pull model context. |

```terraform
  local_mcp_risk_policy = {
    low    = "allow"
    medium = "deny"
    high   = "deny"
  }
```

Each tier is `allow`, `ask_once`, `ask_every_call` or `deny`. A vetting decision outranks the rating, and `allow` applies only on servers an admin has approved.

~> `local_mcp_risk_policy` is **tenant default only**: `plan` refuses it on a device-group row. An `ask_once` or `ask_every_call` tier needs approval routing first, set with `local_mcp_approval_config`; without it the platform rejects the write.

## 6. Apply, then activate

```sh
terraform init
terraform plan
terraform apply
```

!> `terraform apply` **stages** the change. Devices pick up new rules and posture only after an operator activates the pending changes in the admin console, and an activation ships every staged change in the tenant.

After activation, the example's `rule_enforcement` output reports whether the fleet enforces each rule: `active`, `pending`, `not_enforced` or `never_matched`. The platform computes it from device reports, so it can change without an apply. Refresh to read it again.

## Sharp edges

**One configuration owns the tenant-default posture row.** Every endpoint example manages `ferentin_endpoint_policy_settings.default`. Applying two of them to one tenant makes them overwrite each other on every apply. Merge the attributes into one configuration.

**Adopting a posture row resets what the config leaves out.** On a tenant whose posture was set in the console, the first apply adopts the row. The DNS/QUIC flags and `default_destination_action` take the provider's defaults, and `mcp_gateway_url` is cleared when left out. Read the first `plan`.

**`priority` belongs to Terraform.** The console's reorder arrows are disabled on Terraform-managed rules.

## The complete configuration

```terraform
# Govern MCP servers on managed endpoints: steer the remote servers you have
# sanctioned through the tenant's MCP gateway, block the ones you have not,
# block known-bad local packages, and decide what happens to everything else.
#
# MCP rules are evaluated ON-DEVICE, first match wins by ascending priority:
#
#   10  block  unvetted remote MCP hosts
#   2x  steer  sanctioned remote servers → the tenant's MCP gateway route
#   30  block  known-bad local (stdio) packages
#
# MCP server configs no rule matches get the posture's `unapproved_mcp_action`.
#
# `terraform apply` STAGES the change. Devices pick it up only after someone
# activates the pending changes in the admin console.

terraform {
  required_version = ">= 1.11"

  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

provider "ferentin" {
  # endpoint + tenant_id auto-default. See the root README for auth options.
  # Needs `policy:rw` for the rules and the posture.
  profile = var.profile
}

# 10. Remote MCP servers nobody has vetted. `host` values are bare hostnames:
#     no scheme, port or path.
resource "ferentin_endpoint_mcp_rule" "block_unvetted_remote" {
  name         = "Block unvetted remote MCP servers"
  priority     = 10
  match_kind   = "host"
  match_values = var.blocked_remote_hosts
  action       = "block"
}

# 2x. Sanctioned remote servers. The agent rewrites a developer's MCP client
#     entry for these hosts to the tenant's gateway route for the server, so
#     every call crosses the gateway and its MCP policy. The route must belong
#     to an enabled MCP server in the tenant (ferentin_mcp_server, or the
#     admin console's MCP Servers registry).
resource "ferentin_endpoint_mcp_rule" "steer_sanctioned_remote" {
  for_each = var.steered_remote_servers

  name         = "Steer ${each.key} through the MCP gateway"
  priority     = each.value.priority
  match_kind   = "host"
  match_values = each.value.hosts
  action       = "steer"

  # https://<tenant-short-name>.mcp.ferentin.net/v1/mcp/<server-slug>
  steer_to_url = "${var.mcp_gateway_url}/mcp/${each.value.server_slug}"
}

# 30. Local (stdio) packages you never want started. A local block replaces
#     the developer's entry with a refusal that explains the block and offers
#     an appeal; the original is kept and restored when the block is lifted.
resource "ferentin_endpoint_mcp_rule" "block_local_packages" {
  name         = "Block known-bad local MCP packages"
  priority     = 30
  match_kind   = "package"
  match_values = var.blocked_local_packages
  action       = "block"
}

# The tenant-default posture row, which every device resolves through. Only one
# Terraform configuration should own it: if another config (such as
# ../endpoint-approved-ai) already manages it, move these attributes there
# instead of applying both.
resource "ferentin_endpoint_policy_settings" "default" {
  # What happens to an MCP server config no rule matches and that is not
  # approved in the registry: report_only, quarantine or block.
  unapproved_mcp_action = var.unapproved_mcp_action

  # Approved server configs are rewritten to point here. Leaving it null
  # CLEARS it, and the agent stops rewriting MCP client configs.
  mcp_gateway_url = var.mcp_gateway_url

  # What developers get from a local MCP tool nobody has vetted, by its risk
  # rating. Tenant default only. An ask_once / ask_every_call tier needs
  # approval routing (local_mcp_approval_config) first.
  local_mcp_risk_policy = var.local_mcp_risk_policy

  default_destination_action = var.default_destination_action
}

output "mcp_rule_ladder" {
  description = "MCP rules in evaluation order. First match wins, so read top down."
  value = sort([
    for r in concat(
      [
        ferentin_endpoint_mcp_rule.block_unvetted_remote,
        ferentin_endpoint_mcp_rule.block_local_packages,
      ],
      values(ferentin_endpoint_mcp_rule.steer_sanctioned_remote),
    ) : format("%04d %-5s %s", r.priority, r.action, r.name)
  ])
}

output "rule_enforcement" {
  description = "Whether the fleet enforces each rule (active, pending, not_enforced, never_matched), as of the last refresh."
  value = merge(
    {
      (ferentin_endpoint_mcp_rule.block_unvetted_remote.name) = ferentin_endpoint_mcp_rule.block_unvetted_remote.enforcement
      (ferentin_endpoint_mcp_rule.block_local_packages.name)  = ferentin_endpoint_mcp_rule.block_local_packages.enforcement
    },
    { for r in ferentin_endpoint_mcp_rule.steer_sanctioned_remote : r.name => r.enforcement },
  )
}
```

The variables, with their validation, are in the example's [`variables.tf`](https://github.com/ferentin-net/terraform-provider-ferentin/blob/main/examples/endpoint-mcp-servers/variables.tf).
