---
page_title: "Block risky tool calls from coding agents"
subcategory: "Guides"
description: |-
  Block the commands and tools coding agents should never run on managed devices, count the matches first, then deny them one device group at a time.
---

# Block risky tool calls from coding agents

Block the tool calls a coding agent should never make on a managed device, such as running `curl` or `wget`, or using its shell tool at all. Count the matches first, then deny them, one device group at a time. The policy is call-time [`ferentin_endpoint_mcp_rule`](../resources/endpoint_mcp_rule.md) rules, plus a [`ferentin_endpoint_policy_settings`](../resources/endpoint_policy_settings.md) row for the tenant and one for a pilot [`ferentin_device_group`](../resources/device_group.md).

The configuration is the [`endpoint-tool-call-guardrails`](https://github.com/ferentin-net/terraform-provider-ferentin/tree/main/examples/endpoint-tool-call-guardrails) example. [Govern MCP servers on managed endpoints](../guides/endpoint-mcp-servers.md) covers which MCP servers may run at all; this guide covers what agents do once they run.

## How tool call rules work

A tool call rule is decided **at call time**, through the hook Ferentin installs in each coding agent, rather than by governing the server the call came from. Its action is always `block`; `plan` refuses anything else.

Whether a match then **denies** the call or only **counts** it is `hook_rule_enforcement` on the posture of the device's scope:

| `hook_rule_enforcement` | What a matching `block` rule does |
| --- | --- |
| `shadow` | The match is counted, and the call runs. The default. |
| `enforce` | The call is denied before it runs. The developer sees *Blocked by Ferentin policy* and the rule name. |

Today `enforce` denies on **Claude Code** and **Cursor**. On Cursor only shell commands and MCP tool calls are checked before they run. Codex, Copilot and Antigravity calls are counted, not denied.

## What you build

| Priority | Rule | Matches | Scope |
| --- | --- | --- | --- |
| 10 | Block exfiltration commands | `command_binary`: `curl`, `wget`, `nc`, `scp` | every device |
| 20 | Block the shell tool | `tool_behavior`: `shell` | the pilot group |

| Posture row | `hook_rule_enforcement` |
| --- | --- |
| Tenant default | `shadow` |
| Pilot group override | `shadow`, then `enforce` |

## Before you start

- Devices enrolled and running the Ferentin endpoint agent, with its hook in place for the coding agents your developers use. The console's endpoint policy **Posture** tab shows how many agents are enforcing hooks.
- A provider configured for the tenant, with `policy:rw`, and `devices:groups:rw` for the device group. See the [provider overview](../index.md).

## 1. Block commands

```terraform
resource "ferentin_endpoint_mcp_rule" "block_exfil_commands" {
  name         = "Block exfiltration commands from AI agents"
  priority     = 10
  match_kind   = "command_binary"
  match_values = ["curl", "wget", "nc", "scp"]
  action       = "block"
}
```

A `command_binary` rule stops one command whichever agent runs it, however the line spells it: `curl`, `/usr/bin/curl`, `sudo curl`, `cd /tmp && curl`. macOS devices ignore case in command names; Linux devices do not.

To stop one named tool instead, use `match_kind = "tool_name"` with the name the agent reports, exactly and case-sensitively, such as `Bash`, or an MCP tool such as `create_issue`. Agents that name MCP tools `mcp__server__tool` match on the tool part; agents that name tools flat need the full name.

## 2. Block a behaviour

```terraform
resource "ferentin_endpoint_mcp_rule" "block_shell_pilot" {
  name             = "Block the shell tool on ai-guardrails-pilot"
  priority         = 20
  match_kind       = "tool_behavior"
  match_values     = ["shell"]
  action           = "block"
  device_group_ids = [ferentin_device_group.pilot.group_id]
}
```

A `tool_behavior` rule (`shell`, `file_read` or `file_write`) is resolved into the tool name each client actually sends for that behaviour, so one rule covers every client that can express it. **Prefer a behaviour when the intent is the behaviour**: clients spell one behaviour differently, and a single literal `tool_name` covers part of the fleet while reading as though it covers all of it.

~> A behaviour rule blocks **every** call to that tool. Blocking `shell` stops every command the agent runs, not one of them, which is why this example scopes it to the pilot group.

Narrowing a `block` by `device_group_ids` fails safe and is accepted. Leave `app_bundle_ids` and `criteria` off tool call rules: hook calls carry no app name, and devices cannot evaluate user conditions yet, so a tool call rule limited by either would save and never apply.

## 3. Count first

```terraform
resource "ferentin_endpoint_policy_settings" "default" {
  hook_rule_enforcement = "shadow"
  # ...the rest of the tenant posture
}
```

Apply, activate, and let the rules count. Each rule's drawer on the console's endpoint policy page shows **Would block**, and the Agent Execution Monitor ranks the commands. Change a rule until what it would block is what you meant to block.

## 4. Deny on the pilot group

The pilot group gets its own posture row, so it can deny while the rest of the tenant keeps counting:

```terraform
resource "ferentin_device_group" "pilot" {
  name   = "ai-guardrails-pilot"
  source = "manual"
}

resource "ferentin_endpoint_policy_settings" "pilot" {
  device_group_id = ferentin_device_group.pilot.group_id

  # Restate the tenant posture: a group row ignores the tenant default.
  unapproved_mcp_action      = "report_only"
  default_destination_action = "allow"
  mcp_gateway_url            = "https://acme.mcp.ferentin.net/v1"

  hook_rule_enforcement = "enforce"
}
```

~> **A group with its own posture row ignores the tenant default for every setting** (except unvetted local MCP tools, which only the tenant default sets). On a new row an attribute left out takes the platform's default, not the tenant's, and `unapproved_mcp_action` defaults to `quarantine`. The example shares the tenant's values through a `locals` block so the override differs only in `hook_rule_enforcement`.

Move it with a variable:

```sh
terraform apply -var pilot_hook_rule_enforcement=enforce
```

The platform **refuses the move to `enforce` (409) until every active device in the scope runs an agent build that can deny**, rather than claim enforcement it cannot confirm. The provider reports it as *fleet not ready for hook_rule_enforcement = "enforce"*. The console's Posture tab lists the devices holding the scope back. Update the ones on an older build, or retire the ones that never reported, then apply again. A row already on `enforce` saves normally.

## 5. Then the tenant

When the pilot looks right, move the tenant default:

```sh
terraform apply -var pilot_hook_rule_enforcement=enforce -var tenant_hook_rule_enforcement=enforce
```

The same readiness check applies to the whole tenant. Every apply writes every variable, so keep passing the pilot's value too, or set both in a `.tfvars` file.

## What `enforce` does not cover

- **Claude Code**: a managed settings source above the file tier, such as an MDM profile or server-managed settings, outranks the hook unless it is set to merge. If the hook fails or times out, Claude Code runs the tool.
- **Cursor**: only shell commands and MCP tool calls are checked before they run.
- **Codex, Copilot and Antigravity** are not enforced yet. Their calls are counted.
- A device whose breaker opens drops to report-only, and its rules stop denying until the breaker closes.
- Only `block` rules deny. `allow` and `steer` MCP rules behave the same in both modes.

## Apply, then activate

!> `terraform apply` **stages** the change. Devices pick up new rules and posture only after an operator activates the pending changes in the admin console. The console reports a scope as denying from what devices are running, never from what is saved: a scope saved as `enforce` keeps counting until it is activated.

## Sharp edges

**Already configured in the console?** Import it first: see [Bring an existing endpoint policy under Terraform](../guides/endpoint-adopt-existing.md).

**One configuration owns the tenant-default posture row.** Every endpoint example manages `ferentin_endpoint_policy_settings.default`; applying two to one tenant makes them overwrite each other. Merge `hook_rule_enforcement` into the configuration that already owns it.

**Destroying the tenant-default row does not reset it**, so a tenant on `enforce` stays on `enforce`. Apply `shadow` and activate before destroying. A group override is genuinely deleted, and the group falls back to the tenant default.

## The complete configuration

```terraform
# Guardrails for coding agents on managed endpoints: block the tool calls you
# never want an agent to make, count them first, then deny them, one device
# group at a time.
#
# Tool call rules are decided at call time, through the agent hook, and their
# action is always `block`. Whether a match DENIES the call or only COUNTS it
# is `hook_rule_enforcement` on the posture of the device's scope:
#
#   shadow  — count what the rules would deny; the call runs (the default)
#   enforce — deny the call before it runs (Claude Code and Cursor today)
#
# The rules, first match wins by ascending priority:
#
#   10  block  exfiltration commands (curl, wget, …)   every device
#   20  block  the shell tool                            pilot group
#
# The pilot group gets its own posture row, so it can move to `enforce` while
# the rest of the tenant keeps counting.
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
  # Needs `policy:rw` for the rules and posture, and `devices:groups:rw` for
  # the device group.
  profile = var.profile
}

resource "ferentin_device_group" "pilot" {
  name        = var.pilot_group_name
  description = "Devices that move to enforce first"
  source      = "manual"
}

# 10. Commands an agent should never run, whichever agent runs them. Matched
#     however the line spells them: `curl`, `/usr/bin/curl`, `sudo curl`,
#     `cd /tmp && curl`.
resource "ferentin_endpoint_mcp_rule" "block_exfil_commands" {
  name         = "Block exfiltration commands from AI agents"
  priority     = 10
  match_kind   = "command_binary"
  match_values = var.blocked_commands
  action       = "block"
}

# 20. The agent's whole shell tool, for the pilot group only. A behaviour is
#     resolved into the tool name each client actually sends, so one rule
#     covers every client that can express it. Narrowing a block by device
#     group fails safe, so it is accepted.
resource "ferentin_endpoint_mcp_rule" "block_shell_pilot" {
  name             = "Block the shell tool on ${var.pilot_group_name}"
  priority         = 20
  match_kind       = "tool_behavior"
  match_values     = ["shell"]
  action           = "block"
  device_group_ids = [ferentin_device_group.pilot.group_id]
}

locals {
  # A group with its own posture row ignores the tenant default for every
  # setting, so the override restates the tenant posture and differs only in
  # hook_rule_enforcement. On a new row, an attribute left out takes the
  # platform's default (unapproved_mcp_action = quarantine), not the tenant's.
  shared_posture = {
    unapproved_mcp_action      = var.unapproved_mcp_action
    default_destination_action = var.default_destination_action
    mcp_gateway_url            = var.mcp_gateway_url
  }
}

# The tenant-default posture row. Only one Terraform configuration should own
# it: if another config already manages it, set hook_rule_enforcement there
# instead of applying both.
resource "ferentin_endpoint_policy_settings" "default" {
  unapproved_mcp_action      = local.shared_posture.unapproved_mcp_action
  default_destination_action = local.shared_posture.default_destination_action
  mcp_gateway_url            = local.shared_posture.mcp_gateway_url

  hook_rule_enforcement = var.tenant_hook_rule_enforcement
}

# The pilot group's override. Moving it to `enforce` is refused (409) until
# every active device in the group runs an agent build that can deny.
resource "ferentin_endpoint_policy_settings" "pilot" {
  device_group_id = ferentin_device_group.pilot.group_id

  unapproved_mcp_action      = local.shared_posture.unapproved_mcp_action
  default_destination_action = local.shared_posture.default_destination_action
  mcp_gateway_url            = local.shared_posture.mcp_gateway_url

  hook_rule_enforcement = var.pilot_hook_rule_enforcement
}

output "enforcement" {
  description = "What a matching tool call rule does in each scope, as applied (devices follow after activation)."
  value = {
    tenant                 = ferentin_endpoint_policy_settings.default.hook_rule_enforcement
    (var.pilot_group_name) = ferentin_endpoint_policy_settings.pilot.hook_rule_enforcement
  }
}

output "rule_enforcement" {
  description = "Whether the fleet enforces each rule (active, pending, not_enforced, never_matched), as of the last refresh."
  value = {
    (ferentin_endpoint_mcp_rule.block_exfil_commands.name) = ferentin_endpoint_mcp_rule.block_exfil_commands.enforcement
    (ferentin_endpoint_mcp_rule.block_shell_pilot.name)    = ferentin_endpoint_mcp_rule.block_shell_pilot.enforcement
  }
}
```

The variables, with their validation, are in the example's [`variables.tf`](https://github.com/ferentin-net/terraform-provider-ferentin/blob/main/examples/endpoint-tool-call-guardrails/variables.tf).
