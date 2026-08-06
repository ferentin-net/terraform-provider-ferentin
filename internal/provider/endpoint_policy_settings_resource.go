package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// A platform column default, mirrored here as a schema default so a config that
// omits the field plans to the same value the server would pick.
//
// Mirroring a server default into a provider constant is a liability, and the list
// is one entry shorter than it used to be for that reason. A `Default` on an
// Optional+Computed attribute is a silent data migration: when the mirrored value
// changes, every config that omits the attribute plans the new value and APPLIES it
// to rows that already exist. GH #2127 moved `unapproved_mcp_action` server-side, so
// that attribute dropped its Default and now defers to the server via
// Computed + UseStateForUnknown (see the schema block). Prefer that shape for
// anything new; keep a mirrored Default only where an omitted value genuinely must
// resolve client-side.
//
// NOTE: this is NOT what Delete writes. Destroy deliberately leaves the
// tenant-default row enforcing whatever was last applied; see Delete.
const (
	postureDefaultDestinationAction = "allow"
)

// EndpointPolicySettingsResource is the `ferentin_endpoint_policy_settings`
// Terraform resource — endpoint posture for a tenant, or for one device group
// (platform#2010, IaC-ready per platform#2038).
//
// # Why this resource looks different from every other one here
//
// The platform API is UPSERT-ONLY: there is no POST, no GET-by-id, and identity
// is (tenant_id, device_group_id) enforced by two partial unique indexes rather
// than a surrogate key. That drives four deliberate deviations:
//
//  1. Create is a PUT that may ADOPT a pre-existing row. A tenant that
//     configured posture in the console before adopting Terraform will find
//     that `terraform apply` takes ownership of the existing row rather than
//     failing on conflict. `managed_by` still reports the original creator, so
//     the adoption is visible.
//  2. Read LISTS /settings and filters on device_group_id (null ⇒ the tenant
//     default). There is no single-row endpoint to call.
//  3. `device_group_id` is RequiresReplace — it is the identity, not a field.
//  4. Delete is a NO-OP for the tenant-default row — the platform refuses to
//     delete it (every device, including ungrouped ones, resolves through it),
//     so Terraform drops it from state and leaves it enforcing. Group overrides
//     get a real DELETE. See Delete for why this is not a reset-to-defaults.
type EndpointPolicySettingsResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type EndpointPolicySettingsResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	DeviceGroupID types.String `tfsdk:"device_group_id"`

	UnapprovedMcpAction      types.String `tfsdk:"unapproved_mcp_action"`
	McpGatewayURL            types.String `tfsdk:"mcp_gateway_url"`
	DefaultDestinationAction types.String `tfsdk:"default_destination_action"`
	EchStripEnabled          types.Bool   `tfsdk:"ech_strip_enabled"`
	DohBlockEnabled          types.Bool   `tfsdk:"doh_block_enabled"`
	QuicBlockEnabled         types.Bool   `tfsdk:"quic_block_enabled"`

	SettingsID types.String `tfsdk:"settings_id"`
	CreatedAt  types.String `tfsdk:"created_at"`
	UpdatedAt  types.String `tfsdk:"updated_at"`

	// IaC readiness (platform#2038).
	Version           types.Int64  `tfsdk:"version"` // for If-Match
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

func NewEndpointPolicySettingsResource() resource.Resource {
	return &EndpointPolicySettingsResource{}
}

var (
	_ resource.Resource                = (*EndpointPolicySettingsResource)(nil)
	_ resource.ResourceWithConfigure   = (*EndpointPolicySettingsResource)(nil)
	_ resource.ResourceWithImportState = (*EndpointPolicySettingsResource)(nil)
)

func (r *EndpointPolicySettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_endpoint_policy_settings"
}

func (r *EndpointPolicySettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected ProviderData, got %T", req.ProviderData))
		return
	}
	r.sdk = pd.SDK
	r.tenantID = pd.TenantID
}

