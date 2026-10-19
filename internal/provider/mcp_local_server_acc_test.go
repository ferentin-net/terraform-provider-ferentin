package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccMCPLocalServer_basic declares a server from a template no device
// runs, blocks it in place, imports it, and deletes it. Needs a platform with
// ferentin-platform#3188 (version, provenance, DELETE).
func TestAccMCPLocalServer_basic(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_mcp_local_server.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configMCPLocalServer(suffix, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "status", "approved"),
					resource.TestCheckResourceAttr(r, "declared_via", "template"),
					resource.TestCheckResourceAttr(r, "kind", "local"),
					resource.TestCheckResourceAttr(r, "managed_by", "iac"),
					resource.TestCheckResourceAttr(r, "argv_template.#", "3"),
					resource.TestCheckResourceAttrSet(r, "server_id"),
					resource.TestCheckResourceAttrSet(r, "version"),
				),
			},
			{
				Config: configMCPLocalServer(suffix, `  status = "blocked"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "status", "blocked"),
					resource.TestCheckResourceAttr(r, "last_modified_by", "iac"),
				),
			},
			{
				ResourceName:            r,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"reason"},
			},
		},
	})
}

func configMCPLocalServer(suffix, extra string) string {
	return providerBlock() + fmt.Sprintf(`
resource "ferentin_mcp_local_server" "test" {
  display_name  = "tf-acc-local-%[1]s"
  argv_template = ["tf-acc-%[1]s", "serve", "*"]
  reason        = "terraform acceptance test"
%[2]s
}
`, suffix, extra)
}
