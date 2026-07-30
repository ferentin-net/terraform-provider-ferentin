package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acctests for workload OAuth clients and workload identity providers.
// Gated by TF_ACC=1. The IdP-bound endpoints (issuer, JWKS) point at
// well-known public discovery URLs so the tests don't need a customer IdP
// to converge against.

func TestAccWorkloadOAuthClient_basic(t *testing.T) {
	name := "tf-acc-workload-cc-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); requireScope(t, "idps:rw") },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configWorkloadOAuthClient(name, "test-secret-v1", 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_workload_oauth_client.test", "name", name),
					resource.TestCheckResourceAttr("ferentin_workload_oauth_client.test", "idp_type", "generic_oidc"),
					resource.TestCheckResourceAttr("ferentin_workload_oauth_client.test", "auth_method", "client_secret_basic"),
					resource.TestCheckResourceAttr("ferentin_workload_oauth_client.test", "audience_param_strategy", "audience_param"),
					resource.TestCheckResourceAttr("ferentin_workload_oauth_client.test", "is_active", "true"),
					resource.TestCheckResourceAttr("ferentin_workload_oauth_client.test", "client_secret_wo_version", "1"),
					// WriteOnly: state must NOT carry the literal value.
					resource.TestCheckNoResourceAttr("ferentin_workload_oauth_client.test", "client_secret"),
					resource.TestCheckResourceAttr("ferentin_workload_oauth_client.test", "has_client_secret", "true"),
				),
			},
			{
				// Rotate: bump wo_version → secret re-sent.
				Config: configWorkloadOAuthClient(name, "test-secret-v2", 2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_workload_oauth_client.test", "client_secret_wo_version", "2"),
				),
			},
			{
				ResourceName:            "ferentin_workload_oauth_client.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"client_secret", "client_secret_wo_version"},
			},
		},
	})
}

func TestAccWorkloadIdentityProvider_basic(t *testing.T) {
	name := "tf-acc-wip-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); requireScope(t, "idps:rw") },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configWorkloadIdentityProvider(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_workload_identity_provider.test", "name", name),
					resource.TestCheckResourceAttr("ferentin_workload_identity_provider.test", "cloud_provider", "aws"),
					resource.TestCheckResourceAttr("ferentin_workload_identity_provider.test", "protocol_type", "WORKLOAD_IDENTITY"),
					resource.TestCheckResourceAttr("ferentin_workload_identity_provider.test", "active", "true"),
					resource.TestCheckResourceAttr("ferentin_workload_identity_provider.test", "aws", "true"),
					resource.TestCheckResourceAttr("ferentin_workload_identity_provider.test", "workload_identity", "true"),
				),
			},
			{
				ResourceName:      "ferentin_workload_identity_provider.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// --- HCL fixtures ---------------------------------------------------------

func configWorkloadOAuthClient(name, secret string, woVersion int) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_workload_oauth_client" "test" {
  name                    = %q
  description             = "acctest workload OAuth client"
  idp_type                = "generic_oidc"
  auth_method             = "client_secret_basic"
  audience_param_strategy = "audience_param"
  oauth_client_id         = "acctest-client-id"
  issuer                  = "https://example.com"
  jwks_uri                = "https://example.com/.well-known/jwks.json"
  token_endpoint          = "https://example.com/oauth/token"

  client_secret            = %q
  client_secret_wo_version = %d

  default_audience = "https://example.com/mcp"
  default_scopes   = "read"
}
`, name, secret, woVersion)
}

func configWorkloadIdentityProvider(name string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_workload_identity_provider" "test" {
  name              = %q
  description       = "acctest AWS EKS trust"
  cloud_provider    = "aws"
  protocol_type     = "WORKLOAD_IDENTITY"
  jwks_uri          = "https://sts.amazonaws.com/.well-known/jwks.json"
  allowed_issuers   = ["https://sts.amazonaws.com"]
  expected_audiences = ["sts.amazonaws.com"]
  identity_claim    = "sub"
}
`, name)
}

// requireScope skips the calling test when the acceptance principal cannot
// hold the scope the resource needs.
//
// This is not a workaround for a misconfigured environment. `idps:rw` is
// deliberately absent from `ferentin.iac.operator` — platform migration 783
// calls identity scopes "the highest blast-radius IaC mistake" and tells you to
// mint a custom role if you genuinely need them. Widening the seeded IaC role
// so a test goes green would dissolve a separation-of-duties boundary the
// platform team drew on purpose.
//
// So: skip, loudly, rather than fail. A red suite that everyone learns to
// ignore is the problem issue #6 exists to fix; a skip with a reason is
// honest about what this principal was allowed to exercise.
//
// To run these deliberately, authenticate as a principal that legitimately
// carries the scope (a tenant admin, or a purpose-built role) and set
// FERENTIN_ACC_IDENTITY_SCOPES=1.
func requireScope(t *testing.T, scope string) {
	t.Helper()

	if os.Getenv("FERENTIN_ACC_IDENTITY_SCOPES") == "1" {
		return
	}

	// A static token can be inspected directly — no need for the opt-in when
	// the principal demonstrably has the scope.
	if tok := os.Getenv("FERENTIN_TOKEN"); tok != "" {
		if scopes, err := scopesFromJWT(tok); err == nil {
			for _, s := range scopes {
				if s == scope {
					return
				}
			}
			t.Skipf("principal lacks %s (token scopes: %s) — see requireScope; "+
				"set FERENTIN_ACC_IDENTITY_SCOPES=1 to run anyway", scope, strings.Join(scopes, " "))
		}
	}

	t.Skipf("cannot confirm the principal holds %s. The seeded ferentin.iac.operator role "+
		"deliberately excludes identity scopes; set FERENTIN_ACC_IDENTITY_SCOPES=1 to run "+
		"these against a principal that has them", scope)
}

// scopesFromJWT reads the `scope` claim (space-delimited, per RFC 8693) from an
// unverified access token. Test-only: the platform is the trust boundary, and
// the worst case here is skipping a test that would have run.
func scopesFromJWT(token string) ([]string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT (got %d parts)", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode JWT payload: %w", err)
	}
	var claims struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parse JWT claims: %w", err)
	}
	return strings.Fields(claims.Scope), nil
}
