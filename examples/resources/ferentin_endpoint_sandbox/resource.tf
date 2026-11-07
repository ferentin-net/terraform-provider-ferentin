# The agent sandbox: what AI coding tools' shell commands may write, must not
# touch and may reach on managed devices. One per tenant.
resource "ferentin_endpoint_sandbox" "this" {
  tools = {
    # Enforced: a managed file the developer cannot switch off.
    "claude-code" = { enabled = true }
    "codex"       = { enabled = true }
    # Docker and similar need to run outside the sandbox; let the tool's own
    # approval prompt allow that rather than switching the sandbox off.
    "copilot" = { enabled = true, allow_unsandboxed = true }
    # A default only: Cursor reads the sandbox from the developer's settings.
    "cursor" = { enabled = true }
  }

  # Omit this block to keep the devices' credential template (~/.ssh, ~/.aws,
  # ~/.gnupg, ~/.config/gh, ~/Library/Keychains). Set, it is taken as written,
  # so restate the template if you still want it.
  filesystem = {
    writable = ["~/.cache/pip", "/opt/build-cache"]
    denied   = ["~/.ssh", "~/.aws", "~/.gnupg", "~/.config/gh", "~/Library/Keychains", "~/secrets"]
  }

  allowed_domains = ["registry.npmjs.org", "pypi.org", "*.githubusercontent.com"]

  # Also allow what the tenant already allows elsewhere: unconditioned allow
  # destination rules, approved MCP servers and the Ferentin gateway.
  include_derived_domains = true

  description = "Coding agents: package registries only"
}
