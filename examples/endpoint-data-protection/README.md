# PII and card data on managed endpoints

Find SSNs, government IDs and payment card numbers in what coding agents are
given, run and read on a developer's machine. Log every match first, then
block the ones that matter. One data protection policy, on the endpoint only.

The walk-through, including what the device enforces differently from the
gateway, is the
[Keep PII and card data out of coding agents](../../docs/guides/endpoint-data-protection.md)
guide.

## Layout

```
examples/endpoint-data-protection/
├── README.md
├── main.tf        # detector lookups, the policy, three outputs
└── variables.tf   # enforcement, device groups, tool output, block message
```

## What it builds

[`ferentin_data_protection_policy`](../../docs/resources/data_protection_policy.md)
with the `US_PII` and `FINANCIAL` profiles, on three device scopes:

| Scope | Scanned | Before |
| --- | --- | --- |
| `apply_to_prompt_input` | What a developer submits to the agent | the model sees it |
| `apply_to_tool_input` | Shell commands, fetched URLs and bodies, file writes | they run |
| `apply_to_tool_output` | What a tool returns | the model sees it (best effort) |

| Detectors | Effect |
| --- | --- |
| `US_SSN`, `US_PASSPORT`, `US_DRIVERS_LICENSE`, `CREDIT_CARD`, `CVV`, `US_BANK_ROUTING`, `IBAN` | `enforcement`: `log`, then `block` |
| Everything else in the two profiles | `log` |
| `IN_IFSC`, `CRYPTO_WALLET_ETH`, `CRYPTO_WALLET_BTC` | off |

## Rolling it out

```sh
terraform apply                                      # every match is logged
terraform apply -var enforcement=block               # the enforced detectors block
```

Activate in the admin console after each apply: `apply` only stages the
change. Between the two, read **Activity → Endpoint → Data protection** to see
what would have been blocked.

To try `block` on a few devices first, pass their group:

```sh
terraform apply -var enforcement=block -var 'device_group_ids=["<group-id>"]'
```

## Sharp edges

**Devices do not rewrite prompts or tool calls.** `redact` and `tokenize`
stop them, the same as `block`, which is why `enforcement` takes only `log`
or `block`. The `endpoint_effect_mapping` output shows what the platform maps.

**`US_ITIN` runs at the gateway only.** Devices drop it, so it is left out of
the enforced list.

**No criteria.** A policy with `criteria` is withheld from devices and
enforces nothing there. Target devices with `device_group_ids` instead.

**The gateway scopes default to on.** `apply_to_llm_input` and
`apply_to_llm_output` are `true` unless set, so the example sets both to
`false`.

## Required scopes

`policy:rw` for the policy and the detector lookups.
