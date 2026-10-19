# Approved AI on managed endpoints

Allow the AI providers you sanction, pin one of them to its signed desktop app,
and block the rest, on every managed device in the tenant. Four destination
rules and the tenant-default posture, with no device groups and no MCP
rules. Start here; [`../endpoint-fleet/`](../endpoint-fleet/) adds groups,
MCP rules, posture overrides and a staged rollout.

The walk-through, with the reasoning behind each rule, is the
[Approve AI apps on managed endpoints](../../docs/guides/endpoint-approved-ai.md)
guide.

## Layout

```
examples/endpoint-approved-ai/
├── README.md
├── main.tf        # the rule ladder, the tenant-default posture, two outputs
└── variables.tf   # team IDs, blocked hosts, default action, unnamed-flow block, MCP gateway
```

## The rule ladder

[`ferentin_endpoint_destination_rule`](../../docs/resources/endpoint_destination_rule.md)
rules are evaluated **on the device, first match wins by ascending
`priority`**:

| Priority | Action | Rule | Matches |
| --- | --- | --- | --- |
| 10 | `allow` | Approved AI: Anthropic | `.anthropic.com`, `.claude.ai`, from any app |
| 20 | `allow` | Approved AI: ChatGPT desktop | `api.openai.com`, `.chatgpt.com`, only from apps signed by `chatgpt_team_ids` |
| 30 | `block` | Block OpenAI outside approved apps | the same OpenAI hosts, from every other app |
| 40 | `block` | Block unapproved AI | `blocked_hosts` (`.deepseek.com` by default) |

Rules 20 and 30 are a pair: a narrow allow above a broad block on the same
hosts. Together they mean *OpenAI from the ChatGPT app, and nowhere else*:
not from a browser, `curl` or another app. Swap their priorities and the block
wins for everyone.

Traffic no rule matches gets the tenant's `default_destination_action`, set on
[`ferentin_endpoint_policy_settings`](../../docs/resources/endpoint_policy_settings.md).

## Running it

```sh
terraform init
terraform plan          # read it: the posture row may already exist (see below)
terraform apply
```

Then **activate the pending changes in the admin console**. `apply` stages
the rules and posture; devices pick them up only after an activation, which
ships every staged change in the tenant. Until then the console shows the
policy as changed and the fleet keeps enforcing the previous one.

To check a Mac picked it up, open a sanctioned and an unsanctioned host from a
browser:

```sh
curl -sS -o /dev/null -w '%{http_code}\n' https://api.anthropic.com   # answers
curl -sS -o /dev/null -w '%{http_code}\n' https://api.openai.com      # refused: curl is not ChatGPT
```

## Tightening

Each of these is a variable. Try them one at a time, on one machine, before the
fleet:

```sh
# The rules become an allowlist: anything they do not allow is blocked.
terraform apply -var default_destination_action=block

# Drop port-443 flows to bare public IPs, except your VPN concentrators.
terraform apply -var unnamed_flow_block_enabled=true \
  -var 'unnamed_flow_exempt_cidrs=["198.51.100.0/24"]'
```

Every apply writes every variable, so a plain `terraform apply` afterwards
puts both back to their defaults. Activate after each one.

## Sharp edges

**Posture is an upsert, and adopting a row resets what this config leaves
out.** On a tenant whose posture was set in the console, the first apply
adopts the existing row. The DNS/QUIC flags (`ech_strip_enabled`,
`doh_block_enabled`, `quic_block_enabled`) have provider defaults of `false`,
and `mcp_gateway_url` is cleared when left null, so a tenant with any of them
set loses it. The first `plan` shows each one; copy the values you want to keep
into the config before applying. Fields with no default (the unapproved-MCP
action, call-time enforcement, the local MCP policy) keep what the tenant has.

**`destroy` does not reset the posture.** The platform refuses to delete the
row every device resolves through, so Terraform drops it from state and the
fleet keeps the last applied posture. Apply `default_destination_action=allow`
and `unnamed_flow_block_enabled=false` first if you want it permissive. The
rules are deleted.

**An empty `chatgpt_team_ids` would allow OpenAI from every app.** An empty app
filter means *any app*, which makes rule 30 unreachable. The variable refuses
an empty list.

**Prefer `.example.com` to `*.example.com`.** Both match the domain and its
subdomains, but agents without ferentin-endpoint#1246 refuse the `*.` form, and
one refused host makes an agent refuse the tenant's whole policy and keep its
last-known-good one.

**`priority` belongs to Terraform.** The console's reorder arrows are disabled
on Terraform-managed rules. Change the order here.

## Required scopes

`policy:rw` for the rules and the posture.

## Importing existing rules

Already configured in the console? Import instead of re-creating:

```sh
terraform import ferentin_endpoint_destination_rule.approved_anthropic <tenant_id>/<rule_id>
terraform import ferentin_endpoint_policy_settings.default              <tenant_id>
```
