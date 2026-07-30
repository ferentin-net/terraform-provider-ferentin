package provider

import (
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
		PreCheck:                 func() { testAccPreCheck(t) },
		ErrorCheck:               skipIfForbidden(t, "idps:rw"),
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
		PreCheck:                 func() { testAccPreCheck(t) },
		ErrorCheck:               skipIfForbidden(t, "idps:rw"),
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
					resource.TestCheckResourceAttr("ferentin_workload_identity_provider.test", "workload_identity ", "true"),
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

// skipIfForbidden turns the platform's own 403 into a skip.
//
// `idps:rw` is deliberately absent from `ferentin.iac.operator`: platform
// migration 783 calls identity scopes "the highest blast-radius IaC mistake"
// and says to mint a custom role if you need them, alongside the
// policy:activate separation-of-duties split. Widening the seeded IaC role so
// these two tests go green would dissolve a boundary the platform team drew on
// purpose, so they skip instead — a red suite everyone learns to ignore is the
// problem #6 exists to fix, and a skip that states its reason is honest about
// what this principal was allowed to exercise.
//
// Keyed off the 403 rather than the token's `scope` claim, which is NOT the
// authority: the platform's hasScopeInTenant consults the role binding for the
// tenant, so a token can carry a scope the principal cannot exercise there.
// Asking the API is the only answer that matches what the resource will do.
//
// Set FERENTIN_ACC_IDENTITY_SCOPES=1 when running as a principal that does hold
// the scope — then a 403 is a real failure and is reported as one.
func skipIfForbidden(t *testing.T, scope string) resource.ErrorCheckFunc {
	t.Helper()
	return func(err error) error {
		if err == nil || os.Getenv("FERENTIN_ACC_IDENTITY_SCOPES") == "1" {
			return err
		}
		if strings.Contains(err.Error(), "insufficient permissions") ||
			strings.Contains(err.Error(), "forbidden: admin-api 403") {
			t.Skipf("principal cannot exercise %s in this tenant — the seeded "+
				"ferentin.iac.operator role excludes identity scopes by design. Run as a "+
				"principal that holds it with FERENTIN_ACC_IDENTITY_SCOPES=1 to test this "+
				"resource for real.", scope)
		}
		return err
	}
}
