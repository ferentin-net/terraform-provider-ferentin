# Endpoint policy for a managed fleet

Device groups + on-device destination rules + MCP rules + posture, staged from
**observe** to **enforce** with a single variable. This is the endpoint half of
[`../complete/`](../complete/) on its own, with the rollout mechanics spelled
out.

## Layout

```
examples/endpoint-fleet/
├── README.md
├── main.tf        # groups, the two rule ladders, agent decisions, posture
└── variables.tf   # stage + call-time enforcement selectors, group / provider / host inputs
```

## The five moving parts

1. **[`ferentin_device_group`](../../docs/resources/device_group.md)** — the
   targeting dimension. Created with `for_each` over a map, so adding a
   population is a variable edit. `source` and `external_id` are immutable:
   changing either replaces the group, which re-scopes every rule pointed at
   it.

2. **[`ferentin_endpoint_destination_rule`](../../docs/resources/endpoint_destination_rule.md)**
   — allow / block per destination, evaluated **on-device, first match
   wins by ascending `priority`**:

   | Priority | Action | Rule | Scope |
   | --- | --- | --- | --- |
   | 5 | `allow` | OpenAI for legal & compliance | engineering group, narrowed by criteria and to compliant devices (`posture_predicate`) |
   | 10 | `block` | ChatGPT desktop app | contractors group, by bundle id |
   | 20 | `allow` | Sanctioned providers (Anthropic) | fleet-wide |
   | 100 | `block` | Unsanctioned hosts | fleet-wide |

   Specific-and-restrictive sits above broad-and-permissive, because the first
   match ends evaluation.

3. **[`ferentin_endpoint_mcp_rule`](../../docs/resources/endpoint_mcp_rule.md)**
   — allow / block for MCP servers and for what AI tools may do, a second
   first-match-wins ladder:

   | Priority | Action | Rule | Scope |
   | --- | --- | --- | --- |
   | 10 | `block` | Unvetted remote MCP hosts (`host`) | fleet-wide |
   | 20 | `block` | Known-bad local MCP packages (`package`) | fleet-wide |
   | 30 | `block` | Shell from AI tools (`tool_behavior`) | contractors group |

4. **[`ferentin_endpoint_agent_approval`](../../docs/resources/endpoint_agent_approval.md)**
   — which catalogued AI agents are approved to run (`approved_agents`:
   `claude-code` and `cursor` by default), on every device. An agent with no
   decision falls through to `unapproved_agent_action`: reported at
   `observe` and `steer`, blocked at `allowlist` and on the strict groups from
   the first apply. Approve the agents your teams rely on before either
   applies.

5. **[`ferentin_endpoint_policy_settings`](../../docs/resources/endpoint_policy_settings.md)**
   — what happens when **no** rule matches, plus the unapproved-MCP and
   unapproved-agent actions, the DNS/QUIC flags, and whether call-time MCP
   blocks deny or only count (`hook_rule_enforcement`). One tenant-default row
   (no `device_group_id`) that every device including ungrouped ones resolves
   through, plus per-group overrides.

## Rolling it out

The rule set is identical at every stage. Only the posture — what happens to
traffic **no rule matched** — changes. The stage is not an enforcement kill
switch: the `block` rules above enforce from the first apply, at
`observe` too. Set `enabled = false` on a rule to ship it inert.

```sh
terraform apply                                     # observe   (default)
terraform apply -var enforcement_stage=steer        # rewrite MCP configs to the gateway
terraform apply -var enforcement_stage=allowlist    # unmatched traffic is blocked
```

| Stage | `unapproved_mcp_action` | `unapproved_agent_action` | `default_destination_action` | `mcp_gateway_url` | DNS/QUIC flags |
| --- | --- | --- | --- | --- | --- |
| `observe` | `report_only` | `report_only` | `allow` | unset | off |
| `steer` | `report_only` | `report_only` | `allow` | set | off |
| `allowlist` | `quarantine` | `block` | `block` | set | on |

Groups in `strict_groups` (contractors by default) get the strict override from
the first apply, regardless of stage — a useful way to pilot enforcement on the
population where the blast radius is smallest.

**Before selecting `allowlist`, confirm the rules cover every sanctioned
provider.** At that stage they become the allowlist; anything unmatched is
blocked on the device.

### Call-time enforcement

`hook_rule_enforcement` is its own variable, not part of the stage, because the
platform refuses `enforce` (409) until every active device in a scope runs an
agent build that enforces call-time rules. At the default `shadow`, the
`tool_behavior` block above counts the shell calls it would deny and denies
nothing. Check `hookRuleEnforcementCoverage` on the posture fleet status in the
console, then:

```sh
terraform apply -var hook_rule_enforcement=enforce
```

The provider reports a refused move to `enforce` as a fleet-readiness error,
not a conflict.

## Sharp edges

**Destination rules allow or block; they do not steer yet.** Only MCP traffic
is steered today, through the tenant's public edge
(`https://<tenant-short-name>.mcp.ferentin.net/v1`, the `mcp_gateway_url` the
`steer` stage sets). An allowed destination is reached directly from the
device, so gateway LLM policy and DLP do not see it. The provider warns at
plan on a destination `steer` rule.

**The legal carve-out is the only way to OpenAI.** OpenAI is not in
`sanctioned_providers`, so at `allowlist` the priority-5 rule is what lets
legal reach it, and criteria are not enforced on-device yet (below). Give an
exception like this an owner: it is the kind that gets added for one team and
outlives its reason.

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

**The posture condition fails the plan rather than vanishing.** If a rule
has a `posture_predicate` (for example one set in the console) and the config
leaves it out, `plan` fails and says how to keep it (`true`) or remove it
(`false`). Removing it widens the rule to non-compliant devices, so the
provider won't do it silently.

**Approve internal local MCP servers; don't `allow` them.** A local `allow`
puts the developer's entry back exactly as written, so the server runs outside
Ferentin and nothing sees its tool calls. It is also tenant-wide:
`device_group_ids` and `criteria` are refused on a `package` or
`argv_contains` allow until devices carry an authenticated identity when a
local server is admitted. Approving a server under Registries → MCP Servers →
Local servers in the admin console keeps every call governed; the provider
does not manage that approval yet. Narrowing a `block` is fine, as the shell
rule does.

**Destroying an agent approval denies the agent where the default blocks.**
Removing a slug from `approved_agents` deletes its decision, and the agent
falls back to `unapproved_agent_action`: on the contractors group, and on
every device at `allowlist`, that blocks it. `plan` warns. To deny an agent on
purpose, give it a `block` decision instead.

**`local_mcp_block_enabled` is absolute.** On the contractors override it
beats approvals and any `allow` rule: contractors run no local MCP servers at
all. Turn it off for a group to let approvals and rules decide there.

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
terraform import ferentin_endpoint_mcp_rule.block_unvetted_remote      <tenant_id>/<rule_id>
terraform import 'ferentin_endpoint_agent_approval.approved["cursor"]' <tenant_id>/cursor
terraform import ferentin_endpoint_policy_settings.default             <tenant_id>
terraform import 'ferentin_endpoint_policy_settings.override["contractors"]' <tenant_id>/<device_group_id>
```
