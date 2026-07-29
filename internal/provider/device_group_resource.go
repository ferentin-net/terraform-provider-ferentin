package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// DeviceGroupResource is the `ferentin_device_group` Terraform resource —
// the policy-scoping unit endpoint policy targets (platform#2018).
//
// This resource exists primarily so `device_group_ids` on a
// ferentin_endpoint_destination_rule, and the group key on a
// ferentin_endpoint_policy_settings override, can be a *reference*
// (`ferentin_device_group.contractors.group_id`) rather than a hardcoded UUID
// string. Hardcoded group UUIDs are unreferenceable, unimportable, and go stale
// the moment a group is recreated.
//
// Optimistic concurrency arrived with platform migration 1217 (#2040 item 2):
// `version` is threaded as `If-Match` on Update/Delete, and the four
// `managed_by*` attributes surface provenance and drift. Before that this
// resource was last-write-wins, which mattered because device groups are the
// TARGETING dimension for endpoint policy — a group silently renamed under a
// concurrent console edit is how a rule ends up scoped to something its author
// did not intend.
type DeviceGroupResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type DeviceGroupResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Source      types.String `tfsdk:"source"`
	ExternalID  types.String `tfsdk:"external_id"`

	GroupID   types.String `tfsdk:"group_id"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`

	// IaC readiness (platform#2040 item 2).
	Version           types.Int64  `tfsdk:"version"` // for If-Match
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

func NewDeviceGroupResource() resource.Resource { return &DeviceGroupResource{} }

var (
	_ resource.Resource                = (*DeviceGroupResource)(nil)
	_ resource.ResourceWithConfigure   = (*DeviceGroupResource)(nil)
	_ resource.ResourceWithImportState = (*DeviceGroupResource)(nil)
)

func (r *DeviceGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_device_group"
}

func (r *DeviceGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *DeviceGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Device group — the policy-scoping unit for managed devices. Reference " +
			"`group_id` from `ferentin_endpoint_destination_rule.device_group_ids` or from a " +
			"`ferentin_endpoint_policy_settings` override instead of hardcoding a UUID.\n\n" +
			"Carries `version` + the `managed_by*` provenance block, so a concurrent console edit is " +
			"rejected with 412 rather than silently clobbered. That matters here specifically: device " +
			"groups are the *targeting* dimension for endpoint policy, so a group renamed or recreated " +
			"out from under Terraform is how a rule ends up scoped to something its author did not " +
			"intend.\n\n" +
			"### Required scopes\n\n" +
			"Either `devices:groups:rw` (narrow — group CRUD only, held by the seeded " +
			"`ferentin.iac.operator` role) or the broad `devices:rw`. Prefer the narrow one: " +
			"`devices:rw` also grants device status transitions, per-serial certificate " +
			"revocation, and forced re-enrollment, which a pipeline that only creates groups has " +
			"no business holding.\n\n" +
			"## Import\n\n" +
			"```\n" +
			"terraform import ferentin_device_group.example <tenant_id>/<group_id>\n" +
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
			"name": schema.StringAttribute{
				MarkdownDescription: "Group name, unique within the tenant.",
				Required:            true,
			},
			"description": schema.StringAttribute{
				Optional: true, Computed: true,
			},
			"source": schema.StringAttribute{
				MarkdownDescription: "Where the group came from (e.g. `manual`, `scim`, `mdm`). " +
					"**Immutable** — the platform's update DTO does not accept it, so changing " +
					"this forces replacement.",
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"external_id": schema.StringAttribute{
				MarkdownDescription: "Identifier for this group in the upstream system (SCIM group id, " +
					"Jamf smart-group id, …). **Immutable** for the same reason as `source`.",
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},

			"group_id": schema.StringAttribute{
				MarkdownDescription: "Platform UUID for this group. This is what endpoint-policy " +
					"resources reference.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{Computed: true},

			// ===== IaC-readiness attributes (platform#2040 item 2) =====
			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version. Threaded as `If-Match` on " +
					"Update/Delete so a concurrent console edit is rejected with 412 rather than " +
					"silently clobbered. Read-only.",
				Computed: true,
			},
			"managed_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the original creator (`iac` for this provider). " +
					"Immutable after create. A group synced from Jamf or SCIM reports whatever " +
					"stamped it — not `iac`.",
				Computed: true,
			},
			"managed_by_client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth2 `client_id` of the creator, from the authenticated " +
					"principal (never a header).",
				Computed: true,
			},
			"managed_by_module": schema.StringAttribute{
				MarkdownDescription: "Module label this provider sent via `X-Ferentin-Managed-By-Module`.",
				Computed:            true,
			},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent writer. **Divergence from " +
					"`managed_by` is the drift signal** — `iac` + `console` means somebody renamed a " +
					"Terraform-managed group in the admin console.",
				Computed: true,
			},
		},
	}
}

func (r *DeviceGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DeviceGroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	body := adminapi.DeviceGroupCreate{Name: plan.Name.ValueString()}
	setStringPtr(plan.Description, &body.Description)
	setStringPtr(plan.Source, &body.Source)
	setStringPtr(plan.ExternalID, &body.ExternalId)

	group, err := r.sdk.DeviceGroups().Create(ctx, tenantID, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create device group", err)
		return
	}
	state := deviceGroupToModel(tenantID, group)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DeviceGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DeviceGroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	group, err := r.sdk.DeviceGroups().Get(ctx, tenantID, state.GroupID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read device group", err)
		return
	}
	refreshed := deviceGroupToModel(tenantID, group)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *DeviceGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DeviceGroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	// Only name and description are mutable; source / external_id are
	// RequiresReplace in the schema so they can never reach this path changed.
	body := adminapi.DeviceGroupUpdate{}
	setStringPtr(plan.Name, &body.Name)
	setStringPtr(plan.Description, &body.Description)

	// Version comes from state, never a literal — see the ferentin_mcp_policy note
	// where a hardcoded 0 made every second update 412.
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	group, err := r.sdk.DeviceGroups().Update(ctx, tenantID, state.GroupID.ValueString(), version, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update device group", err)
		return
	}
	refreshed := deviceGroupToModel(tenantID, group)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *DeviceGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DeviceGroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	err := r.sdk.DeviceGroups().Delete(ctx, tenantID, state.GroupID.ValueString(), version)
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete device group", err)
	}
}

func (r *DeviceGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, groupID string
	switch len(parts) {
	case 1:
		tenantID, groupID = r.tenantID, parts[0]
	case 2:
		tenantID, groupID = parts[0], parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>/<group_id>` or configure `tenant_id` on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), groupID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+groupID)...)
}

func (r *DeviceGroupResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

func deviceGroupToModel(tenantID string, g *adminapi.DeviceGroup) DeviceGroupResourceModel {
	m := DeviceGroupResourceModel{TenantID: types.StringValue(tenantID)}
	if g.GroupId != nil {
		m.GroupID = types.StringValue(g.GroupId.String())
	} else {
		m.GroupID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.GroupID.ValueString())
	m.Name = strPtrToTF(g.Name)
	m.Description = strPtrToTF(g.Description)
	m.Source = strPtrToTF(g.Source)
	m.ExternalID = strPtrToTF(g.ExternalId)
	m.CreatedAt = timePtrToTF(g.CreatedAt)
	m.UpdatedAt = timePtrToTF(g.UpdatedAt)

	m.Version = int64PtrToTF(g.Version)
	m.ManagedBy = enumPtrToTF(g.ManagedBy)
	m.ManagedByClientID = strPtrToTF(g.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(g.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(g.LastModifiedBy)
	return m
}
