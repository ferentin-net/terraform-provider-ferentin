# Every device group in the tenant, sorted by name.
data "ferentin_device_groups" "all" {}

# Groups created in the admin console, which Terraform does not manage yet.
output "console_groups" {
  value = { for g in data.ferentin_device_groups.all.groups : g.name => g.import_id if g.managed_by == "console" }
}
