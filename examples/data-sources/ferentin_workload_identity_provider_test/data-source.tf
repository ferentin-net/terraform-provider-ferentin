# Probe a workload identity provider trust config. The result is a raw JSON
# string — parse with jsondecode() to extract fields.

data "ferentin_workload_identity_provider_test" "aws_check" {
  provider_id = ferentin_workload_identity_provider.aws_prod_eks.provider_id
}

output "aws_trust_result" {
  value = jsondecode(data.ferentin_workload_identity_provider_test.aws_check.result)
}