func (r *EndpointPolicySettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Endpoint posture for managed devices — the unapproved-MCP action, the MCP " +
			"gateway target, the DNS/QUIC network flags, and the default action for AI destinations with " +
			"no matching `ferentin_endpoint_destination_rule`.\n\n" +
			"Omit `device_group_id` for the **tenant default** row (which every device, including " +
			"ungrouped ones, resolves through). Set it for a **per-group override**.\n\n" +
			"~> **This resource is an upsert, not a create.** The platform API has no POST and no " +
			"GET-by-id; identity is `(tenant_id, device_group_id)`. Consequences:\n\n" +
			"* If posture already exists (e.g. configured in the console), `terraform apply` **adopts** " +
			"the existing row rather than failing. `managed_by` still reports the original creator, so " +
			"the adoption is visible as drift.\n" +
			"* `terraform destroy` on the **tenant default** row does not delete it and does not " +
			"change it — the platform refuses to delete a row every device resolves through. " +
			"Terraform drops it from state and **the fleet keeps enforcing the last applied " +
			"posture**, with a warning saying so. This is fail-closed on purpose: resetting to the " +
			"permissive defaults would silently de-enforce the whole fleet, and removing the " +
			"resource block or dropping a module reaches the same code path as an explicit " +
			"destroy. To stand enforcement down, apply the permissive posture first, then " +
			"destroy. A group override *is* genuinely deleted, and the group falls back to the " +
			"tenant default.\n\n" +
			"## Import\n\n" +
			"Tenant default row:\n\n" +
			"```\n" +
			"terraform import ferentin_endpoint_policy_settings.default <tenant_id>\n" +
			"```\n\n" +
			"Per-group override:\n\n" +
			"```\n" +
			"terraform import ferentin_endpoint_policy_settings.contractors <tenant_id>/<device_group_id>\n" +
			"```",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant_id": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"device_group_id": schema.StringAttribute{
				MarkdownDescription: "Device group this posture overrides — reference " +
					"`ferentin_device_group.<name>.group_id`. Omit for the tenant default row.\n\n" +
					"This is part of the resource **identity**, not a mutable field: changing it forces " +
					"replacement, because a different value addresses a different row.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			"unapproved_mcp_action": schema.StringAttribute{
				MarkdownDescription: "What the agent does with an MCP server config it finds on a device " +
					"that is not approved:\n\n" +
					"* `report_only` — report it in telemetry, change nothing on the machine\n" +
					"* `quarantine` — move the offending config aside so the client cannot load it " +
					"(default). The entry is preserved verbatim in a root-owned sidecar before " +
					"removal, and is not removed if it cannot be preserved\n" +
					"* `block` — block the server's traffic outright (requires the NetworkExtension " +
					"content filter; without it the agent can report but not enforce)",
				// NO schema Default, deliberately — unlike its sibling below.
				//
				// A `Default` on an Optional+Computed attribute is a silent data
				// migration: a config that omits the field plans to the constant and
				// APPLIES it. When platform#2127 moved the server default to
				// "quarantine", that turned every IaC-managed tenant with an existing
				// row into a plan of `report_only -> quarantine`, applied on the next
				// run — precisely the migration changeset 1248 refused to perform,
				// because an implicit `report_only` and a deliberate one are the same
				// bytes and there is no way to tell them apart.
				//
				// Computed + UseStateForUnknown instead: omitting the attribute keeps
				// whatever the tenant already has, and a brand-new row gets whatever
				// default the SERVER picks. That is also the better shape generally —
				// mirroring a server-side default into a provider constant is what
				// created the drift in the first place, and this attribute no longer
				// has a second definition to fall out of step with.
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.OneOf("report_only", "quarantine", "block")},
			},
			"mcp_gateway_url": schema.StringAttribute{
				MarkdownDescription: "Tenant MCP gateway base URL that approved server configs are " +
					"rewritten to point at. Unset = the agent reports what it finds and rewrites nothing.\n\n" +
					"Must be `https://` — an `http://` value would downgrade every governed MCP session " +
					"on the fleet to cleartext, which is strictly worse than the ungoverned status quo it " +
					"replaces. The platform rejects it; this fails at plan.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(httpsOnlyURL,
						"must be an https:// URL — http:// would downgrade governed MCP sessions to cleartext"),
				},
			},
			"default_destination_action": schema.StringAttribute{
				MarkdownDescription: "Action for an AI destination with no matching destination rule: " +
					"`allow` (default) or `block`. Setting `block` makes the rule set an allowlist — " +
					"verify your rules cover every sanctioned provider first.",
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString(postureDefaultDestinationAction),
				Validators: []validator.String{stringvalidator.OneOf("allow", "block")},
			},
			"ech_strip_enabled": schema.BoolAttribute{
				MarkdownDescription: "Strip HTTPS/SVCB DNS records so Encrypted Client Hello is not " +
					"advertised, keeping SNI inspectable. Default `false`.",
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
			},
			"doh_block_enabled": schema.BoolAttribute{
				MarkdownDescription: "Block external DNS-over-HTTPS resolvers so name resolution stays " +
					"observable. Default `false`.",
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
			},
			"quic_block_enabled": schema.BoolAttribute{
				MarkdownDescription: "Block QUIC / UDP-443 to force TCP fallback where SNI is " +
					"inspectable. Default `false`.",
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
			},

			"settings_id": schema.StringAttribute{
				MarkdownDescription: "Platform row UUID. Informational — the resource is addressed by " +
					"`(tenant_id, device_group_id)`, not by this id.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{Computed: true},

			// ===== IaC-readiness attributes (platform#2038) =====
			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version, threaded as `If-Match` on " +
					"update, and on delete of a group override (the tenant-default row is never " +
					"deleted, so nothing is sent for it).\n\n" +
					"~> On this upsert resource the platform treats an `If-Match` against a row that " +
					"does **not** exist as a failed precondition (412), not a create — it refuses to " +
					"resurrect a deleted row at version 0 with fresh provenance. The provider therefore " +
					"sends no `If-Match` on the first write.",
				Computed: true,
			},
			"managed_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the original creator (`iac` for this provider). " +
					"Immutable after create. On an adopted row this reports whoever created it first — " +
					"`console`, for posture configured in the admin console before Terraform took over.",
				Computed: true,
			},
			"managed_by_client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth2 `client_id` of the creator, from the authenticated principal.",
				Computed:            true,
			},
			"managed_by_module": schema.StringAttribute{
				MarkdownDescription: "Module label this provider sent via `X-Ferentin-Managed-By-Module`.",
				Computed:            true,
			},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent writer. Divergence from `managed_by` " +
					"is the drift signal.",
				Computed: true,
			},
		},
	}
}

