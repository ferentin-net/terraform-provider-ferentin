# Probe an existing workload OAuth client against its bound IdP and surface
# the result. Each plan / apply re-runs the probe — useful for guardrailing
# config in CI.

data "ferentin_workload_oauth_client_test" "salesforce_check" {
  client_id = ferentin_workload_oauth_client.salesforce_cc.client_id_resource
}

# Use the result to gate downstream resources or surface in outputs.
output "salesforce_idp_reachable" {
  value = data.ferentin_workload_oauth_client_test.salesforce_check.overall_pass
}
