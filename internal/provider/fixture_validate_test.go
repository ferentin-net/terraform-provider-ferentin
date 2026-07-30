package provider

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Validate every acceptance-test fixture against the real provider schema,
// with no platform and no credentials.
//
// This is the gate issue #6 was missing. Four of the seven failures it lists —
// `tags` not being an attribute, `endpoint` missing from the MCP server
// fixture, a `transport` value outside the enum, an un-`jsonencode`d criteria
// value — are schema drift that `terraform validate` catches on its own,
// because validate runs the provider's schema and ValidateConfig locally. They
// sat red for as long as they did only because the sole way to exercise a
// fixture was a full apply against local dev, which needs credentials nobody
// has in CI.
//
// What this does NOT cover: anything the platform decides (uniqueness, FK
// integrity, scope checks). Those still need `make testacc-local`.
func TestAcceptanceFixturesValidate(t *testing.T) {
	tfBin, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform not on PATH; skipping fixture validation")
	}

	// Build the provider once and point a dev_overrides CLI config at it, so
	// validate resolves the binary under test instead of the registry. Nothing
	// here talks to a network: dev_overrides skips `terraform init` entirely.
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "terraform-provider-ferentin")
	build := exec.Command("go", "build", "-o", binPath, "../..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build provider: %v\n%s", err, out)
	}

	cliConfig := filepath.Join(t.TempDir(), "dev.tfrc")
	if err := os.WriteFile(cliConfig, []byte(`provider_installation {
  dev_overrides { "ferentin-net/ferentin" = "`+binDir+`" }
  direct {}
}
`), 0o600); err != nil {
		t.Fatalf("write CLI config: %v", err)
	}

	// The fixtures embed providerBlock(); swap it for one that also declares
	// required_providers, which a standalone `terraform validate` needs and the
	// in-process acctest harness supplies itself.
	const versions = `
terraform {
  required_providers {
    ferentin = {
      source = "ferentin-net/ferentin"
    }
  }
}

provider "ferentin" {}
`

	fixtures := map[string]string{
		"edge_site":          configEdgeSite("tf-acc-fixture", "Fixture site", "primary"),
		"llm_provider":       configLLMProvider("tf-acc-fixture", "test-key", 1),
		"mcp_server":         configMCPServer("tf-acc-fixture"),
		"otel_sink":          configOtelSink("tf-acc-fixture"),
		"mcp_provider":       configMCPProvider("tf-acc-fixture", "Fixture provider"),
		"mcp_policy":         configMCPPolicy("tf-acc-fixture", "allow", 60),
		"llm_policy":         configLLMPolicy("tf-acc-fixture", "AND"),
		"otel_policy":        configOtelPolicy("tf-acc-fixture", "tf-acc-fixture-sink", `["traces", "logs"]`),
		"ai_agent":           configAIAgent("tf-acc-fixture"),
		"data_protection":    configDataProtectionPolicy("tf-acc-fixture", "redact", "blocked"),
		"device_group":       configDeviceGroup("tf-acc-fixture", "Fixture group", "manual"),
		"endpoint_rule":      configEndpointRule("fixture", 10, "block", ""),
		"endpoint_rule_hard": configEndpointRule("fixture", 20, "steer", "https://edge.example.com/v1"),
		"endpoint_criteria": configEndpointRuleWithCriteria("fixture", `
  criteria_combinator = "AND"
  criteria = [{
    operator = "AND"
    conditions = [{
      field    = "department"
      operator = "equals"
      value    = jsonencode("legal")
    }]
  }]
`),
	}

	for name, cfg := range fixtures {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			body := versions + strings.TrimPrefix(cfg, providerBlock())
			if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(body), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			cmd := exec.Command(tfBin, "validate", "-no-color")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+cliConfig)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("terraform validate failed for the %s fixture: %v\n%s\n--- config ---\n%s",
					name, err, out, body)
			}
		})
	}
}
