# Device groups are the policy-scoping unit for managed devices. Endpoint
# destination rules and posture overrides both target a group, so declaring
# groups here lets those resources use a reference instead of a hardcoded UUID.

resource "ferentin_device_group" "contractors" {
  name        = "contractors"
  description = "Third-party contractors on BYOD hardware"

  # Where the group came from. Immutable — changing it replaces the group.
  source = "manual"
}

resource "ferentin_device_group" "engineering" {
  name        = "engineering"
  description = "Engineering laptops enrolled via Jamf"
  source      = "mdm"

  # Identifier for this group in the upstream system (Jamf smart-group id here).
  # Also immutable.
  external_id = "jamf-smart-group-42"
}
