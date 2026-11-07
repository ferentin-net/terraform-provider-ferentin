package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// Tenant-wide acceptance tests. They write the tenant default posture and the
// tenant's one sandbox policy, so run them only against a dev tenant: its
// devices see the change once someone activates.

// TestAccEndpointSandbox_lifecycle covers create, update, import and destroy,
// then a second create after the destroy. Destroy saves a policy that enables
// nothing (there is no DELETE), and Create adopts that shape: without that,
// the resource could never be created again.
//
// Skipped when the tenant already has an authored sandbox policy, which Create
// refuses to overwrite.
func TestAccEndpointSandbox_lifecycle(t *testing.T) {
	const r = "ferentin_endpoint_sandbox.test"
	cfg := func(desc string, copilotUnsandboxed bool) string {
		return providerBlock() + fmt.Sprintf(`
resource "ferentin_endpoint_sandbox" "test" {
  tools = {
    "codex"   = { enabled = true }
    "copilot" = { enabled = true, allow_unsandboxed = %[2]t }
  }
  filesystem = {
    writable = ["~/.cache/tf-acc"]
    denied   = ["~/.ssh", "~/.aws"]
  }
  allowed_domains = ["Registry.NPMjs.org", "pypi.org"]
  description     = %[1]q
}
`, desc, copilotUnsandboxed)
	}
	steps := []resource.TestStep{
		{
			Config: cfg("tf-acc sandbox", false),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(r, "tools.codex.enabled", "true"),
				resource.TestCheckResourceAttr(r, "include_derived_domains", "true"),
				resource.TestCheckResourceAttr(r, "managed_by", "iac"),
				resource.TestCheckResourceAttrSet(r, "version"),
			),
		},
		{
			Config: cfg("tf-acc sandbox v2", true),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(r, "description", "tf-acc sandbox v2"),
				resource.TestCheckResourceAttr(r, "tools.copilot.allow_unsandboxed", "true"),
			),
		},
		{
			ResourceName:            r,
			ImportState:             true,
			ImportStateVerify:       true,
			ImportStateVerifyIgnore: []string{"allowed_domains", "filesystem"},
		},
	}
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			skipIfAuthoredSandbox(t)
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
	// The first run's destroy left the inert policy; a fresh create adopts it.
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps[:1],
	})
}

func skipIfAuthoredSandbox(t *testing.T) {
	t.Helper()
	got, err := testAccSDK(t).EndpointSandbox().Get(context.Background(), os.Getenv(envTenantID))
	if err != nil {
		t.Fatalf("read sandbox policy: %v", err)
	}
	if got.Configured != nil && *got.Configured && !sandboxIsInert(got) {
		t.Skip("the tenant has an authored sandbox policy; this test would have to overwrite it")
	}
}

// TestAccMCPScopeDefaults_tenantDefault writes the tenant default's MCP scope
// defaults, a rehearsal included, and imports them. Destroy leaves the
// defaults in force, so the test puts back what the tenant had before it ran.
func TestAccMCPScopeDefaults_tenantDefault(t *testing.T) {
	const r = "ferentin_mcp_scope_defaults.tenant"
	restoreTenantScopeDefaults(t)
	cfg := func(defaults string) string {
		return providerBlock() + `
resource "ferentin_mcp_scope_defaults" "tenant" {
` + defaults + `
}
`
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Rehearse a block: devices still act on report_only.
				Config: cfg(`
  server_approval      = "block"
  server_approval_mode = "report_only"
  unattributed         = "report_only"
  tool_vetting_mode    = "enforce"
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "server_approval", "block"),
					resource.TestCheckResourceAttr(r, "server_approval_mode", "report_only"),
					resource.TestCheckResourceAttr(r, "remote_entry", "block"),
					resource.TestCheckNoResourceAttr(r, "device_group_id"),
					resource.TestCheckResourceAttrSet(r, "version"),
				),
			},
			{
				// Ending the rehearsal: the mode follows the value when unset.
				Config: cfg(`
  server_approval   = "report_only"
  unattributed      = "report_only"
  tool_vetting_mode = "enforce"
`),
				Check: resource.TestCheckResourceAttr(r, "server_approval_mode", "report_only"),
			},
			{
				ResourceName:      r,
				ImportState:       true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return os.Getenv(envTenantID), nil },
				ImportStateVerify: true,
			},
		},
	})
}

// restoreTenantScopeDefaults records the tenant default's MCP scope defaults
// and writes them back when the test ends.
func restoreTenantScopeDefaults(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		return
	}
	ctx := context.Background()
	sdk := testAccSDK(t)
	tenantID := os.Getenv(envTenantID)
	before, err := sdk.MCPScopeDefaults().GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("read tenant scope defaults: %v", err)
	}
	t.Cleanup(func() {
		now, err := sdk.MCPScopeDefaults().GetTenant(ctx, tenantID)
		if err != nil {
			t.Errorf("restore: read tenant scope defaults: %v", err)
			return
		}
		version := ""
		if now.Version != nil {
			version = strconv.FormatInt(*now.Version, 10)
		}
		w := adminapi.MCPScopeDefaultsWrite{
			ServerApproval: before.ServerApproval,
			Unattributed:   before.Unattributed,
		}
		if tv := before.ToolVetting; tv != nil && tv.Mode != nil {
			mode := gen.McpScopeToolVettingWriteMode(string(*tv.Mode))
			w.ToolVetting = &adminapi.MCPScopeToolVettingWrite{Mode: &mode}
		}
		if _, err := sdk.MCPScopeDefaults().PutTenant(ctx, tenantID, version, w); err != nil {
			t.Errorf("restore tenant scope defaults: %v", err)
		}
	})
}
