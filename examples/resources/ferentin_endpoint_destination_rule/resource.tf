# Endpoint destination rules govern AI traffic on managed devices. They are
# evaluated on-device, FIRST MATCH WINS by ascending priority — so priority is
# policy semantics, not cosmetics.

# 1. Block ChatGPT outright for contractors, and only for the ChatGPT desktop
#    app (a browser hitting the same host is a different code identity).
resource "ferentin_endpoint_destination_rule" "block_chatgpt_for_contractors" {
  name        = "block-chatgpt-contractors"
  description = "Contractors may not use the ChatGPT desktop app"
  priority    = 10

  destination_kind = "ai_provider"
  catalog_slug     = "openai"
  action           = "block"

  app_bundle_ids   = ["com.openai.chat"]
  device_group_ids = [ferentin_device_group.contractors.group_id]
}

# 2. Steer engineering's Claude Desktop traffic through service-edge so it is
#    governed, logged, and DLP-scanned. The device never holds the upstream
#    provider key — service-edge presents it.
resource "ferentin_endpoint_destination_rule" "steer_claude_for_engineering" {
  name     = "steer-claude-engineering"
  priority = 20

  destination_kind = "ai_provider"
  catalog_slug     = "anthropic"
  action           = "steer"

  # Must be https:// — an http:// target would downgrade every steered flow on
  # the fleet to cleartext, and both the provider and the platform reject it.
  steer_to_url = "https://edge.example.com/v1"

  app_bundle_ids   = ["com.anthropic.claudefordesktop"]
  device_group_ids = [ferentin_device_group.engineering.group_id]
}

# 3. Block an explicit host list fleet-wide. No device_group_ids means EVERY
#    device in the tenant, including ungrouped ones.
resource "ferentin_endpoint_destination_rule" "block_unsanctioned_hosts" {
  name     = "block-unsanctioned-hosts"
  priority = 100

  destination_kind  = "host"
  destination_hosts = ["api.unsanctioned-ai.example", "*.shadow-llm.example"]
  action            = "block"
}

# 4. Scope a rule to a POPULATION with criteria. Criteria narrow a rule: this
#    allow applies only to devices in the engineering group AND signed in as a
#    user whose department claim is legal or compliance. Omitting `criteria`
#    would make the allow apply to every user on those devices.
#
#    Criteria groups combine via `criteria_combinator`; conditions inside a
#    group combine via that group's own `operator`.
#
#    NOTE: the endpoint agent ships criteria in the policy bundle but does not
#    evaluate them until the on-device user principal lands (platform#2014), so
#    a criteria-scoped rule is inert on the fleet today — fail-closed for allow
#    and steer, but a criteria-scoped `block` does not block.
resource "ferentin_endpoint_destination_rule" "allow_chatgpt_for_legal" {
  name        = "allow-chatgpt-legal"
  description = "Legal and compliance may use ChatGPT; everyone else falls through"
  priority    = 5

  destination_kind = "ai_provider"
  catalog_slug     = "openai"
  action           = "allow"

  device_group_ids    = [ferentin_device_group.engineering.group_id]
  criteria_combinator = "OR"

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
    {
      operator = "AND"
      conditions = [{
        field          = "email"
        operator       = "ends_with"
        value          = jsonencode("@legal.example.com")
        case_sensitive = false
      }]
    },
  ]
}
