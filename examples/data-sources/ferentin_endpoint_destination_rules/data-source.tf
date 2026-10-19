# Every endpoint destination rule, in evaluation order (ascending priority).
data "ferentin_endpoint_destination_rules" "all" {}

output "rule_ladder" {
  value = [for r in data.ferentin_endpoint_destination_rules.all.rules : format("%04d %-5s %-7s %s", r.priority, r.action, r.managed_by, r.name)]
}
