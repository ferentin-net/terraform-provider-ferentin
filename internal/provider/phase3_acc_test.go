package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acctests for the Phase 3 + remaining resources. Each test exercises the
// Create → Read → Update → Import → Destroy cycle, gated by TF_ACC=1.

// TestAccMCPProvider_basic covers the tenant-custom MCP provider definition.
func TestAccMCPProvider_basic(t *testing.T) {
	name := "tf-acc-provider-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configMCPProvider(name, "Test provider for acceptance suite"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_mcp_provider.test", "display_name", name),
					resource.TestCheckResourceAttr("ferentin_mcp_provider.test", "description", "Test provider for acceptance suite"),
					// `http` IS Streamable HTTP on this enum. The catalog column
					// predates the rename and its allowed set is
					// [stdio sse http] — `streamable_http` is the value the
					// downstream `ferentin_mcp_server.transport_type` takes, a
					// different enum on a different table.
					resource.TestCheckResourceAttr("ferentin_mcp_provider.test", "transport", "http"),
					resource.TestCheckResourceAttr("ferentin_mcp_provider.test", "allow_endpoint_override", "false"),
					resource.TestCheckResourceAttrSet("ferentin_mcp_provider.test", "provider_id"),
				),
			},
			{
				Config: configMCPProvider(name, "Updated description"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_mcp_provider.test", "description", "Updated description"),
				),
			},
			{
				ResourceName:      "ferentin_mcp_provider.test",
				ImportState:       true,
				ImportStateVerify: true,
				// `transport` etc. are normalized server-side; skip Computed-but-
				// platform-derived fields when verifying import equality.
				ImportStateVerifyIgnore: []string{},
			},
		},
	})
}

// TestAccMCPPolicy_basic covers the nested effect-attribute resource path,
// including the allow/deny enum and rate-limit fields.
func TestAccMCPPolicy_basic(t *testing.T) {
	name := "tf-acc-mcp-pol-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configMCPPolicy(name, "allow", 60),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_mcp_policy.test", "name", name),
					resource.TestCheckResourceAttr("ferentin_mcp_policy.test", "effect.type", "allow"),
					resource.TestCheckResourceAttr("ferentin_mcp_policy.test", "effect.rate_limit_per_minute", "60"),
					resource.TestCheckResourceAttr("ferentin_mcp_policy.test", "enabled", "true"),
					resource.TestCheckResourceAttr("ferentin_mcp_policy.test", "validate_arguments", "true"),
				),
			},
			{
				// Flip the effect type — exercises Update + the OneOf validator.
				Config: configMCPPolicy(name, "deny", 30),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_mcp_policy.test", "effect.type", "deny"),
					resource.TestCheckResourceAttr("ferentin_mcp_policy.test", "effect.rate_limit_per_minute", "30"),
				),
			},
			{
				ResourceName:      "ferentin_mcp_policy.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccLLMPolicy_basic covers the nested criteria + conditions path —
// the deepest schema in the provider.
func TestAccLLMPolicy_basic(t *testing.T) {
	name := "tf-acc-llm-pol-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configLLMPolicy(name, "AND"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_llm_policy.test", "name", name),
					resource.TestCheckResourceAttr("ferentin_llm_policy.test", "enabled", "true"),
					resource.TestCheckResourceAttr("ferentin_llm_policy.test", "criteria.0.operator", "AND"),
				),
			},
			{
				ResourceName:      "ferentin_llm_policy.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccOtelPolicy_basic exercises the list-attribute path (signals,
// sink_ids) and the per-element listvalidator.ValueStringsAre.
func TestAccOtelPolicy_basic(t *testing.T) {
	name := "tf-acc-otel-pol-" + randomSuffix(t)
	sinkName := "tf-acc-otel-sink-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configOtelPolicy(name, sinkName, `["traces", "logs"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_otel_policy.test", "name", name),
					resource.TestCheckResourceAttr("ferentin_otel_policy.test", "signals.#", "2"),
					resource.TestCheckResourceAttr("ferentin_otel_policy.test", "signals.0", "traces"),
					resource.TestCheckResourceAttr("ferentin_otel_policy.test", "signals.1", "logs"),
					resource.TestCheckResourceAttr("ferentin_otel_policy.test", "sink_count", "1"),
				),
			},
			{
				// Add a signal — exercises Update + list normalization.
				Config: configOtelPolicy(name, sinkName, `["traces", "metrics", "logs"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_otel_policy.test", "signals.#", "3"),
				),
			},
		},
	})
}

// TestAccAIAgent_basic covers the AI-agent OIDC client path with its
// constrained scope allowlist and server-issued client_secret.
func TestAccAIAgent_basic(t *testing.T) {
	name := "tf-acc-agent-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configAIAgent(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_ai_agent.test", "name", name),
					resource.TestCheckResourceAttr("ferentin_ai_agent.test", "agent_platform", "claude"),
					resource.TestCheckResourceAttr("ferentin_ai_agent.test", "application_type", "SERVICE"),
					resource.TestCheckResourceAttr("ferentin_ai_agent.test", "token_endpoint_auth_method", "client_secret_basic"),
					resource.TestCheckResourceAttr("ferentin_ai_agent.test", "active", "true"),
					resource.TestCheckResourceAttrSet("ferentin_ai_agent.test", "client_id"),
					resource.TestCheckResourceAttrSet("ferentin_ai_agent.test", "client_secret"),
					resource.TestCheckResourceAttr("ferentin_ai_agent.test", "ai_client_type", "agent"),
				),
			},
			{
				ResourceName:      "ferentin_ai_agent.test",
				ImportState:       true,
				ImportStateVerify: true,
				// `client_secret` is only returned on Create — import can't recover it.
				ImportStateVerifyIgnore: []string{"client_secret"},
			},
		},
	})
}

// --- HCL fixtures ---------------------------------------------------------

func configMCPProvider(displayName, description string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_mcp_provider" "test" {
  display_name = %q
  description  = %q
  transport    = "http"
  default_url  = "https://example.com/mcp"
}
`, displayName, description)
}

