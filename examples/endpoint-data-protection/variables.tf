variable "profile" {
  type        = string
  description = "Shared-config profile holding the tokens `ferentin login --profile <name>` stashed in the OS keyring."
  default     = "prod"
}

variable "policy_name" {
  type        = string
  description = "Name of the data protection policy."
  default     = "endpoint-pii-pci"
}

variable "priority" {
  type        = number
  description = "Evaluation priority among the tenant's data protection policies (1 = first)."
  default     = 50
}

variable "enforcement" {
  type        = string
  description = <<-EOT
    What the enforced detectors do: log (record the match, change nothing) or
    block (stop the prompt or tool call, replace the tool output).

    Start on log, read Activity → Endpoint → Data protection for a while, then
    move to block.
  EOT
  default     = "log"

  validation {
    condition     = contains(["log", "block"], var.enforcement)
    error_message = "enforcement must be log or block. Devices enforce redact and tokenize as block on prompts and tool calls, so say block."
  }
}

variable "disabled_detectors" {
  type        = set(string)
  description = "Detectors the profiles pull in that this policy leaves out."
  default     = ["IN_IFSC", "CRYPTO_WALLET_ETH", "CRYPTO_WALLET_BTC"]
}

variable "scan_tool_output" {
  type        = bool
  description = "Also scan what tools return, before the model sees it. Best effort, and enforced on Claude Code, Codex and local MCP servers that run through ferentin-mcp only."
  default     = true
}

variable "device_group_ids" {
  type        = set(string)
  description = "Device groups that receive the policy. Empty = every enrolled device. At most 100."
  default     = []

  validation {
    condition     = length(var.device_group_ids) <= 100
    error_message = "A policy can target at most 100 device groups."
  }
}

variable "blocked_message" {
  type        = string
  description = "Shown to the developer, in the agent, when a prompt or tool call is blocked. At most 1,024 characters."
  default     = "Blocked by Ferentin: this looks like PII or payment card data, such as an SSN or a card number. Use synthetic test data instead."

  validation {
    condition     = length(var.blocked_message) <= 1024
    error_message = "blocked_message must be at most 1,024 characters."
  }
}
