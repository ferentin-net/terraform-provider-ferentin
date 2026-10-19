variable "profile" {
  type        = string
  description = "Shared-config profile holding the tokens `ferentin login --profile <name>` stashed in the OS keyring."
  default     = "prod"
}

variable "adopt_managed_by" {
  type        = list(string)
  description = <<-EOT
    Print import blocks for objects whose managed_by is one of these. The
    default adopts what was created in the admin console. Objects with
    managed_by = "iac" were created by a Terraform configuration, possibly
    another one that still manages them: add "iac" only if that configuration
    is gone.
  EOT
  default     = ["console"]

  validation {
    condition     = alltrue([for m in var.adopt_managed_by : contains(["console", "iac", "cli", "system", "unknown"], m)])
    error_message = "adopt_managed_by entries must be console, iac, cli, system or unknown."
  }
}

variable "skip_last_written_by_terraform" {
  type        = bool
  description = <<-EOT
    Skip objects whose last_modified_by is "iac". managed_by never changes, so
    a console-created object that another configuration has since imported
    still reads managed_by = "console"; having been last written by Terraform
    is the sign that a configuration owns it. Set false only once you know no
    other configuration does.
  EOT
  default     = true
}
