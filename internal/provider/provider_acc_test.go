package provider

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acceptance tests for the Ferentin Terraform provider.
//
// Gated by `TF_ACC=1`; otherwise resource.Test skips them. They expect a
// live platform reachable at FERENTIN_ENDPOINT with credentials in either
// FERENTIN_TOKEN (static) or FERENTIN_CLIENT_ID + FERENTIN_CLIENT_SECRET
// (refreshing client_credentials). Required for every test:
//
//   FERENTIN_ENDPOINT   — e.g. https://api.local.ferentin.test
//   FERENTIN_TENANT_ID  — tenant UUID
//   <one of>:
//     FERENTIN_TOKEN
//     FERENTIN_CLIENT_ID + FERENTIN_CLIENT_SECRET (+ optional FERENTIN_AUTH_URL)
//
// Optional:
//   FERENTIN_INSECURE_SKIP_VERIFY=1   for self-signed local certs

// testAccProtoV6ProviderFactories yields the provider under test wrapped as a
// proto-v6 server, as required by terraform-plugin-testing for framework-
// based providers.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"ferentin": providerserver.NewProtocol6WithError(New("acctest")()),
}

// testAccPreCheck asserts the env is set up for a real apply. Each
// acceptance test calls it from PreCheck so missing config fails fast with
// a clear message instead of an opaque provider error.
func testAccPreCheck(t *testing.T) {
	t.Helper()
	required := []string{"FERENTIN_ENDPOINT", "FERENTIN_TENANT_ID"}
	for _, k := range required {
		if os.Getenv(k) == "" {
			t.Fatalf("%s must be set for acceptance tests", k)
		}
	}
	hasToken := os.Getenv("FERENTIN_TOKEN") != ""
	hasCC := os.Getenv("FERENTIN_CLIENT_ID") != "" && os.Getenv("FERENTIN_CLIENT_SECRET") != ""
	if !hasToken && !hasCC {
		t.Fatal("set FERENTIN_TOKEN, or both FERENTIN_CLIENT_ID and FERENTIN_CLIENT_SECRET, for acceptance tests")
	}
}

// TestAccEdgeSite_basic exercises the Phase 2 reference resource end-to-end:
// Create → Read → Update (rename / re-tag) → Import → Destroy.
func TestAccEdgeSite_basic(t *testing.T) {
	siteID := "tf-acc-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configEdgeSite(siteID, "Acc test "+siteID, "primary"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_edge_site.test", "site_id", siteID),
					resource.TestCheckResourceAttr("ferentin_edge_site.test", "site_name", "Acc test "+siteID),
					resource.TestCheckResourceAttr("ferentin_edge_site.test", "verify_upstream_tls", "true"),
					resource.TestCheckResourceAttr("ferentin_edge_site.test", "allow_http_upstream", "false"),
					resource.TestCheckResourceAttr("ferentin_edge_site.test", "bundle_cloud_mcp", "true"),
					resource.TestCheckResourceAttrSet("ferentin_edge_site.test", "synthetic_id"),
				),
			},
			{
				Config: configEdgeSite(siteID, "Acc test "+siteID+" renamed", "secondary"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_edge_site.test", "site_name", "Acc test "+siteID+" renamed"),
					resource.TestCheckResourceAttr("ferentin_edge_site.test", "tags.tier", "secondary"),
				),
			},
			{
				ResourceName:      "ferentin_edge_site.test",
				ImportState:       true,
				ImportStateVerify: true,
				// id format is "<tenant>/<site_id>" — auto-derived from state.
			},
		},
	})
}

