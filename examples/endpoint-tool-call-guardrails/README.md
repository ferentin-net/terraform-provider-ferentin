# Guardrails for coding agents on managed endpoints

Block the tool calls a coding agent should never make, count the matches
first, then deny them, one device group at a time.

The walk-through, including what `enforce` does and does not cover, is the
[Block risky tool calls from coding agents](../../docs/guides/endpoint-tool-calls.md)
guide.

## Layout

```
examples/endpoint-tool-call-guardrails/
├── README.md
├── main.tf        # pilot group, two tool call rules, tenant + pilot posture, outputs
└── variables.tf   # blocked commands, enforcement per scope, shared posture
```

## What it builds

| Priority | Rule | Matches | Scope |
| --- | --- | --- | --- |
| 10 | Block exfiltration commands | `command_binary`: `blocked_commands` | every device |
| 20 | Block the shell tool | `tool_behavior`: `shell` | the pilot group |

Tool call rules are always `block`. Whether a match denies the call or only
counts it is `hook_rule_enforcement` on the scope's posture: the tenant
default, and the pilot group's own row.

## Rolling it out

```sh
terraform apply                                                    # both scopes count (shadow)
terraform apply -var pilot_hook_rule_enforcement=enforce           # the pilot group denies
terraform apply -var pilot_hook_rule_enforcement=enforce \
                -var tenant_hook_rule_enforcement=enforce          # everyone denies
```

Activate in the admin console after each apply: `apply` only stages the
change. Add devices to the pilot group in the console or from your MDM.

The platform refuses a move to `enforce` (409) until every active device in
the scope runs an agent build that can deny. The provider reports it as *fleet
not ready for hook_rule_enforcement = "enforce"*; the console's Posture tab
lists the devices holding the scope back.

## Sharp edges

**The pilot override restates the tenant posture.** A group with its own
posture row ignores the tenant default for every setting, and on a new row an
attribute left out takes the platform's default (`unapproved_mcp_action =
quarantine`), not the tenant's. The `shared_posture` local keeps the two rows
identical apart from `hook_rule_enforcement`; add any other tenant setting you
manage to it.

**One configuration owns the tenant-default posture row.** If another config
already manages `ferentin_endpoint_policy_settings.default`, set
`hook_rule_enforcement` there instead of applying both.

**Blocking `shell` blocks every command the agent runs.** That is why the
example scopes it to the pilot group.

**`enforce` denies on Claude Code and Cursor today.** Codex, Copilot and
Antigravity calls are counted.

## Required scopes

`policy:rw` for the rules and posture, and `devices:groups:rw` for the device
group.