func (r *EndpointPolicySettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan EndpointPolicySettingsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	// No If-Match on the first write: the row may not exist, and on this upsert
	// the platform answers an If-Match against a missing row with 412.
	row, err := r.upsert(ctx, tenantID, plan.DeviceGroupID, "", plan.toWrite())
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to write endpoint policy settings", err)
		return
	}
	state := endpointSettingsToModel(tenantID, row)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *EndpointPolicySettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state EndpointPolicySettingsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	row, err := r.fetch(ctx, tenantID, state.DeviceGroupID)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			// For the tenant-default row ErrNotFound means "the tenant has
			// authored no posture", which after a destroy-reset is the expected
			// end state; either way the row Terraform tracked is gone.
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read endpoint policy settings", err)
		return
	}
	refreshed := endpointSettingsToModel(tenantID, row)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *EndpointPolicySettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state EndpointPolicySettingsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	row, err := r.upsert(ctx, tenantID, state.DeviceGroupID, version, plan.toWrite())
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update endpoint policy settings", err)
		return
	}
	refreshed := endpointSettingsToModel(tenantID, row)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

// Delete has two behaviours because the platform has two shapes of row.
//
//   - A GROUP OVERRIDE is genuinely deleted. The group then falls back to the
//     tenant default, which is real inheritance, not a weakening.
//
//   - The TENANT DEFAULT row cannot be deleted — the platform returns 400,
//     because every device including ungrouped ones resolves through it. So
//     destroy DROPS IT FROM STATE AND LEAVES THE ROW ENFORCING AS-IS.
//
// # Why not reset it to the platform defaults
//
// Resetting to `report_only` / `allow` / flags-off is tempting: it looks like
// "undo the Terraform-managed configuration". It is fail-OPEN, and the path is
// reachable by ordinary refactoring — deleting the resource block from HCL, or
// dropping a module, runs Delete just as `terraform destroy` does. A fleet
// would silently fall from "quarantine unapproved MCP servers, allowlist AI
// destinations" to "observe only", arriving on-device as a routine bundle
// update with no operator signal beyond an audit row.
//
// Leaving the row alone matches how Terraform providers handle an adopted
// singleton they cannot delete: `aws_default_vpc` drops state and touches
// nothing, and `aws_default_security_group` resets to MAXIMALLY RESTRICTIVE.
// Neither resets to permissive.
//
// Standing enforcement down is therefore an explicit two-step an operator has
// to mean: apply the permissive posture, then destroy. Both steps are audited.
func (r *EndpointPolicySettingsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state EndpointPolicySettingsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	if !isUnsetString(state.DeviceGroupID) {
		version := strconv.FormatInt(state.Version.ValueInt64(), 10)
		err := r.sdk.EndpointPolicies().DeleteGroupSettings(ctx, tenantID,
			state.DeviceGroupID.ValueString(), version)
		if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
			addSDKError(&resp.Diagnostics, "Failed to delete endpoint policy settings override", err)
		}
		return
	}

	// Tenant default: no API call at all. Terraform removes the resource from
	// state when Delete returns without error.
	resp.Diagnostics.AddWarning(
		"Tenant endpoint posture removed from state, but STILL IN FORCE",
		fmt.Sprintf("The platform does not allow deleting the tenant-default endpoint posture — every "+
			"managed device, including ungrouped ones, resolves through it. Terraform has stopped "+
			"managing the row; the fleet continues to enforce the last applied posture "+
			"(unapproved_mcp_action = %q, default_destination_action = %q).\n\n"+
			"This is deliberate: resetting to the permissive defaults on destroy would silently "+
			"de-enforce the whole fleet, and removing a resource block or dropping a module reaches "+
			"this path just as `terraform destroy` does.\n\n"+
			"To actually stand enforcement down, do it explicitly and then destroy:\n"+
			"  1. Set unapproved_mcp_action = \"report_only\", default_destination_action = \"allow\", "+
			"clear mcp_gateway_url, and turn the network flags off.\n"+
			"  2. terraform apply\n"+
			"  3. terraform destroy (or remove the resource block)",
			state.UnapprovedMcpAction.ValueString(), state.DefaultDestinationAction.ValueString()))
}

