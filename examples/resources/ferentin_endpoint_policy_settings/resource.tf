# Endpoint posture. Omit device_group_id for the TENANT DEFAULT row that every
# device — including ungrouped ones — resolves through. Set it for a per-group
# override.
#
# NOTE: this resource is an upsert. If posture already exists (e.g. configured
# in the admin console) `terraform apply` ADOPTS the existing row rather than
# failing; `managed_by` still reports the original creator so the adoption shows
# up as drift.
#
# `terraform destroy` on the tenant-default row does NOT delete it and does NOT
# change it — the platform refuses to delete a row every device resolves
# through, so Terraform just stops managing it and the fleet keeps enforcing
# whatever was last applied. That is fail-closed on purpose. To genuinely stand
# enforcement down, apply the permissive posture first, then destroy.

# Tenant default: observe and report, enforce nothing. This is the Phase 1
# shadow-AI-visibility posture — tightening is a deliberate act.
resource "ferentin_endpoint_policy_settings" "default" {
  unapproved_mcp_action      = "report_only"
  default_destination_action = "allow"
}

# Contractors get the strict posture: quarantine unapproved MCP server configs,
# force all AI traffic through an allowlist, and close the network side-channels
# that hide SNI.
resource "ferentin_endpoint_policy_settings" "contractors" {
  device_group_id = ferentin_device_group.contractors.group_id

  unapproved_mcp_action = "quarantine"

  # Approved MCP client configs are rewritten to point here. Must be https://.
  mcp_gateway_url = "https://acme.mcp.example.com"

  # With "block", your destination rules become an allowlist — make sure they
  # cover every sanctioned provider before flipping this.
  default_destination_action = "block"

  ech_strip_enabled  = true
  doh_block_enabled  = true
  quic_block_enabled = true
}
