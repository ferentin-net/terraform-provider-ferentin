# Print an `import` block for every endpoint object Terraform does not manage
# yet: device groups, destination rules, MCP rules, posture rows and AI-agent
# decisions.
#
#   terraform init
#   terraform apply                       # reads only; writes nothing to the tenant
#   terraform output -raw import_blocks > ../my-endpoint-policy/imports.tf
#
# Then, in the configuration that will own the policy:
#
#   terraform plan -generate-config-out=generated.tf
#
# The generated blocks copy what the platform holds, so the plan reads
# "N to import, 0 to change". See the "Bring an existing endpoint policy under
# Terraform" guide for tidying them up.

terraform {
  required_version = ">= 1.11"

  required_providers {
    ferentin = {
      source  = "ferentin-net/ferentin"
      version = "~> 0.1"
    }
  }
}

provider "ferentin" {
  # endpoint + tenant_id auto-default. See the root README for auth options.
  # Reading needs `policy:rw` (rules, posture) and `devices:groups:rw` (groups).
  profile = var.profile
}

data "ferentin_device_groups" "all" {}
data "ferentin_endpoint_destination_rules" "all" {}
data "ferentin_endpoint_mcp_rules" "all" {}
data "ferentin_endpoint_policy_settings_scopes" "all" {}
data "ferentin_endpoint_agent_approvals" "all" {}

locals {
  # Resource names: the object's name, lowercased, non-alphanumerics folded to
  # "_", plus the first 8 characters of its ID so two objects with similar
  # names cannot collide. Rename them before the first apply.
  label = {
    for o in concat(
      [for g in data.ferentin_device_groups.all.groups : { id = g.group_id, name = g.name }],
      [for r in data.ferentin_endpoint_destination_rules.all.rules : { id = r.rule_id, name = r.name }],
      [for r in data.ferentin_endpoint_mcp_rules.all.rules : { id = r.rule_id, name = r.name }],
    ) : o.id => "${trim(replace(lower(o.name), "/[^a-z0-9]+/", "_"), "_")}_${substr(o.id, 0, 8)}"
  }

  adopt = toset(var.adopt_managed_by)

  # Whether to adopt each object, keyed by kind and import ID (a device group
  # and its posture override share an import ID).
  #
  # managed_by never changes, so an object created in the console and since
  # imported by another configuration still says "console". Its
  # last_modified_by = "iac" is the sign that some configuration writes it.
  wanted = { for o in concat(
    [for x in data.ferentin_device_groups.all.groups : { k = "g:${x.import_id}", by = x.managed_by, last = x.last_modified_by }],
    [for x in data.ferentin_endpoint_destination_rules.all.rules : { k = "d:${x.import_id}", by = x.managed_by, last = x.last_modified_by }],
    [for x in data.ferentin_endpoint_mcp_rules.all.rules : { k = "m:${x.import_id}", by = x.managed_by, last = x.last_modified_by }],
    [for x in data.ferentin_endpoint_policy_settings_scopes.all.scopes : { k = "p:${x.import_id}", by = x.managed_by, last = x.last_modified_by }],
    [for x in data.ferentin_endpoint_agent_approvals.all.approvals : { k = "a:${x.import_id}", by = x.managed_by, last = x.last_modified_by }],
    ) : o.k => contains(local.adopt, o.by) && !(var.skip_last_written_by_terraform && o.last == "iac")
  }

  imports = concat(
    [for g in data.ferentin_device_groups.all.groups : {
      to = "ferentin_device_group.g_${local.label[g.group_id]}", id = g.import_id
    } if local.wanted["g:${g.import_id}"]],
    [for r in data.ferentin_endpoint_destination_rules.all.rules : {
      to = "ferentin_endpoint_destination_rule.d_${local.label[r.rule_id]}", id = r.import_id
    } if local.wanted["d:${r.import_id}"]],
    [for r in data.ferentin_endpoint_mcp_rules.all.rules : {
      to = "ferentin_endpoint_mcp_rule.m_${local.label[r.rule_id]}", id = r.import_id
    } if local.wanted["m:${r.import_id}"]],
    [for s in data.ferentin_endpoint_policy_settings_scopes.all.scopes : {
      # The tenant default is "default"; an override is named after its group.
      to = "ferentin_endpoint_policy_settings.${s.device_group_id == null ? "default" : "p_${lookup(local.label, s.device_group_id, s.device_group_id)}"}"
      id = s.import_id
    } if local.wanted["p:${s.import_id}"]],
    [for a in data.ferentin_endpoint_agent_approvals.all.approvals : {
      to = "ferentin_endpoint_agent_approval.a_${trim(replace(lower(a.catalog_slug), "/[^a-z0-9]+/", "_"), "_")}", id = a.import_id
    } if local.wanted["a:${a.import_id}"]],
  )
}

output "import_blocks" {
  description = "One import block per object to adopt. Write it to a file with `terraform output -raw import_blocks`."
  value       = join("\n", [for i in local.imports : "import {\n  to = ${i.to}\n  id = \"${i.id}\"\n}\n"])
}

output "already_managed" {
  description = "Objects skipped: created by something not in adopt_managed_by, or last written by Terraform, so probably owned by another configuration."
  value = concat(
    [for g in data.ferentin_device_groups.all.groups : "device group ${g.name} (created by ${g.managed_by}, last written by ${g.last_modified_by})" if !local.wanted["g:${g.import_id}"]],
    [for r in data.ferentin_endpoint_destination_rules.all.rules : "destination rule ${r.name} (created by ${r.managed_by}, last written by ${r.last_modified_by})" if !local.wanted["d:${r.import_id}"]],
    [for r in data.ferentin_endpoint_mcp_rules.all.rules : "MCP rule ${r.name} (created by ${r.managed_by}, last written by ${r.last_modified_by})" if !local.wanted["m:${r.import_id}"]],
    [for s in data.ferentin_endpoint_policy_settings_scopes.all.scopes : "posture ${coalesce(s.device_group_id, "tenant default")} (created by ${s.managed_by}, last written by ${s.last_modified_by})" if !local.wanted["p:${s.import_id}"]],
    [for a in data.ferentin_endpoint_agent_approvals.all.approvals : "agent decision ${a.catalog_slug} (created by ${a.managed_by}, last written by ${a.last_modified_by})" if !local.wanted["a:${a.import_id}"]],
  )
}
