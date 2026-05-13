package provider

import (
	"fmt"
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
