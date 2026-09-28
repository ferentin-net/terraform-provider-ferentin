# Probe an existing workload OAuth client against its bound IdP and surface
# the result. Each plan / apply re-runs the probe — useful for guardrailing
# config in CI.

variable "salesforce_workload_client_id" {
  type        = string
  description = "In a real config this is `ferentin_workload_oauth_client.<name>.client_id_resource`."
}

data "ferentin_workload_oauth_client_test" "salesforce_check" {
  client_id = var.salesforce_workload_client_id
}

# Use the result to gate downstream resources or surface in outputs.
output "salesforce_idp_reachable" {
  value = data.ferentin_workload_oauth_client_test.salesforce_check.overall_pass
}
