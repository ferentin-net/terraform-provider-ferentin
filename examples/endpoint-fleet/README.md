# Endpoint policy for a managed fleet

Device groups + on-device destination rules + posture, staged from **observe**
to **enforce** with a single variable. This is the endpoint half of
[`../complete/`](../complete/) on its own, with the rollout mechanics spelled
out.

## Layout

```
examples/endpoint-fleet/
├── README.md
├── main.tf        # groups, the priority ladder, posture
└── variables.tf   # stage selector + the group / provider / host inputs
```

## The three moving parts

1. **[`ferentin_device_group`](../../docs/resources/device_group.md)** — the
   targeting dimension. Created with `for_each` over a map, so adding a
   population is a variable edit. `source` and `external_id` are immutable:
   changing either replaces the group, which re-scopes every rule pointed at
   it.

2. **[`ferentin_endpoint_destination_rule`](../../docs/resources/endpoint_destination_rule.md)**
   — allow / block / steer per destination, evaluated **on-device, first match
   wins by ascending `priority`**:

   | Priority | Action | Rule | Scope |
   | --- | --- | --- | --- |
   | 5 | `allow` | OpenAI for legal & compliance | engineering group, narrowed by criteria — **bypasses service-edge**, see below |
   | 10 | `block` | ChatGPT desktop app | contractors group, by bundle id |
   | 20 | `steer` | Anthropic → service-edge | fleet-wide |
   | 30 | `steer` | OpenAI → service-edge | fleet-wide |
   | 100 | `block` | Unsanctioned hosts | fleet-wide |

   Specific-and-restrictive sits above broad-and-permissive, because the first
   match ends evaluation.

3. **[`ferentin_endpoint_policy_settings`](../../docs/resources/endpoint_policy_settings.md)**
   — what happens when **no** rule matches, plus the unapproved-MCP action and
   the DNS/QUIC flags. One tenant-default row (no `device_group_id`) that every
   device including ungrouped ones resolves through, plus per-group overrides.

## Rolling it out

The rule set is identical at every stage. Only the posture — what happens to
traffic **no rule matched** — changes. The stage is not an enforcement kill
switch: the `block` and `steer` rules above enforce from the first apply, at
`observe` too. Set `enabled = false` on a rule to ship it inert.

```sh
terraform apply                                     # observe   (default)
terraform apply -var enforcement_stage=steer        # rewrite MCP configs to the gateway
terraform apply -var enforcement_stage=allowlist    # unmatched traffic is blocked
```

| Stage | `unapproved_mcp_action` | `default_destination_action` | `mcp_gateway_url` | DNS/QUIC flags |
| --- | --- | --- | --- | --- |
| `observe` | `report_only` | `allow` | unset | off |
| `steer` | `report_only` | `allow` | set | off |
| `allowlist` | `quarantine` | `block` | set | on |

Groups in `strict_groups` (contractors by default) get the strict override from
the first apply, regardless of stage — a useful way to pilot enforcement on the
population where the blast radius is smallest.

**Before selecting `allowlist`, confirm the rules cover every sanctioned
provider.** At that stage they become the allowlist; anything unmatched is
blocked on the device.

## Sharp edges

**An `allow` above a `steer` is a governance bypass.** First match ends
evaluation, so the priority-5 carve-out means that population's traffic never
reaches the priority-30 steer rule — no service-edge, and therefore no LLM
policy, no DLP, no telemetry on it. That is what a carve-out *is*, but it is
also the kind of exception that gets added for one team and outlives its
reason. If you want the exception governed rather than exempted, make it a
`steer` to a different service-edge URL instead of an `allow`.

**`priority` is policy semantics, not cosmetics.** The admin console's reorder
arrows write the same field. Don't split ownership of ordering between
Terraform and the console — the next plan shows drift and the two fight over
evaluation order.

**Criteria narrow a rule; removing them widens it.** Deleting the `criteria`
block from `allow_openai_legal` does not remove the carve-out — it turns a
department-scoped allow into an allow for everyone on those devices. The
provider warns on this, and the plan shows the removal. Read both.

**Criteria are not enforced on-device yet.** The agent ships them in the policy
bundle but will not match a rule carrying them until the on-device user
principal lands (platform#2014). That is fail-closed for `allow` and `steer` —
and it means a criteria-scoped `block` does not block.

**Destroying the tenant-default posture does not reset it.** The platform
refuses to delete a row every device resolves through, so Terraform drops it
from state and the fleet keeps enforcing the last applied posture. Removing the
resource block or dropping the module reaches the same code path. To stand
enforcement down, `terraform apply -var enforcement_stage=observe` first, then
destroy. Group overrides *are* genuinely deleted, and the group falls back to
the tenant default.

**Posture is an upsert.** Applying against a tenant whose posture was set in the
console **adopts** the existing row rather than failing. `managed_by` keeps
naming the original creator — that's what the `posture_provenance` output
surfaces, and `managed_by = "iac"` with `last_modified_by = "console"` means
somebody edited a Terraform-managed row by hand.

## Required scopes

`devices:groups:rw` (narrow — group CRUD only, held by the seeded
`ferentin.iac.operator` role) or the broad `devices:rw`, plus `policy:rw` for
the rules and posture. Prefer the narrow one: `devices:rw` also grants device
status transitions, per-serial certificate revocation, and forced
re-enrollment, which this pipeline has no business holding.

## Importing an existing fleet

Already configured in the console? Import instead of re-creating — especially
the tenant-default posture row, which cannot be cleanly destroyed afterwards.

```sh
terraform import 'ferentin_device_group.this["engineering"]'          <tenant_id>/<group_id>
terraform import ferentin_endpoint_destination_rule.block_unsanctioned <tenant_id>/<rule_id>
terraform import ferentin_endpoint_policy_settings.default             <tenant_id>
terraform import 'ferentin_endpoint_policy_settings.override["contractors"]' <tenant_id>/<device_group_id>
```
