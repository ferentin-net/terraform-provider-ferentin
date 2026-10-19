# Adopt an existing endpoint policy

Print an `import` block for every endpoint object Terraform does not manage
yet: device groups, destination rules, MCP rules, posture rows and AI-agent
decisions. It reads
from the tenant and writes nothing to it.

This is step 1 of the
[Bring an existing endpoint policy under Terraform](../../docs/guides/endpoint-adopt-existing.md)
guide.

## Running it

```sh
terraform init
terraform apply
terraform output -raw import_blocks > ../my-endpoint-policy/imports.tf
terraform output already_managed
```

`apply` only reads the five list data sources
([`ferentin_device_groups`](../../docs/data-sources/device_groups.md),
[`ferentin_endpoint_destination_rules`](../../docs/data-sources/endpoint_destination_rules.md),
[`ferentin_endpoint_mcp_rules`](../../docs/data-sources/endpoint_mcp_rules.md),
[`ferentin_endpoint_policy_settings_scopes`](../../docs/data-sources/endpoint_policy_settings_scopes.md),
[`ferentin_endpoint_agent_approvals`](../../docs/data-sources/endpoint_agent_approvals.md))
and records their outputs in local state.

Then, in the configuration that will own the policy:

```sh
terraform plan -generate-config-out=generated.tf
```

## Which objects it picks

An object gets an import block when:

- its `managed_by` is in `adopt_managed_by` (default `["console"]`), and
- its `last_modified_by` is not `iac`, unless `skip_last_written_by_terraform = false`.

The second rule matters because **`managed_by` never changes**. An object
created in the console and since imported by another configuration still
reads `managed_by = "console"`; that it was last written by Terraform is the
sign that a configuration owns it. `already_managed` lists everything skipped,
with both values, so you can check.

## Names

Each import is named after the object: lowercased, non-alphanumerics folded to
`_`, with a kind prefix (`g_`, `d_`, `m_`, `p_`) and the first 8 characters of
its ID, so two similar names cannot collide. An agent decision is named after
its catalog slug (`a_claude_code`), which is already unique. The tenant-default posture row is
`ferentin_endpoint_policy_settings.default`. Rename them in the owning
configuration before its first apply; afterwards a rename needs a `moved`
block.

## Required scopes

`policy:rw` and `devices:groups:rw`, to read rules, posture and groups.
