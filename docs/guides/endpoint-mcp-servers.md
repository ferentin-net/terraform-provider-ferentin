---
page_title: "Govern MCP servers on managed endpoints"
subcategory: "Guides"
description: |-
  Steer sanctioned remote MCP servers through the tenant's MCP gateway, block unvetted remote servers and local packages, and decide what happens to every other MCP server config on a managed device.
---

# Govern MCP servers on managed endpoints

Steer the remote MCP servers you have sanctioned through your tenant's MCP gateway, block the ones you have not, approve the local servers your developers run and block known-bad ones, and decide what happens to every other MCP server a developer configures. The policy is [`ferentin_mcp_policy`](../resources/mcp_policy.md) policies that name device targets, a [`ferentin_mcp_route`](../resources/mcp_route.md), a [`ferentin_mcp_local_server`](../resources/mcp_local_server.md) approval, the tenant's [`ferentin_mcp_scope_defaults`](../resources/mcp_scope_defaults.md), and the tenant-default [`ferentin_endpoint_policy_settings`](../resources/endpoint_policy_settings.md) row.

The configuration is the [`endpoint-mcp-servers`](https://github.com/ferentin-net/terraform-provider-ferentin/tree/main/examples/endpoint-mcp-servers) example. It pairs with [Approve AI apps on managed endpoints](../guides/endpoint-approved-ai.md), which covers network destinations, and [Block risky tool calls from coding agents](../guides/endpoint-tool-calls.md), which covers what agents do once a server runs.

## What you build

One MCP policy model covers remote and local servers. On the device, a policy selects servers by its `targets`, and a deny is decided before any allow:

| Resource | Decision | Selects | Effect |
| --- | --- | --- | --- |
| `ferentin_mcp_policy` | `deny` | remote host `mcp.unvetted.example`, local package `npm:@example/unvetted-mcp` | the remote server is blocked; the local entry becomes a refusal that explains the block |
| `ferentin_mcp_policy` + `ferentin_mcp_route` | `allow`, routed | remote host `api.githubcopilot.com` | the developer's entry is rewritten to the tenant's gateway route for the server |
| `ferentin_mcp_local_server` | approved | `uv run --directory * okta-mcp-server` | the entry is wrapped in `ferentin-mcp` and runs governed |
| `ferentin_mcp_scope_defaults` | `server_approval` | any other MCP server config that is not approved | `report_only`, until you choose `quarantine` or `block` |

## Before you start

- Devices enrolled and running the Ferentin endpoint agent **1.5.8 or later**. The agent package installs `ferentin-mcp`, and every local entry the agent rewrites runs it. Earlier agents wrapped local entries in the Ferentin CLI's `ferentin mcp local` instead, so a Mac on one of them without the CLI gets entries that cannot start. On upgrade, the agent rewrites those entries to `ferentin-mcp`.
- For each remote server you steer: an **enabled MCP server in the tenant**, from [`ferentin_mcp_server`](../resources/mcp_server.md), [`ferentin_mcp_server_from_card`](../resources/mcp_server_from_card.md) or the admin console, and an [`ferentin_mcp_policy`](../resources/mcp_policy.md) that grants it to the people who use it. You need its `slug`.
- A provider configured for the tenant, with the `policy:rw` scope. An MCP policy that names device targets needs `policy:rw`, not only `mcp:policies:rw`. See the [provider overview](../index.md).

## What the agent does with each entry

The endpoint agent reads every MCP client configuration on the Mac and rewrites each entry to match your policy. It re-reads them on every reconcile pass and after every policy change.

| Your decision | Remote server | Local (stdio) server |
| --- | --- | --- |
| MCP policy `deny` | Blocked | Replaced by a refusal entry. It explains the block and offers an appeal. |
| MCP route | Rewritten to the gateway route, so every call crosses the gateway and its MCP policies | Wrapped in `ferentin-mcp`, which still refuses the server until it is approved (below) |
| MCP policy `allow` | Left as written | Left as written: it runs, and **nothing sees its tool calls** |
| No policy, not approved | `server_approval` | `server_approval` |

Nothing is deleted. The agent keeps the developer's original entry in a root-owned backup and puts it back when the block is lifted or the agent is removed.

## 1. Block unvetted servers

```terraform
resource "ferentin_mcp_policy" "block_unvetted" {
  name     = "Block unvetted MCP servers"
  priority = 10

  targets = [
    { kind = "host", values = ["mcp.unvetted.example"] },
    { kind = "package", values = ["npm:@example/unvetted-mcp"] },
  ]

  effect = {
    type    = "deny"
    message = "This MCP server has not been vetted. Ask the security team for a review."
  }
}
```

A policy's targets are a union: it denies whatever any of them selects. A `host` value is a bare hostname, with no scheme, port or path; `plan` refuses anything else. To match part of a URL, use `kind = "url_prefix"`. A `host` or `url_prefix` target also reaches the device network filter as a destination rule.

A `package` value is one exact registry-qualified id: `npm:@scope/name`, `pypi:name` or `oci:…`, with no wildcards. A pinned npm value ends in a version, such as `npm:@acme/server@1.2.3`. To block a git, file or URL source, use an `argv_contains` target.

`effect.message` is what the developer sees on every surface the policy refuses on. `mode = "report_only"` records what the policy would refuse and refuses nothing, so you can read the fleet's reports before enforcing.

## 2. Steer sanctioned remote servers through the gateway

```terraform
resource "ferentin_mcp_policy" "allow_github" {
  name     = "Allow github"
  priority = 20

  targets = [
    { kind = "host", values = ["api.githubcopilot.com"] },
  ]

  effect = {
    type = "allow"
  }
}

resource "ferentin_mcp_route" "steer_github" {
  name = "Steer github through the MCP gateway"

  target = {
    kind   = "host"
    values = ["api.githubcopilot.com"]
  }
  steer_to_url = "https://acme.mcp.ferentin.net/v1/mcp/github"
}
```

The allow lifts the `server_approval` default for the server where it starts: an allow with targets and no `provider_instances` applies at `admit`. The route rewrites the entry. A route never grants, so a route without the allow leaves the server to `server_approval`.

`steer_to_url` is the tenant's MCP gateway on the public edge, `https://<tenant-short-name>.mcp.ferentin.net/v1`, followed by `/mcp/<server-slug>` for an enabled MCP server. Leave it unset to use the scope's own gateway. The agent rewrites a developer's entry for any of the listed hosts to that route, so the call reaches the upstream server through the gateway, under its MCP policies and with the credentials the tenant holds. The console's Shadow AI view reports these entries as **Sanctioned**.

-> If you later remove the gateway route a server was steered to, the agent re-steers the entry to another approved instance of the same server, restores the developer's original URL when nothing withholds it, or blocks it.

An `app_bundle_id` target on a policy, or `app_bundle_ids` on a route, narrows it to an MCP **client**, such as Claude Desktop or Cursor, by bundle identifier. It never names the server, so a policy cannot have `app_bundle_id` targets alone.

## 3. Local servers: approve the ones you run, block the rest

```terraform
resource "ferentin_mcp_local_server" "okta" {
  display_name  = "Okta MCP"
  argv_template = ["uv", "run", "--directory", "*", "okta-mcp-server"]
}
```

[`ferentin_mcp_local_server`](../resources/mcp_local_server.md) approves a local server tenant-wide. `argv_template` is the command line the approval covers, as the client's config spells it; `*` matches exactly one argument, so this one approves `okta-mcp-server` from any checkout directory. Devices consult a template only for a command line that names no package. For a packaged server in the local MCP catalog, set `catalog_slug` instead, and the approval is keyed on the entry's packages. Its tools still start unvetted: `tool_vetting_risk_policy` (step 5) decides what developers get from them.

~> The template ships **verbatim** in the signed policy bundle to every managed device. Put `*` where a credential or anything private would go.


Known-bad local packages are denied by the policy in step 1.

To let a local server run, prefer **approving** it to writing an `allow` policy:

- **Approve it**, with `ferentin_mcp_local_server` or in the admin console. The agent wraps the entry in `ferentin-mcp`, so admission, tool filtering and every call are governed.
- **An MCP policy `allow`** with a `package` target puts the entry back exactly as written. The server runs outside Ferentin and nothing sees its tool calls.
- **A local route** keeps an entry wrapped, but `ferentin-mcp` still refuses the server until it is approved. Use one for a command line too long to approve on its own: the agent reads at most 64 arguments of an entry, so match the first 60 in a route, then approve the server.

~> `local_mcp_block_enabled = true` on a posture row turns off every local MCP server in that scope, approved ones included, and MCP policy allows on local targets stop having any effect. Remote servers are unaffected.

## 4. Decide what happens to everything else

```terraform
resource "ferentin_mcp_scope_defaults" "default" {
  server_approval = "report_only"
}

resource "ferentin_endpoint_policy_settings" "default" {
  mcp_gateway_url = "https://acme.mcp.ferentin.net/v1"
}
```

[`ferentin_mcp_scope_defaults`](../resources/mcp_scope_defaults.md) holds what a server meets when nothing applies to it, per tenant or device group. `server_approval` applies to an MCP server config that no policy decides and that is not approved:

| Value | What happens |
| --- | --- |
| `report_only` | Reported in inventory. Nothing on the machine changes. |
| `quarantine` | The entry is moved aside so the client cannot load it, and the developer can appeal. |
| `block` | The server does not start. |

Start at `report_only` and read what the fleet reports before moving to `quarantine`. Or rehearse it: `server_approval = "quarantine"` with `server_approval_mode = "report_only"` records what quarantining would move aside, and changes nothing on the machine.

`mcp_gateway_url` is where the agent points approved server configs. Leaving it out of the config **clears** it, and the agent stops rewriting MCP client configs.

## 5. Unvetted local tools

Approving a local server does not approve its tools. Each tool stays unvetted until an admin approves or denies it, and `tool_vetting_risk_policy` on `ferentin_mcp_scope_defaults` decides what developers get meanwhile, by the risk rating Ferentin gives each tool from its schema:

| Rating | Means |
| --- | --- |
| `low` | Reads only: no parameter names a file, URL, command or account. |
| `medium` | Touches files, the network or an account, or Ferentin cannot tell what it does. |
| `high` | Runs commands, writes or deletes, or tries to pull model context. |

```terraform
  tool_vetting_risk_policy = {
    low    = "allow"
    medium = "deny"
    high   = "deny"
  }
```

Each tier is `allow`, `ask_once`, `ask_every_call` or `deny`. A vetting decision outranks the rating, and `allow` applies only on servers an admin has approved.

A device group can set its own; one that sets none follows the tenant default's. An `ask_once` or `ask_every_call` tier needs approval routing first, set with `tool_vetting_approval_config` (on a device group, the tenant default's counts); without it the platform rejects the write. `tool_vetting_mode = "report_only"` rehearses the policy: every unvetted tool runs and the platform records what the policy would have answered.

