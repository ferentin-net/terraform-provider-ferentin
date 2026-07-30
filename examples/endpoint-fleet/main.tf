# Endpoint policy for a managed macOS fleet, staged from observe to enforce.
#
# Three moving parts:
#
#   1. Device groups     — the targeting dimension for everything else.
#   2. Destination rules — allow / block / steer per AI destination, evaluated
#                          ON-DEVICE, first match wins by ascending priority.
#   3. Posture           — what happens to traffic and to unapproved MCP server
#                          configs when NO rule matches. One tenant-default row
#                          plus optional per-group overrides.
#
# `var.enforcement_stage` moves the whole fleet along the rollout without
# rewriting the rules: the rule set stays constant, the posture tightens.

terraform {
  # >= 1.9 for the cross-variable validation on `strict_groups`, which catches
  # a group name that has no matching device_groups entry at plan time.
  required_version = ">= 1.9"

  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

provider "ferentin" {
  # endpoint + tenant_id auto-default. See the root README for auth options.
  # The endpoint resources need `devices:groups:rw` (or the broader
  # `devices:rw`) in addition to `policy:rw`.
  profile = var.profile
}

# --- 1. Device groups ----------------------------------------------------
# for_each over a map so adding a population is a variable edit, not a copy of
# a resource block. `source` and `external_id` are immutable — changing either
# replaces the group, which re-scopes every rule that targets it, so keep them
# accurate from the start.

resource "ferentin_device_group" "this" {
  for_each = var.device_groups

  name        = each.key
  description = each.value.description
  source      = each.value.source
  external_id = each.value.external_id
}

locals {
  group_ids = { for k, g in ferentin_device_group.this : k => g.group_id }

  # Posture per rollout stage — the single source of truth for what each stage
  # means. `observe` reports without touching the machine; `steer` starts
  # rewriting MCP client configs to the gateway; `allowlist` makes the
  # destination rules exhaustive — anything unmatched is blocked.
  stage = {
    observe = {
      unapproved_mcp_action      = "report_only"
      default_destination_action = "allow"
      rewrite_mcp_configs        = false
      harden_network             = false
    }
    steer = {
      unapproved_mcp_action      = "report_only"
      default_destination_action = "allow"
      rewrite_mcp_configs        = true
      harden_network             = false
    }
    allowlist = {
      unapproved_mcp_action      = "quarantine"
      default_destination_action = "block"
      rewrite_mcp_configs        = true
      harden_network             = true
    }
  }[var.enforcement_stage]
}

# --- 2. Destination rules ------------------------------------------------
#
# The priority ladder. FIRST MATCH WINS, so specific-and-restrictive sits above
# broad-and-permissive:
#
#     5    allow   OpenAI for legal/compliance      (narrow carve-out)
#    10    block   ChatGPT desktop for contractors  (population block)
#    20    steer   Anthropic → service-edge         (fleet-wide governance)
#    30    steer   OpenAI    → service-edge         (fleet-wide governance)
#   100    block   unsanctioned hosts               (catch-all deny)
#
# ~> Do not split ownership of `priority` with the admin console. The console's
#    reorder arrows swap two rows' priorities directly; if Terraform also
#    declares priority, the next plan shows drift and the two fight over
#    evaluation order — which here IS the policy.

# 5. Carve-out: legal and compliance keep direct OpenAI access.
#
# ~> An `allow` above a `steer` is a GOVERNANCE BYPASS, not just a permission.
#    Matching ends evaluation, so this population's traffic never reaches the
#    steer rule at priority 30 — no service-edge, which means no LLM policy, no
#    DLP, and no telemetry for it. That is the point of a carve-out, but it is
#    the kind of thing that gets added for one team and quietly outlives the
#    reason. If you want the exception governed, make it a `steer` to a
#    different service-edge URL instead of an `allow`.
#
# ~> Criteria NARROW a rule. Deleting this `criteria` block does not delete the
#    carve-out — it widens the allow to every user on those devices. That is a
#    fail-open edit; the provider warns on it, and so does the plan diff.
#
# ~> Not yet enforced on-device: the agent ships criteria in the policy bundle
#    but will not match a rule that carries them until the on-device user
#    principal lands (platform#2014). Today this rule is inert — fail-closed for
#    an `allow`, so legal falls through to the rules below until then.
resource "ferentin_endpoint_destination_rule" "allow_openai_legal" {
  name        = "allow-openai-legal"
  description = "Legal and compliance may use OpenAI directly"
  priority    = 5

  destination_kind = "ai_provider"
  catalog_slug     = "openai"
  action           = "allow"

  device_group_ids = [local.group_ids["engineering"]]

  criteria = [
    {
      operator = "AND"
      conditions = [{
        field      = "department"
        operator   = "in"
        value      = jsonencode(["legal", "compliance"])
        value_type = "array"
      }]
    },
  ]
}

# 10. Contractors may not run the ChatGPT desktop app. Scoped by bundle id: a
#     browser hitting the same host is a different code identity and falls
#     through to the steer rules below.
resource "ferentin_endpoint_destination_rule" "block_chatgpt_contractors" {
  name        = "block-chatgpt-contractors"
  description = "Contractors may not use the ChatGPT desktop app"
  priority    = 10

  destination_kind = "ai_provider"
  catalog_slug     = "openai"
  action           = "block"

  app_bundle_ids   = ["com.openai.chat"]
  device_group_ids = [local.group_ids["contractors"]]
}

# 20 + 30. Steer sanctioned providers through service-edge, where LLM policy,
# DLP, and telemetry actually apply. The device never holds an upstream
# provider key — service-edge presents it.
#
# for_each keeps the two rules in lockstep: same target, same semantics, one
# entry per catalog slug. Priorities are derived so the ladder above stays
# readable as slugs are added.
resource "ferentin_endpoint_destination_rule" "steer" {
  for_each = var.steered_providers

  name        = "steer-${each.key}-to-edge"
  description = "Route ${each.key} traffic through service-edge for governance"
  priority    = each.value.priority

  destination_kind = "ai_provider"
  catalog_slug     = each.key
  action           = "steer"

  # Must be https:// — an http:// target downgrades every steered flow on the
  # fleet to cleartext. The provider rejects it at plan time.
  steer_to_url = var.service_edge_url

  # No device_group_ids: every device in the tenant, including ungrouped ones.
  # Targeting groups instead would bump only those groups' bundle versions, so
  # devices elsewhere would not re-pull.
}

# 100. Catch-all deny for hosts that have no catalog entry. `host` matching is
# literal — prefer `ai_provider` where a catalog slug exists, because the agent
# resolves provider hosts from discovery data and a new upstream host does not
# require a rule rewrite.
resource "ferentin_endpoint_destination_rule" "block_unsanctioned" {
  name        = "block-unsanctioned-hosts"
  description = "Known shadow-AI endpoints with no catalog entry"
  priority    = 100

  destination_kind  = "host"
  destination_hosts = var.blocked_hosts
  action            = "block"
}

# --- 3. Posture ----------------------------------------------------------

# Tenant default. No device_group_id — every device, including ungrouped ones,
# resolves through this row.
#
# ~> This resource is an UPSERT. If posture already exists (configured in the
#    console before Terraform took over) apply ADOPTS it rather than failing;
#    `managed_by` keeps naming the original creator, so the adoption shows up as
#    drift rather than passing silently.
#
# ~> `terraform destroy` on this row does NOT delete it and does NOT reset it.
#    The platform refuses to delete a row the whole fleet resolves through, so
#    Terraform stops managing it and the fleet keeps enforcing the last applied
#    posture. Removing the resource block or dropping the module reaches the
#    same code path. To stand enforcement down, apply
#    `enforcement_stage = "observe"` first, then destroy.
resource "ferentin_endpoint_policy_settings" "default" {
  unapproved_mcp_action      = local.stage.unapproved_mcp_action
  default_destination_action = local.stage.default_destination_action

  # From the `steer` stage on, approved MCP client configs found on the device
  # are rewritten to point at the tenant gateway. Null at `observe` leaves the
  # attribute unset: the agent reports what it finds and rewrites nothing.
  # Stepping back down to `observe` therefore clears the gateway again.
  mcp_gateway_url = local.stage.rewrite_mcp_configs ? var.mcp_gateway_url : null

  # Network side-channels that hide SNI. Only meaningful once unmatched traffic
  # is actually being blocked.
  ech_strip_enabled  = local.stage.harden_network
  doh_block_enabled  = local.stage.harden_network
  quic_block_enabled = local.stage.harden_network
}

# Per-group overrides. Unlike the tenant default, a group override IS genuinely
# deleted on destroy — the group then falls back to the tenant default.
#
# Contractors run ahead of the fleet: strict from day one, regardless of stage.
resource "ferentin_endpoint_policy_settings" "override" {
  for_each = var.strict_groups

  device_group_id = local.group_ids[each.key]

  unapproved_mcp_action      = "quarantine"
  mcp_gateway_url            = var.mcp_gateway_url
  default_destination_action = "block"

  ech_strip_enabled  = true
  doh_block_enabled  = true
  quic_block_enabled = true
}

# --- Outputs -------------------------------------------------------------

output "device_group_ids" {
  description = "Group UUIDs keyed by name — what every rule and override targets."
  value       = local.group_ids
}

output "rule_ladder" {
  description = "Rules in evaluation order. First match wins, so read top down."
  value = sort([
    for r in concat(
      [
        ferentin_endpoint_destination_rule.allow_openai_legal,
        ferentin_endpoint_destination_rule.block_chatgpt_contractors,
        ferentin_endpoint_destination_rule.block_unsanctioned,
      ],
      values(ferentin_endpoint_destination_rule.steer),
    ) : format("%04d %-6s %s", r.priority, r.action, r.name)
  ])
}

output "posture_provenance" {
  description = <<-EOT
    Provenance of the tenant-default posture row. Divergence between
    `managed_by` (original creator) and `last_modified_by` (most recent
    writer) is the drift signal — "iac" + "console" means somebody edited a
    Terraform-managed row in the admin console.
  EOT
  value = {
    managed_by       = ferentin_endpoint_policy_settings.default.managed_by
    last_modified_by = ferentin_endpoint_policy_settings.default.last_modified_by
    version          = ferentin_endpoint_policy_settings.default.version
  }
}