func configMCPPolicy(name, effect string, ratePerMinute int) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_mcp_policy" "test" {
  name        = %q
  description = "acctest policy"
  priority    = 100

  effect = {
    type                  = %q
    message               = "Acceptance test policy applied"
    rate_limit_per_minute = %d
  }
}
`, name, effect, ratePerMinute)
}

// configLLMPolicy carries its own provider instance: `providerInstances` is
// `@NotEmpty` on LlmPolicyCreateRequest, so a policy that governs nothing is a
// 400, not an empty-but-valid policy. The old fixture omitted it entirely.
func configLLMPolicy(name, operator string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_llm_provider" "policy_dep" {
  provider_type      = "anthropic"
  instance_name      = "%[1]s-inst"
  display_name       = "%[1]s instance"
  priority           = 100
  enabled            = true
  api_key            = "test-key"
  api_key_wo_version = 1
}

resource "ferentin_llm_policy" "test" {
  name               = %[1]q
  description        = "acctest llm policy"
  priority           = 100
  provider_instances = [ferentin_llm_provider.policy_dep.instance_id]

  criteria = [
    {
      operator    = %[2]q
      type        = "user"
      description = "All users in this acctest"
      conditions = [
        {
          field    = "email"
          operator = "endswith"
          # JSON-encoded, like every criteria value on every policy resource —
          # a bare "@example.com" is not valid JSON and fails at apply.
          value = jsonencode("@example.com")
        }
      ]
    }
  ]
}
`, name, operator)
}

func configOtelPolicy(policyName, sinkName, signalsHCL string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_otel_sink" "policy_dep" {
  name        = %q
  endpoint    = "https://otlp.example.com/v1/traces"
  sink_type   = "OTLP_HTTP"
  protocol    = "HTTP"
  compression = "gzip"
}

resource "ferentin_otel_policy" "test" {
  name        = %q
  description = "acctest otel policy"
  priority    = 100
  signals     = %s
  sink_ids    = [ferentin_otel_sink.policy_dep.sink_id]
}
`, sinkName, policyName, signalsHCL)
}

func configAIAgent(name string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_ai_agent" "test" {
  name                       = %q
  agent_platform             = "claude"
  application_type           = "SERVICE"
  token_endpoint_auth_method = "client_secret_basic"
  description                = "acctest agent"
  scopes                     = ["llm", "mcp"]
}
`, name)
}
