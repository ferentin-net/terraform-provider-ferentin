package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccMCPServerFromCard_basic covers the one resource the acceptance suite
// did not exercise: Create (card import) → Update (priority, no re-import) →
// Import → Destroy (which removes both the provider and the server).
//
// The card's name carries the run's suffix, because the platform dedupes an
// import by the card, and a fixed name would make a second run adopt the pair
// the first one left behind rather than create one.
func TestAccMCPServerFromCard_basic(t *testing.T) {
	name := "tf-acc-" + randomSuffix(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configMCPServerFromCard(name, 100),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ferentin_mcp_server_from_card.test", "provider_id"),
					resource.TestCheckResourceAttrSet("ferentin_mcp_server_from_card.test", "server_id"),
					resource.TestCheckResourceAttrSet("ferentin_mcp_server_from_card.test", "slug"),
					resource.TestCheckResourceAttr("ferentin_mcp_server_from_card.test", "endpoint", "https://mcp-acctest.ferentin.test/mcp"),
					resource.TestCheckResourceAttrPair(
						"ferentin_mcp_server_from_card.test", "edge_site_id",
						"ferentin_edge_site.test_site", "site_id"),
					resource.TestCheckResourceAttr("ferentin_mcp_server_from_card.test", "priority", "100"),
					resource.TestCheckResourceAttr("ferentin_mcp_server_from_card.test", "import_action", "created"),
				),
			},
			{
				Config: configMCPServerFromCard(name, 50),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ferentin_mcp_server_from_card.test", "priority", "50"),
				),
			},
			{
				ResourceName:      "ferentin_mcp_server_from_card.test",
				ImportState:       true,
				ImportStateVerify: true,
				// Read cannot recover these: the card and the credentials are
				// write-side inputs the platform does not return, instance_name
				// is honoured only on first import, and the import_* fields
				// describe the last import call, which an import did not make.
				ImportStateVerifyIgnore: []string{
					"card_json", "bearer_token", "env", "instance_name",
					"import_action", "import_unchanged",
				},
			},
		},
	})
}

func configMCPServerFromCard(name string, priority int) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_edge_site" "test_site" {
  site_id   = "%[1]s-site"
  site_name = "%[1]s site"
}

resource "ferentin_mcp_server_from_card" "test" {
  card_json = jsonencode({
    "$schema"   = "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json"
    name        = "net.ferentin.acctest/%[1]s"
    title       = "%[1]s"
    version     = "0.1.0"
    description = "Acceptance-test server card."
    remotes = [{
      type = "streamable-http"
      url  = "https://mcp-acctest.ferentin.test/mcp"
    }]
  })
  edge_site_id = ferentin_edge_site.test_site.site_id
  priority     = %[2]d
}
`, name, priority)
}