func (r *EndpointPolicySettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// "<tenant_id>" = the tenant-default row; "<tenant_id>/<device_group_id>" =
	// an override. A bare "<device_group_id>" is NOT accepted: it is
	// indistinguishable from a bare tenant id, and guessing wrong would import
	// the tenant default over a group override (or vice versa) and then
	// overwrite the wrong row on the next apply.
	parts := strings.SplitN(req.ID, "/", 2)
	tenantID, groupID := parts[0], ""
	if len(parts) == 2 {
		groupID = parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>` for the tenant-default row or `<tenant_id>/<device_group_id>` for an override.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	if groupID == "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("device_group_id"), types.StringNull())...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID)...)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("device_group_id"), groupID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+groupID)...)
}

// upsert routes to the tenant-default or the per-group endpoint. One helper so
// Create and Update cannot drift on which URL they hit — writing a group's
// posture over the tenant default would silently apply one group's strictness
// to the entire fleet.
func (r *EndpointPolicySettingsResource) upsert(ctx context.Context, tenantID string,
	groupID types.String, version string,
	body adminapi.EndpointPolicySettingsWrite) (*adminapi.EndpointPolicySettings, error) {
	if isUnsetString(groupID) {
		return r.sdk.EndpointPolicies().UpsertTenantSettings(ctx, tenantID, version, body)
	}
	return r.sdk.EndpointPolicies().UpsertGroupSettings(ctx, tenantID, groupID.ValueString(), version, body)
}

func (r *EndpointPolicySettingsResource) fetch(ctx context.Context, tenantID string,
	groupID types.String) (*adminapi.EndpointPolicySettings, error) {
	if isUnsetString(groupID) {
		return r.sdk.EndpointPolicies().GetTenantSettings(ctx, tenantID)
	}
	return r.sdk.EndpointPolicies().GetGroupSettings(ctx, tenantID, groupID.ValueString())
}

func (r *EndpointPolicySettingsResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

func (m *EndpointPolicySettingsResourceModel) toWrite() adminapi.EndpointPolicySettingsWrite {
	body := adminapi.EndpointPolicySettingsWrite{}
	setStringPtr(m.UnapprovedMcpAction, &body.UnapprovedMcpAction)
	setStringPtr(m.DefaultDestinationAction, &body.DefaultDestinationAction)
	setBoolPtr(m.EchStripEnabled, &body.EchStripEnabled)
	setBoolPtr(m.DohBlockEnabled, &body.DohBlockEnabled)
	setBoolPtr(m.QuicBlockEnabled, &body.QuicBlockEnabled)

	// mcp_gateway_url is optional-not-computed, so a config that drops it must
	// actively CLEAR the stored value. The platform's upsert preserves omitted
	// fields, so sending nothing would leave a stale gateway URL rewriting every
	// approved MCP config on the fleet after the operator thought they removed
	// it. An explicit empty string is how the API clears it.
	if isUnsetString(m.McpGatewayURL) {
		empty := ""
		body.McpGatewayUrl = &empty
	} else {
		setStringPtr(m.McpGatewayURL, &body.McpGatewayUrl)
	}
	return body
}

func endpointSettingsToModel(tenantID string, row *adminapi.EndpointPolicySettings) EndpointPolicySettingsResourceModel {
	m := EndpointPolicySettingsResourceModel{TenantID: types.StringValue(tenantID)}
	if row.DeviceGroupId != nil {
		m.DeviceGroupID = types.StringValue(row.DeviceGroupId.String())
		m.ID = types.StringValue(tenantID + "/" + row.DeviceGroupId.String())
	} else {
		m.DeviceGroupID = types.StringNull()
		m.ID = types.StringValue(tenantID)
	}
	if row.Id != nil {
		m.SettingsID = types.StringValue(row.Id.String())
	} else {
		m.SettingsID = types.StringNull()
	}
	m.UnapprovedMcpAction = strPtrToTF(row.UnapprovedMcpAction)
	m.McpGatewayURL = strPtrToTF(row.McpGatewayUrl)
	m.DefaultDestinationAction = strPtrToTF(row.DefaultDestinationAction)
	m.EchStripEnabled = boolPtrOrDefault(row.EchStripEnabled)
	m.DohBlockEnabled = boolPtrOrDefault(row.DohBlockEnabled)
	m.QuicBlockEnabled = boolPtrOrDefault(row.QuicBlockEnabled)
	m.CreatedAt = timePtrToTF(row.CreatedAt)
	m.UpdatedAt = timePtrToTF(row.UpdatedAt)

	m.Version = int64PtrToTF(row.Version)
	m.ManagedBy = enumPtrToTF(row.ManagedBy)
	m.ManagedByClientID = strPtrToTF(row.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(row.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(row.LastModifiedBy)
	return m
}
