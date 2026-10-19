---
page_title: "Bring an existing endpoint policy under Terraform"
subcategory: "Guides"
description: |-
  Import device groups, destination rules, MCP rules, posture rows and AI-agent decisions configured in the admin console, so the first apply changes nothing on the fleet.
---

# Bring an existing endpoint policy under Terraform

Your tenant's endpoint policy was set up in the admin console, and you want Terraform to own it from here, without the first apply changing what a single device enforces. The way there is `import` blocks, generated configuration, and a plan that says **0 to change**.

Importing only reads from the platform. Nothing changes on the tenant, and nothing needs activating, until an apply that you have planned and read.

## Before you start

- Terraform 1.11 or later, and a provider configured for the tenant with `policy:rw` (rules and posture) and `devices:groups:rw` (device groups). See the [provider overview](../index.md).

## 1. List what exists, as import blocks

The [`endpoint-adopt-existing`](https://github.com/ferentin-net/terraform-provider-ferentin/tree/main/examples/endpoint-adopt-existing) example reads the five list data sources, [`ferentin_device_groups`](../data-sources/device_groups.md), [`ferentin_endpoint_destination_rules`](../data-sources/endpoint_destination_rules.md), [`ferentin_endpoint_mcp_rules`](../data-sources/endpoint_mcp_rules.md), [`ferentin_endpoint_policy_settings_scopes`](../data-sources/endpoint_policy_settings_scopes.md) and [`ferentin_endpoint_agent_approvals`](../data-sources/endpoint_agent_approvals.md), and prints an `import` block for each object to adopt:

```sh
cd endpoint-adopt-existing
terraform init
terraform apply                    # reads only; writes nothing to the tenant
terraform output -raw import_blocks > ../my-endpoint-policy/imports.tf
terraform output already_managed   # what it skipped, and why
```

```terraform
import {
  to = ferentin_device_group.g_engineering_e6c81c22
  id = "<tenant_id>/<group_id>"
}

import {
  to = ferentin_endpoint_destination_rule.d_allow_anthropic_3804043c
  id = "<tenant_id>/<rule_id>"
}

import {
  to = ferentin_endpoint_policy_settings.default
  id = "<tenant_id>"
}
```

By default it picks objects created in the console (`managed_by = "console"`) that Terraform has not written since. **`managed_by` never changes**, so an object created in the console and imported by another configuration still says `console`; its `last_modified_by = "iac"` is the sign that some configuration owns it, and the example skips it. Import only what no other configuration owns.

Every list item also carries `import_id`, so you can write the blocks by hand instead. The IDs take these forms:

| Resource | Import ID |
| --- | --- |
| [`ferentin_device_group`](../resources/device_group.md) | `<tenant_id>/<group_id>` |
| [`ferentin_endpoint_destination_rule`](../resources/endpoint_destination_rule.md) | `<tenant_id>/<rule_id>` |
| [`ferentin_endpoint_mcp_rule`](../resources/endpoint_mcp_rule.md) | `<tenant_id>/<rule_id>` |
| [`ferentin_endpoint_policy_settings`](../resources/endpoint_policy_settings.md), tenant default | `<tenant_id>` |
| `ferentin_endpoint_policy_settings`, group override | `<tenant_id>/<device_group_id>` |
| [`ferentin_endpoint_agent_approval`](../resources/endpoint_agent_approval.md) | `<tenant_id>/<catalog_slug>` |

-> Terraform does not generate configuration for an `import` block that uses `for_each`, which is why the example prints one block per object rather than importing from the data sources directly.

## 2. Put them in the owning configuration

Copy `imports.tf` into the directory of the configuration that will own the policy, next to its provider configuration.

**Import the posture rows, even though the resource is an upsert.** Without an import, the first apply adopts the existing row and writes your config over it, including the provider's defaults for attributes you left out. With an import, the plan shows every difference first.

## 3. Generate the configuration

```sh
terraform init
terraform plan -generate-config-out=generated.tf
```

Terraform writes a resource block for each import from what the platform holds, and the plan reads:

```
Plan: 5 to import, 0 to add, 0 to change, 0 to destroy.
```

The generated blocks are a faithful copy of the console's configuration, including the platform's normalized spelling of hosts and match values. That is why they plan cleanly.

## 4. Tidy it

Move the blocks into your configuration, then:

- **Delete `tenant_id`** from each block; it defaults to the provider's.
- **Delete the `= null` lines.** An unset attribute means the same thing.
- **Replace pasted UUIDs with references**, so a group and the rules that target it stay connected: `device_group_ids = [ferentin_device_group.engineering.group_id]`, and `device_group_id = ferentin_device_group.engineering.group_id` on a group's posture row.
- **Rename the resources** to something you will recognize: change the name in both the resource block and its `import` block's `to`. Renaming before the first apply is free; afterwards it needs a `moved` block.

Plan again after each change. The goal is still **0 to change**.

## 5. Apply the import

```sh
terraform apply
```

The apply records the objects in state. It writes nothing to the platform, so there is nothing to activate. Delete the `import` blocks afterwards, or keep them as a record; once the objects are in state, they do nothing.

## What a non-empty plan means

If you write the configuration by hand instead of generating it, or edit what was generated, the plan tells you where it differs from the tenant. These are the differences that matter:

| Plan shows | Because | Do |
| --- | --- | --- |
| `mcp_gateway_url` changing to null | The provider clears it when the config leaves it out | Copy the tenant's value into the config. Leaving it out stops devices rewriting MCP client configs. |
| `default_destination_action`, `ech_strip_enabled`, `doh_block_enabled` or `quic_block_enabled` changing | These have provider defaults (`allow`, `false`), written when the config leaves them out | Copy the tenant's values. |
| A device group **must be replaced** | `source` and `external_id` are immutable | Copy them exactly. Replacing a group re-scopes every rule that targets it. |
| `plan` fails on `posture_predicate` | The rule has a posture condition, set in the console, and the config omits it | Write `posture_predicate = { require_compliant = true }` to keep it. Leaving it out would widen the rule to non-compliant devices, so the provider refuses to do it silently. |
| `criteria`, `device_group_ids` or an app filter being removed, with a warning | Removing a narrowing **widens** the rule | Copy it, unless widening is what you meant. |
| `match_values` or `destination_hosts` changing spelling only | The platform stores a normalized form; your spelling is kept in Terraform | Applying changes nothing on the platform. Use the generated spelling to avoid the no-op update. |

Attributes with no provider default, such as `unapproved_mcp_action`, `hook_rule_enforcement` and `local_mcp_risk_policy`, keep the tenant's value when the config leaves them out.

## After adoption

**`managed_by` keeps saying `console`.** It records who created the object and never changes. After Terraform's first write, `last_modified_by` becomes `iac`, and the console marks the row as having changed hands. A later `last_modified_by = "console"` on a row Terraform manages means someone edited it by hand.

~> **The console still lets you reorder an imported rule.** The console locks a rule's reorder arrows only when Terraform *created* it (`managed_by = "iac"`). For an imported rule, the arrows still work, and a console reorder writes `priority`, which the next `plan` shows as drift. Agree that priority changes go through Terraform, or recreate the rule from Terraform.

**Every change from here is staged.** An apply that changes rules or posture saves them; devices pick them up after an operator activates the pending changes in the console.

**Destroying the tenant-default posture row does not reset it.** Terraform drops it from state and the fleet keeps the last applied posture. A group override is genuinely deleted, and the group falls back to the tenant default.

**One configuration owns each object.** In particular, keep the tenant-default posture row in one configuration: two configurations writing it overwrite each other on every apply.

## Next

- [Approve AI apps on managed endpoints](../guides/endpoint-approved-ai.md)
- [Govern MCP servers on managed endpoints](../guides/endpoint-mcp-servers.md)
- [Block risky tool calls from coding agents](../guides/endpoint-tool-calls.md)