## 6. Apply, then activate

```sh
terraform init
terraform plan
terraform apply
```

!> `terraform apply` **stages** the change. Devices pick up new policies, routes and posture only after an operator activates the pending changes in the admin console, and an activation ships every staged change in the tenant.

## Sharp edges

**Already configured in the console?** Import it first: see [Bring an existing endpoint policy under Terraform](../guides/endpoint-adopt-existing.md).

**One configuration owns the tenant-default posture row.** Every endpoint example manages `ferentin_endpoint_policy_settings.default`. Applying two of them to one tenant makes them overwrite each other on every apply. Merge the attributes into one configuration.

**Adopting a posture row resets what the config leaves out.** On a tenant whose posture was set in the console, the first apply adopts the row. The DNS/QUIC flags and `default_destination_action` take the provider's defaults, and `mcp_gateway_url` is cleared when left out. Read the first `plan`.

**A policy's `targets` are written in canonical spelling.** The platform lowercases hosts and canonicalizes URLs. Terraform keeps your spelling in `targets` and the platform's in `stored_targets`, so that is not drift.

## The complete configuration

```terraform
# Govern MCP servers on managed endpoints: steer the remote servers you have
# sanctioned through the tenant's MCP gateway, block the ones you have not,
# block known-bad local packages, and decide what happens to everything else.
#
# One MCP policy model covers remote and local servers. On a device, policies
# select servers by `targets`, and a deny is decided before any allow:
#
#   deny   unvetted remote MCP hosts and known-bad local (stdio) packages
#   allow  sanctioned remote servers, which a route also sends through the
#          tenant's MCP gateway
#
# Local servers your developers run are approved with ferentin_mcp_local_server,
# which keeps them governed through `ferentin-mcp`. MCP server configs no
# policy decides and nothing approves get the `server_approval` scope default.
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
  # Needs `policy:rw`: for MCP policies that name device targets, the routes
  # and the posture.
  profile = var.profile
}

# Remote hosts nobody has vetted and local packages you never want started.
# `host` values are bare hostnames: no scheme, port or path. A local deny
# replaces the developer's entry with a refusal that explains the block and
# offers an appeal; the original is kept and restored when the block is lifted.
resource "ferentin_mcp_policy" "block_unvetted" {
  name     = "Block unvetted MCP servers"
  priority = 10

  targets = [
    { kind = "host", values = var.blocked_remote_hosts },
    { kind = "package", values = var.blocked_local_packages },
  ]

  effect = {
    type    = "deny"
    message = "This MCP server has not been vetted. Ask the security team for a review."
  }
}

# Sanctioned remote servers. The allow lifts the server_approval default where
# the server starts (applies_at defaults to "admit" for an allow with targets
# and no provider instance), and the route rewrites a developer's MCP client
# entry for these hosts to the tenant's gateway route for the server, so every
# call crosses the gateway and its MCP policies. The route must belong to an
# enabled MCP server in the tenant (ferentin_mcp_server, or the admin console's
# MCP Servers registry).
resource "ferentin_mcp_policy" "allow_sanctioned_remote" {
  for_each = var.steered_remote_servers

  name     = "Allow ${each.key}"
  priority = 20

  targets = [
    { kind = "host", values = each.value.hosts },
  ]

  effect = {
    type = "allow"
  }
}

resource "ferentin_mcp_route" "steer_sanctioned_remote" {
  for_each = var.steered_remote_servers

  name = "Steer ${each.key} through the MCP gateway"

  target = {
    kind   = "host"
    values = each.value.hosts
  }

  # https://<tenant-short-name>.mcp.ferentin.net/v1/mcp/<server-slug>
  steer_to_url = "${var.mcp_gateway_url}/mcp/${each.value.server_slug}"
}

# Approved local (stdio) servers, by the command line each approval covers.
# `*` matches exactly one argument. Approving keeps the server governed through
# ferentin-mcp.
resource "ferentin_mcp_local_server" "approved" {
  for_each = var.approved_local_servers

  display_name  = each.value.display_name
  argv_template = each.value.argv_template
}

# The tenant-default posture row, which every device resolves through. Only one
# Terraform configuration should own it: if another config (such as
# ../endpoint-approved-ai) already manages it, move these attributes there
# instead of applying both.
resource "ferentin_endpoint_policy_settings" "default" {
  # Approved server configs are rewritten to point here. Leaving it null
  # CLEARS it, and the agent stops rewriting MCP client configs.
  mcp_gateway_url = var.mcp_gateway_url

  default_destination_action = var.default_destination_action
}

# The tenant default's MCP scope defaults, on the same posture row.
resource "ferentin_mcp_scope_defaults" "default" {
  # What happens to an MCP server config no policy decides and that is not
  # approved in the registry: report_only, quarantine or block.
  server_approval = var.server_approval

  # What developers get from a local MCP tool nobody has vetted, by its risk
  # rating. An ask_once / ask_every_call tier needs approval routing
  # (tool_vetting_approval_config) first.
  tool_vetting_risk_policy = var.tool_vetting_risk_policy
}

output "mcp_policies" {
  description = "The MCP policies this config writes, as priority, effect and name."
  value = sort([
    for p in concat(
      [ferentin_mcp_policy.block_unvetted],
      values(ferentin_mcp_policy.allow_sanctioned_remote),
    ) : format("%04d %-5s %s", p.priority, p.effect.type, p.name)
  ])
}

output "mcp_routes" {
  description = "Where each sanctioned remote server is steered."
  value       = { for k, r in ferentin_mcp_route.steer_sanctioned_remote : k => r.steer_to_url }
}
```

The variables, with their validation, are in the example's [`variables.tf`](https://github.com/ferentin-net/terraform-provider-ferentin/blob/main/examples/endpoint-mcp-servers/variables.tf).
