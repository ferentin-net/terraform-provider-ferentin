package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccMCPScopeDefaults_deviceGroup covers ferentin_mcp_scope_defaults on a
// device group (so the tenant default is never touched), beside the group's
// ferentin_endpoint_policy_settings, which writes the same posture row. Each
// step relies on the framework's post-apply empty-plan check:
//
//  1. Every default set, server approval rehearsed, with approval routing whose
//     JSON the platform stores in its own form.
//  2. Both resources change in one apply, so whichever writes second meets a
//     version the first moved: the provider must retry it, not fail with 412.
//     The rehearsal ends (server_approval_mode unset: enforce).
//  3. The risk policy and approval routing left out of config are kept.
//  4. Import, with nothing ignored.
func TestAccMCPScopeDefaults_deviceGroup(t *testing.T) {
	suffix := randomSuffix(t)
	const r = "ferentin_mcp_scope_defaults.group"
	cfg := func(quic bool, defaults string) string {
		return fmt.Sprintf(`
resource "ferentin_device_group" "test" {
  name   = "tf-acc-dg-scope-%[1]s"
  source = "manual"
}

resource "ferentin_endpoint_policy_settings" "group" {
  device_group_id    = ferentin_device_group.test.group_id
  quic_block_enabled = %[2]t
}

resource "ferentin_mcp_scope_defaults" "group" {
  device_group_id = ferentin_device_group.test.group_id
%[3]s
  # One writer creates the group's posture row.
  depends_on = [ferentin_endpoint_policy_settings.group]
}
`, suffix, quic, defaults)
	}
	const approval = `
  tool_vetting_approval_config = jsonencode({
    step_up_mode    = "approve"
    timeout_seconds = 900
    channels        = [{ type = "invocation_context" }]
  })
`
	const risk = `
  tool_vetting_risk_policy = {
    low    = "allow"
    medium = "ask_once"
    high   = "deny"
  }
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(false, `
  server_approval      = "block"
  server_approval_mode = "report_only"
  unattributed         = "report_only"
  tool_vetting_mode    = "enforce"
`+risk+approval),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "server_approval", "block"),
					resource.TestCheckResourceAttr(r, "server_approval_mode", "report_only"),
					resource.TestCheckResourceAttr(r, "remote_entry", "block"),
					resource.TestCheckResourceAttr(r, "unattributed_mode", "report_only"),
					resource.TestCheckResourceAttr(r, "source", "device_group"),
					resource.TestCheckResourceAttr(r, "tool_vetting_risk_policy.medium", "ask_once"),
					resource.TestCheckResourceAttr(r, "tool_vetting_risk_policy_source", "device_group"),
					resource.TestCheckResourceAttrSet(r, "version"),
				),
			},
			{
				Config: cfg(true, `
  server_approval   = "block"
  unattributed      = "report_only"
  tool_vetting_mode = "enforce"
`+risk+approval),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "server_approval_mode", "enforce"),
					resource.TestCheckResourceAttr("ferentin_endpoint_policy_settings.group",
						"quic_block_enabled", "true"),
				),
			},
			{
				Config: cfg(true, `
  server_approval   = "block"
  unattributed      = "block"
  tool_vetting_mode = "report_only"
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "unattributed", "block"),
					resource.TestCheckResourceAttr(r, "tool_vetting_risk_policy.medium", "ask_once"),
					resource.TestCheckResourceAttr(r, "tool_vetting_approval_config",
						`{"channels":[{"type":"invocation_context"}],"step_up_mode":"approve","timeout_seconds":900}`),
				),
			},
			{
				ResourceName:      r,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// The scope defaults write after the settings (depends_on), which
				// moves the row's version under the settings resource's state. The
				// post-test destroy does not refresh, and the settings Delete reports
				// a 412 rather than retrying past it (a DELETE also removes the
				// group's scope defaults), so refresh first, as `terraform destroy`
				// does by default.
				RefreshState: true,
			},
		},
	})
}