// TestAccLLMProvider_basic exercises WriteOnly secret handling on the
// ferentin_llm_provider resource (tenant-scoped binding). Create with
// api_key in config, verify state never carries it, then rotate via
// api_key_wo_version bump.
func TestAccLLMProvider_basic(t *testing.T) {
	name := "tf-acc-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configLLMProvider(name, "test-key-v1", 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_llm_provider.test", "instance_name", name),
					resource.TestCheckResourceAttr("ferentin_llm_provider.test", "api_key_configured", "true"),
					resource.TestCheckResourceAttr("ferentin_llm_provider.test", "api_key_wo_version", "1"),
					// WriteOnly: state must NOT carry the literal value.
					resource.TestCheckNoResourceAttr("ferentin_llm_provider.test", "api_key"),
				),
			},
			{
				// Rotate: same instance, bump version → secret re-sent.
				Config: configLLMProvider(name, "test-key-v2", 2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_llm_provider.test", "api_key_wo_version", "2"),
				),
			},
		},
	})
}

// TestAccMCPServer_basic exercises Phase 3's most complex resource:
// Create → Read → Update → Import → Destroy. Verifies enum validators
// don't reject the canonical values.
func TestAccMCPServer_basic(t *testing.T) {
	name := "tf-acc-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configMCPServer(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_mcp_server.test", "name", name),
					resource.TestCheckResourceAttr("ferentin_mcp_server.test", "transport_type", "streamable_http"),
					resource.TestCheckResourceAttr("ferentin_mcp_server.test", "deployment_mode", "public"),
					resource.TestCheckResourceAttr("ferentin_mcp_server.test", "enabled", "true"),
				),
			},
			{
				ResourceName:      "ferentin_mcp_server.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccOtelSink_basic covers the OTEL sink path including its enum
// validators (sink_type, protocol, compression).
func TestAccOtelSink_basic(t *testing.T) {
	name := "tf-acc-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configOtelSink(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_otel_sink.test", "name", name),
					resource.TestCheckResourceAttr("ferentin_otel_sink.test", "sink_type", "OTLP_HTTP"),
					resource.TestCheckResourceAttr("ferentin_otel_sink.test", "protocol", "HTTP"),
					resource.TestCheckResourceAttr("ferentin_otel_sink.test", "compression", "gzip"),
					resource.TestCheckResourceAttr("ferentin_otel_sink.test", "enabled", "true"),
				),
			},
		},
	})
}

// --- HCL fixtures ---------------------------------------------------------

func configEdgeSite(siteID, name, tier string) string {
	return providerBlock() + `
resource "ferentin_edge_site" "test" {
  site_id   = "` + siteID + `"
  site_name = "` + name + `"
  tags = {
    "tier"        = "` + tier + `"
    "managed-by"  = "terraform-acc-test"
  }
}
`
}

func configLLMProvider(name, secret string, woVersion int) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_llm_provider" "test" {
  provider_type      = "anthropic"
  instance_name      = %q
  api_key            = %q
  api_key_wo_version = %d
}
`, name, secret, woVersion)
}

func configMCPServer(name string) string {
	return providerBlock() + `
data "ferentin_mcp_provider" "echo" {
  slug = "echo"
}

resource "ferentin_mcp_server" "test" {
  name            = "` + name + `"
  provider_id     = data.ferentin_mcp_provider.echo.id
  transport_type  = "streamable_http"
  deployment_mode = "public"
  upstream_auth_strategy = "none"
}
`
}

func configOtelSink(name string) string {
	return providerBlock() + `
resource "ferentin_otel_sink" "test" {
  name        = "` + name + `"
  endpoint    = "https://otlp.example.com/v1/traces"
  sink_type   = "OTLP_HTTP"
  protocol    = "HTTP"
  compression = "gzip"
}
`
}

// providerBlock yields the HCL provider stanza, deferring fully to env-var
// fallback so acctests pick up FERENTIN_* from the runner.
func providerBlock() string {
	return `
provider "ferentin" {
  # endpoint / tenant_id / token (or client_id+client_secret) come from env.
}
`
}

// randomSuffix yields a short unique suffix so concurrent / retried runs
// don't collide on platform-side name uniqueness.
func randomSuffix(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%06d", time.Now().UnixMilli()%1_000_000)
}
