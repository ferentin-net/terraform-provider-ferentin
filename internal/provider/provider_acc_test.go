package provider

import (
	crand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"sync/atomic"
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
				//
				// `current_devices` is a runtime statistic, not configuration:
				// the create response omits it (state: null) while the GET the
				// import performs returns 0, so verify sees a difference that
				// says nothing about the mapping. It is Computed, so the value
				// settles on the next refresh either way.
				ImportStateVerifyIgnore: []string{"current_devices"},
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
					resource.TestCheckResourceAttr("ferentin_mcp_server.test", "deployment_mode", "edge_routed"),
					resource.TestCheckResourceAttrSet("ferentin_mcp_server.test", "edge_site_id"),
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

// configLLMProvider is deliberately explicit about display_name / priority /
// enabled. The platform defaults them, but a fixture that sends only the
// minimum makes a create failure ambiguous between "the provider built a bad
// body" and "the platform rejected the values", which is what made the
// DATA_INTEGRITY_VIOLATION in #6 hard to read.
func configLLMProvider(name, secret string, woVersion int) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_llm_provider" "test" {
  provider_type      = "anthropic"
  instance_name      = %[1]q
  display_name       = "%[1]s (acctest)"
  priority           = 100
  enabled            = true
  api_key            = %[2]q
  api_key_wo_version = %[3]d
}
`, name, secret, woVersion)
}

// configMCPServer stands the server up on a tenant provider the test creates
// itself.
//
// It used to look the catalog entry up with `data "ferentin_mcp_provider"`
// filtered by `slug`, which cannot work: that data source is keyed by
// `provider_id` (a UUID) and has no `slug` argument — resolving a catalog row
// by slug means filtering the plural `ferentin_mcp_providers`. Even fixed, it
// would make the test depend on the local-dev catalog shipping an `echo` row.
// A tenant-owned provider keeps the fixture self-contained.
//
// `allow_endpoint_override` is on because the server below sets its own
// `endpoint` (a required attribute the old fixture omitted entirely) rather
// than inheriting `default_url`.
//
// `edge_routed`, not `public`, and that is load-bearing rather than incidental
// coverage. On a `public` instance the platform runs the strict cloud-dial SSRF
// check, which RESOLVES the hostname and 400s with "Failed to resolve hostname"
// for anything that doesn't exist in DNS — so no invented host works, and a
// real one would make the suite depend on the outside world. `edge_routed`
// takes SsrfProtectionService's relaxed path (admin-api never dials these URLs;
// the customer's service-edge does), which is exactly why the interlock exists.
// It needs an `edge_site_id`, so the fixture creates its own site.
func configMCPServer(name string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_edge_site" "server_dep" {
  site_id   = "%[1]s-site"
  site_name = "%[1]s site"
}

resource "ferentin_mcp_provider" "server_dep" {
  display_name            = "%[1]s-provider"
  description             = "acctest provider backing %[1]s"
  transport               = "http"
  default_url             = "https://mcp-acctest.ferentin.test/mcp"
  allow_endpoint_override = true
}

resource "ferentin_mcp_server" "test" {
  name                   = %[1]q
  provider_id            = ferentin_mcp_provider.server_dep.provider_id
  endpoint               = "https://mcp-acctest.ferentin.test/mcp"
  transport_type         = "streamable_http"
  deployment_mode        = "edge_routed"
  edge_site_id           = ferentin_edge_site.server_dep.synthetic_id
  upstream_auth_strategy = "none"
}
`, name)
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

// acctestRunID tags every name produced by one `go test` process.
//
// The previous scheme — `time.Now().UnixMilli() % 1_000_000` — repeats every
// 1000 seconds, and two calls in the same millisecond return the SAME value.
// Both matter here. Names are unique per tenant, the platform has no TTL on
// test rows, and a run killed part-way (the token-endpoint rate limit in #6
// killed one) leaves rows behind that nothing cleans up. Re-running inside the
// same ~16-minute window then replays a suffix straight into a uniqueness
// violation on create — which is what a `400 DATA_INTEGRITY_VIOLATION` on a
// fixture that reads as obviously-unique actually was.
//
// A per-process random tag plus a monotonic counter removes both collision
// modes: distinct across runs, distinct within a run regardless of clock
// resolution.
var acctestRunID = func() string {
	var b [4]byte
	if _, err := crand.Read(b[:]); err != nil {
		// A CSPRNG read should not be able to fail a test run; the clock is a
		// weaker but adequate fallback for a name tag.
		binary.LittleEndian.PutUint32(b[:], uint32(time.Now().UnixNano()))
	}
	return hex.EncodeToString(b[:])
}()

var acctestSeq atomic.Uint32

// randomSuffix yields a suffix unique to this process and to each call, so
// retried or interleaved runs don't collide on platform-side name uniqueness.
// Lowercase hex + digits keeps it legal everywhere the provider accepts a
// slug-shaped identifier (`site_id`, `instance_name`, group names).
func randomSuffix(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%s%02d", acctestRunID, acctestSeq.Add(1))
}
