# Probe a workload identity provider trust config. The result is a raw JSON
# string — parse with jsondecode() to extract fields.

variable "aws_workload_identity_provider_id" {
  type        = string
  description = "In a real config this is `ferentin_workload_identity_provider.<name>.provider_id`."
}

data "ferentin_workload_identity_provider_test" "aws_check" {
  provider_id = var.aws_workload_identity_provider_id
}

output "aws_trust_result" {
  value = jsondecode(data.ferentin_workload_identity_provider_test.aws_check.result)
}
