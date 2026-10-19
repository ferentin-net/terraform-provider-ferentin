---
page_title: "Approve AI apps on managed endpoints"
subcategory: "Guides"
description: |-
  Allow the AI providers you sanction, pin one to its signed desktop app, and block the rest on every managed device, with four destination rules and the tenant-default posture.
---

# Approve AI apps on managed endpoints

Allow the AI providers you sanction, pin one of them to its signed desktop app, and block the rest, on every managed device in the tenant. The whole policy is four [`ferentin_endpoint_destination_rule`](../resources/endpoint_destination_rule.md) resources and the tenant-default [`ferentin_endpoint_policy_settings`](../resources/endpoint_policy_settings.md) row.

The configuration is the [`endpoint-approved-ai`](https://github.com/ferentin-net/terraform-provider-ferentin/tree/main/examples/endpoint-approved-ai) example. It uses no device groups and no MCP rules, so it is the place to start. The [`endpoint-fleet`](https://github.com/ferentin-net/terraform-provider-ferentin/tree/main/examples/endpoint-fleet) example builds on it with device groups, MCP rules, posture overrides and a staged rollout.

## What you build

Destination rules are evaluated **on the device**, in ascending `priority`, and the **first match wins**:

| Priority | Action | Rule | Matches |
| --- | --- | --- | --- |
| 10 | `allow` | Approved AI: Anthropic | `.anthropic.com`, `.claude.ai`, from any app |
| 20 | `allow` | Approved AI: ChatGPT desktop | `api.openai.com`, `.chatgpt.com`, only from the ChatGPT desktop app |
| 30 | `block` | Block OpenAI outside approved apps | the same OpenAI hosts, from every other app |
| 40 | `block` | Block unapproved AI | `.deepseek.com` |
| — | `default_destination_action` | When no rule matches | `allow`, until you choose `block` |

## Before you start

- Devices enrolled and running the Ferentin endpoint agent, with its content filter enabled. Destination rules are enforced by the filter; a device without it does not enforce them.
- A provider configured for the tenant, with the `policy:rw` scope. See the [provider overview](../index.md) for authentication.

## 1. Allow the sanctioned providers

```terraform
resource "ferentin_endpoint_destination_rule" "approved_anthropic" {
  name              = "Approved AI: Anthropic"
  priority          = 10
  action            = "allow"
  destination_kind  = "host"
  destination_hosts = [".anthropic.com", ".claude.ai"]
}
```

Destination rules `allow` or `block`. `steer` is not available on them yet: only MCP traffic is steered today, through the tenant's public edge.

A bare host (`api.openai.com`) matches that host alone. A leading dot (`.anthropic.com`) matches the domain and every host under it.

-> Write subdomain patterns as `.example.com`, not `*.example.com`. The two mean the same thing, but endpoint agents without ferentin-endpoint#1246 refuse the `*.` form, and one host an agent refuses makes it refuse the tenant's **whole** endpoint policy and keep its last-known-good one.

`destination_kind = "host"` matches exactly the hosts you list. To match a provider from the platform's AI catalog instead, so that a new provider host is covered without editing the rule, use `destination_kind = "ai_provider"` with a `catalog_slug`.

## 2. Pin OpenAI to the ChatGPT desktop app

Two rules on the same hosts: a narrow `allow` above a broad `block`.

```terraform
locals {
  openai_hosts = ["api.openai.com", ".chatgpt.com"]
}

resource "ferentin_endpoint_destination_rule" "approved_chatgpt_desktop" {
  name              = "Approved AI: ChatGPT desktop"
  priority          = 20
  action            = "allow"
  destination_kind  = "host"
  destination_hosts = local.openai_hosts
  app_team_ids      = ["2DC432GLL2"] # codesign -dv /Applications/ChatGPT.app
}

resource "ferentin_endpoint_destination_rule" "block_openai_elsewhere" {
  name              = "Block OpenAI outside approved apps"
  priority          = 30
  action            = "block"
  destination_kind  = "host"
  destination_hosts = local.openai_hosts
}
```

Traffic from the ChatGPT app matches rule 20 and is allowed. The same hosts reached from a browser, `curl` or any other app skip rule 20, because the app filter does not match, and land on rule 30.

`app_team_ids` matches the Apple Developer Team ID that signed the app, so a renamed or re-bundled copy signed by someone else does not match. Read it from the app itself:

```sh
codesign -dv /Applications/ChatGPT.app 2>&1 | grep TeamIdentifier
```

`app_bundle_ids` and `app_signing_ids` narrow by bundle and signing identifier the same way.

~> An empty app filter means **any app**. Leaving `app_team_ids` empty on rule 20 allows OpenAI from everything and makes rule 30 unreachable. The example's variable refuses an empty list for this reason.

## 3. Block the rest

```terraform
resource "ferentin_endpoint_destination_rule" "block_unapproved" {
  name              = "Block unapproved AI"
  priority          = 40
  action            = "block"
  destination_kind  = "host"
  destination_hosts = [".deepseek.com"]
}
```

Add hosts here as you find unsanctioned services. Keep blocks below the allows they would otherwise shadow: the first match ends evaluation.

## 4. Set the fallthrough

```terraform
resource "ferentin_endpoint_policy_settings" "default" {
  default_destination_action = "allow"
}
```

With `allow`, the rules act as a deny list with pinned exceptions. With `block`, they become an **allowlist**: anything they do not allow is blocked on the device, including traffic you did not expect. Try `block` on one machine before the fleet.

~> This resource is an **upsert**. On a tenant whose posture was configured in the console, `apply` adopts the existing row and writes the attributes in your config, along with the provider's defaults for attributes it leaves out: `false` for `ech_strip_enabled`, `doh_block_enabled` and `quic_block_enabled`, and an empty `mcp_gateway_url`. Read the first `plan` and copy any values you want to keep into the config. Attributes with no default, such as `unapproved_mcp_action` and `hook_rule_enforcement`, keep what the tenant has.

## 5. Apply, then activate

```sh
terraform init
terraform plan
terraform apply
```

!> `terraform apply` **stages** the change; it does not ship it. Devices pick up new rules and posture only after an operator activates the pending changes in the admin console. Until then the console shows the policy as changed, and the fleet keeps enforcing the previous one. An activation ships every staged change in the tenant, not only this one.

## 6. Verify on a device

From a managed Mac, after activation:

```sh
curl -sS -o /dev/null -w '%{http_code}\n' https://api.anthropic.com   # answers: rule 10
curl -sS -o /dev/null -w '%{http_code}\n' https://api.openai.com      # refused: rule 30, curl is not ChatGPT
```

The ChatGPT desktop app keeps working, through rule 20. Each rule's row in the console's endpoint policy page counts the devices it reached.

## Tightening

Two posture settings close the gaps this rule set leaves. Each is a variable in the example:

| Variable | Effect |
| --- | --- |
| `default_destination_action = "block"` | Unmatched AI destinations are blocked. The rules become the allowlist. |
| `unnamed_flow_block_enabled = true` | Port-443 flows to bare public IPs, which no host rule can match, are dropped. List VPN concentrators and other infrastructure reached by IP in `unnamed_flow_exempt_cidrs` first. |

Change one at a time, and activate after each apply.

## Removing it

`terraform destroy` deletes the four rules. It does **not** reset the tenant-default posture: the platform refuses to delete the row every device resolves through, so Terraform drops it from state and the fleet keeps the last applied posture. To leave the fleet permissive, apply `default_destination_action = "allow"` and `unnamed_flow_block_enabled = false` first, activate, then destroy.

## The complete configuration

```terraform
# Approved AI on managed endpoints: allow the sanctioned providers, pin one of
# them to its signed desktop app, and block the rest.
#
# Destination rules are evaluated ON-DEVICE, first match wins by ascending
# priority:
#
#   10  allow  Anthropic, from any app
#   20  allow  OpenAI, only from the ChatGPT desktop app (by Apple Team ID)
#   30  block  OpenAI from everything else (browsers, scripts, other apps)
#   40  block  DeepSeek
#
# Everything no rule matches falls through to the tenant-default posture's
# `default_destination_action` (allow, unless you set it to block).
#
# One tenant, no device groups and no MCP rules: this is the starting point.
# ../endpoint-fleet/ adds groups, MCP rules, posture overrides and a staged
# rollout.
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

locals {
  # One host list for the allow and the block that follows it, so the two
  # rules cannot drift apart. A bare host matches only itself; a leading dot
  # matches the domain and every host under it. Prefer ".example.com" to
  # "*.example.com": agents without ferentin-endpoint#1246 refuse the `*.`
  # form, and one refused host makes an agent refuse the whole policy.
  openai_hosts = ["api.openai.com", ".chatgpt.com"]
}

resource "ferentin_endpoint_destination_rule" "approved_anthropic" {
  name              = "Approved AI: Anthropic"
  priority          = 10
  action            = "allow"
  destination_kind  = "host"
  destination_hosts = [".anthropic.com", ".claude.ai"]
}

# The allow is narrowed to one signed app; the block below catches every other
# app reaching the same hosts. Swap the order and the block wins for everyone.
resource "ferentin_endpoint_destination_rule" "approved_chatgpt_desktop" {
  name              = "Approved AI: ChatGPT desktop"
  priority          = 20
  action            = "allow"
  destination_kind  = "host"
  destination_hosts = local.openai_hosts
  app_team_ids      = var.chatgpt_team_ids
}

resource "ferentin_endpoint_destination_rule" "block_openai_elsewhere" {
  name              = "Block OpenAI outside approved apps"
  priority          = 30
  action            = "block"
  destination_kind  = "host"
  destination_hosts = local.openai_hosts
}

resource "ferentin_endpoint_destination_rule" "block_unapproved" {
  name              = "Block unapproved AI"
  priority          = 40
  action            = "block"
  destination_kind  = "host"
  destination_hosts = var.blocked_hosts
}

# The tenant-default posture row, which every device (grouped or not) resolves
# through. This is an upsert: on a tenant that already has posture, apply
# ADOPTS the row instead of failing. Read the first plan before approving it,
# because the attributes below are written on every apply.
resource "ferentin_endpoint_policy_settings" "default" {
  default_destination_action = var.default_destination_action

  # Port-443 flows to bare public IPs have no hostname for a rule to match.
  # Off by default: on a working Mac it also catches VPNs and other
  # infrastructure reached by IP, so list those in the exemptions first.
  unnamed_flow_block_enabled = var.unnamed_flow_block_enabled
  unnamed_flow_exempt_cidrs  = var.unnamed_flow_exempt_cidrs

  # Leaving this null CLEARS a gateway URL the tenant already has, and the
  # agent stops rewriting MCP client configs. Set it to keep one.
  mcp_gateway_url = var.mcp_gateway_url
}

output "rule_ladder" {
  description = "Destination rules in evaluation order. First match wins, so read top down."
  value = sort([
    for r in [
      ferentin_endpoint_destination_rule.approved_anthropic,
      ferentin_endpoint_destination_rule.approved_chatgpt_desktop,
      ferentin_endpoint_destination_rule.block_openai_elsewhere,
      ferentin_endpoint_destination_rule.block_unapproved,
    ] : format("%04d %-5s %s", r.priority, r.action, r.name)
  ])
}

output "posture_provenance" {
  description = <<-EOT
    Provenance of the tenant-default posture row. `managed_by = "console"`
    means Terraform adopted a row first configured in the admin console;
    `last_modified_by = "console"` on a Terraform-managed row means someone
    edited it by hand since the last apply.
  EOT
  value = {
    managed_by       = ferentin_endpoint_policy_settings.default.managed_by
    last_modified_by = ferentin_endpoint_policy_settings.default.last_modified_by
    version          = ferentin_endpoint_policy_settings.default.version
  }
}
```

The variables, with their validation, are in the example's [`variables.tf`](https://github.com/ferentin-net/terraform-provider-ferentin/blob/main/examples/endpoint-approved-ai/variables.tf).
