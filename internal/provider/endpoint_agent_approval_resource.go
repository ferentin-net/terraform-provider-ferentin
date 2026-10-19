package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// EndpointAgentApprovalResource is the `ferentin_endpoint_agent_approval`
// resource: the admission decision for one catalogued AI agent on managed
// devices (platform#2275), approve (`report_only`) or deny (`block`).
//
// The platform keys a decision by detection-catalog slug and writes it with an
// upsert PUT, so catalog_slug is the identity. Create refuses to overwrite a
// decision that already exists rather than adopting it silently: a console
// decision is imported, never clobbered.
type EndpointAgentApprovalResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type EndpointAgentApprovalResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	CatalogSlug    types.String `tfsdk:"catalog_slug"`
	Action         types.String `tfsdk:"action"`
	DeviceGroupIDs types.List   `tfsdk:"device_group_ids"`
	Description    types.String `tfsdk:"description"`

	ApprovalID                   types.String `tfsdk:"approval_id"`
	ScopeUnreadable              types.Bool   `tfsdk:"scope_unreadable"`
	DeferredPendingUserPrincipal types.Bool   `tfsdk:"deferred_pending_user_principal"`
	CreatedAt                    types.String `tfsdk:"created_at"`
	UpdatedAt                    types.String `tfsdk:"updated_at"`

	Version           types.Int64  `tfsdk:"version"`
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

func NewEndpointAgentApprovalResource() resource.Resource { return &EndpointAgentApprovalResource{} }

var (
	_ resource.Resource                   = (*EndpointAgentApprovalResource)(nil)
	_ resource.ResourceWithConfigure      = (*EndpointAgentApprovalResource)(nil)
	_ resource.ResourceWithImportState    = (*EndpointAgentApprovalResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*EndpointAgentApprovalResource)(nil)
	_ resource.ResourceWithValidateConfig = (*EndpointAgentApprovalResource)(nil)
)

func (r *EndpointAgentApprovalResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_endpoint_agent_approval"
}

func (r *EndpointAgentApprovalResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *EndpointAgentApprovalResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Endpoint AI-agent decision — whether a catalogued AI agent (`claude-code`, " +
			"`codex-cli`, `openclaw`, …) may run on managed devices: `report_only` approves it (it runs and " +
			"its presence is reported), `block` denies it.\n\n" +
			"An agent with no decision falls through to `unapproved_agent_action` on the device's " +
			"`ferentin_endpoint_policy_settings` posture (`report_only` unless set).\n\n" +
			"~> **Destroying a decision is not neutral.** The agent falls back to " +
			"`unapproved_agent_action`, so under a `block` default, destroying a `report_only` decision " +
			"**denies** the agent. `plan` warns when it would.\n\n" +
			"A decision with no `device_group_ids` reaches every device and, like other endpoint policy " +
			"changes, is staged until an operator activates it in the admin console. A decision scoped to " +
			"device groups reaches those groups on their next policy poll.\n\n" +
			"The decision is keyed by `catalog_slug`. Creating one for a slug that already has a decision " +
			"(for example one made in the console) fails and says to import it instead, rather than " +
			"overwriting it.\n\n" +
			"## Import\n\n" +
			"```\n" +
			"terraform import ferentin_endpoint_agent_approval.example <tenant_id>/<catalog_slug>\n" +
			"```",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"tenant_id": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"catalog_slug": schema.StringAttribute{
				MarkdownDescription: "The agent's slug in the platform's AI-agent detection catalog, e.g. " +
					"`claude-code`, `codex-cli`, `openclaw`. The platform refuses a slug that is not a " +
					"detection-enabled catalog entry. Changing it replaces the decision.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"action": schema.StringAttribute{
				MarkdownDescription: "`report_only` approves the agent: it runs and its presence is reported. " +
					"`block` denies it.",
				Required:   true,
				Validators: []validator.String{stringvalidator.OneOf("report_only", "block")},
			},
			"device_group_ids": schema.ListAttribute{
				MarkdownDescription: "Device groups the decision applies to — reference " +
					"`ferentin_device_group.<name>.group_id`. Empty or unset = **every device in the " +
					"tenant**. Devices outside the groups fall through to `unapproved_agent_action`, so " +
					"\"block for one pilot group, allow everywhere else\" needs one decision, not two. " +
					"At most 64.",
				Optional:    true,
				ElementType: types.StringType,
				Validators:  []validator.List{listvalidator.SizeAtMost(64)},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Admin note. Never reaches the device. At most 500 characters.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthAtMost(500)},
			},

			"approval_id": schema.StringAttribute{
				MarkdownDescription: "Platform row UUID. Informational: the decision is addressed by " +
					"`catalog_slug`.",
				Computed: true, PlanModifiers: keep,
			},
			"scope_unreadable": schema.BoolAttribute{
				MarkdownDescription: "True when the platform cannot read the stored device-group scope. " +
					"Such a decision is in force on **no** device, even though it reads as unscoped. The " +
					"provider plans an update that writes the configured scope back.",
				Computed: true,
			},
			"deferred_pending_user_principal": schema.BoolAttribute{
				MarkdownDescription: "True when the decision carries user criteria that devices cannot " +
					"evaluate yet, which makes it inert on every device. Only a decision written outside " +
					"this provider can have them.",
				Computed: true,
			},
			"created_at": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"updated_at": schema.StringAttribute{Computed: true},
			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version, sent as `If-Match` on update and " +
					"delete so a concurrent console edit is rejected with 412 rather than overwritten.",
				Computed: true,
			},
			"managed_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the original creator (`iac` for this provider). " +
					"Immutable after create.",
				Computed: true, PlanModifiers: keep,
			},
			"managed_by_client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth2 `client_id` of the creator.",
				Computed:            true, PlanModifiers: keep,
			},
			"managed_by_module": schema.StringAttribute{
				MarkdownDescription: "Module label the creator sent via `X-Ferentin-Managed-By-Module`.",
				Computed:            true, PlanModifiers: keep,
			},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent writer. Divergence from `managed_by` " +
					"is the drift signal.",
				Computed: true,
			},
		},
	}
}

func (r *EndpointAgentApprovalResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// ValidateConfig refuses a device_group_ids entry that is not a UUID at plan
// time, when it is known then.
func (r *EndpointAgentApprovalResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var groups types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("device_group_ids"), &groups)...)
	if resp.Diagnostics.HasError() || groups.IsNull() || groups.IsUnknown() {
		return
	}
	for _, v := range groups.Elements() {
		if s, ok := v.(types.String); ok && !s.IsUnknown() && !s.IsNull() {
			if _, err := uuid.Parse(s.ValueString()); err == nil {
				continue
			}
			resp.Diagnostics.AddAttributeError(path.Root("device_group_ids"),
				fmt.Sprintf("%q is not a device group ID", s.ValueString()),
				"Reference `ferentin_device_group.<name>.group_id` rather than writing a group's name.")
		}
	}
}

// ModifyPlan says out loud the three changes whose effect the plan alone does
// not show: destroying an approval, widening one to every device, and a stored
// scope the platform cannot read, which it forces an update to repair.
func (r *EndpointAgentApprovalResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		return
	}
	var state EndpointAgentApprovalResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	slug := state.CatalogSlug.ValueString()

	if req.Plan.Raw.IsNull() {
		if state.Action.ValueString() == "report_only" {
			resp.Diagnostics.AddWarning(
				fmt.Sprintf("Destroying the approval of %q", slug),
				"Without a decision the agent falls back to `unapproved_agent_action` on the posture of "+
					"each device's scope. Where that is `block`, this destroy DENIES the agent. To deny it "+
					"on purpose, set `action = \"block\"` instead, which says so in the configuration.")
		}
		return
	}

	var plan EndpointAgentApprovalResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if n := len(state.DeviceGroupIDs.Elements()); n > 0 && !plan.DeviceGroupIDs.IsUnknown() &&
		len(plan.DeviceGroupIDs.Elements()) == 0 {
		resp.Diagnostics.AddAttributeWarning(path.Root("device_group_ids"),
			"This apply removes the decision's `device_group_ids`",
			fmt.Sprintf("The decision for %q targets %d device group(s) and the plan has none, so it will "+
				"apply to every device in the tenant. An empty list means \"no narrowing\", not "+
				"\"nothing\".", slug, n))
	}
	if state.ScopeUnreadable.ValueBool() {
		resp.Diagnostics.AddWarning(
			fmt.Sprintf("The decision for %q is in force on no device", slug),
			"The platform cannot read its stored device-group scope, so devices drop it. This plan "+
				"writes the configured scope back to repair it.")
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("version"), types.Int64Unknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("scope_unreadable"), types.BoolUnknown())...)
	}
}

func (r *EndpointAgentApprovalResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan EndpointAgentApprovalResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)
	slug := plan.CatalogSlug.ValueString()

	// The write is an upsert, so without this check Create would silently
	// replace a decision someone made in the console.
	existing, err := r.sdk.EndpointAgentApprovals().Get(ctx, tenantID, slug)
	switch {
	case err == nil:
		resp.Diagnostics.AddAttributeError(path.Root("catalog_slug"),
			fmt.Sprintf("%q already has a decision", slug),
			fmt.Sprintf("The tenant already decides %q (action %q, created by %q). Import it instead of "+
				"creating it:\n\n  terraform import <address> %s/%s",
				slug, derefStr(existing.Action), enumPtrToTF(existing.ManagedBy).ValueString(),
				tenantID, slug))
		return
	case !errors.Is(err, adminapi.ErrNotFound):
		addSDKError(&resp.Diagnostics, "Failed to read endpoint agent approvals", err)
		return
	}

	body := agentApprovalWrite(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.sdk.EndpointAgentApprovals().Upsert(ctx, tenantID, slug, "", body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create endpoint agent approval", err)
		return
	}
	state := agentApprovalToModel(tenantID, got)
	state.keepConfigured(plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *EndpointAgentApprovalResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state EndpointAgentApprovalResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	got, err := r.sdk.EndpointAgentApprovals().Get(ctx, tenantID, state.CatalogSlug.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read endpoint agent approval", err)
		return
	}
	refreshed := agentApprovalToModel(tenantID, got)
	refreshed.keepConfigured(state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *EndpointAgentApprovalResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state EndpointAgentApprovalResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	body := agentApprovalWrite(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	got, err := r.sdk.EndpointAgentApprovals().Upsert(ctx, tenantID, state.CatalogSlug.ValueString(), version, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update endpoint agent approval", err)
		return
	}
	refreshed := agentApprovalToModel(tenantID, got)
	refreshed.keepConfigured(plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *EndpointAgentApprovalResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state EndpointAgentApprovalResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	err := r.sdk.EndpointAgentApprovals().Delete(ctx, tenantID, state.CatalogSlug.ValueString(), version)
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete endpoint agent approval", err)
	}
}

func (r *EndpointAgentApprovalResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	tenantID, slug := r.tenantID, req.ID
	if t, s, ok := strings.Cut(req.ID, "/"); ok {
		tenantID, slug = t, s
	}
	if tenantID == "" || slug == "" {
		resp.Diagnostics.AddError("Cannot determine the decision to import",
			"Pass `<tenant_id>/<catalog_slug>`, or `<catalog_slug>` with `tenant_id` configured on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("catalog_slug"), slug)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+slug)...)
}

// agentApprovalWrite builds the PUT body. It is a full replacement: an unset
// description or device_group_ids is sent as absent, which clears it.
func agentApprovalWrite(ctx context.Context, m EndpointAgentApprovalResourceModel, diags *diag.Diagnostics) adminapi.EndpointAgentApprovalWrite {
	body := adminapi.EndpointAgentApprovalWrite{Action: m.Action.ValueString()}
	setStringPtr(m.Description, &body.Description)
	if invalid := setUUIDListPtr(ctx, m.DeviceGroupIDs, &body.DeviceGroupIds); len(invalid) > 0 {
		diags.AddAttributeError(path.Root("device_group_ids"), "Invalid device group ID",
			fmt.Sprintf("Not UUIDs: %s. Reference `ferentin_device_group.<name>.group_id`.", strings.Join(invalid, ", ")))
	}
	return body
}

func agentApprovalToModel(tenantID string, a *adminapi.EndpointAgentApproval) EndpointAgentApprovalResourceModel {
	m := EndpointAgentApprovalResourceModel{TenantID: types.StringValue(tenantID)}
	m.CatalogSlug = strPtrToTF(a.CatalogSlug)
	m.ID = types.StringValue(tenantID + "/" + m.CatalogSlug.ValueString())
	if a.Id != nil {
		m.ApprovalID = types.StringValue(a.Id.String())
	} else {
		m.ApprovalID = types.StringNull()
	}
	m.Action = strPtrToTF(a.Action)
	m.Description = emptyStringAsNull(a.Description)
	m.DeviceGroupIDs = emptyListAsNull(uuidSliceToList(a.DeviceGroupIds))
	m.ScopeUnreadable = types.BoolValue(a.ScopeUnreadable != nil && *a.ScopeUnreadable)
	m.DeferredPendingUserPrincipal = types.BoolValue(a.DeferredPendingUserPrincipal != nil && *a.DeferredPendingUserPrincipal)
	m.CreatedAt = timePtrToTF(a.CreatedAt)
	m.UpdatedAt = timePtrToTF(a.UpdatedAt)
	m.Version = int64PtrToTF(a.Version)
	m.ManagedBy = enumPtrToTF(a.ManagedBy)
	m.ManagedByClientID = strPtrToTF(a.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(a.ManagedByModule)
	m.LastModifiedBy = strPtrToTF(a.LastModifiedBy)
	return m
}

// keepConfigured restores the empty forms a configuration wrote explicitly
// (device_group_ids = [], description = ""), which the platform returns as
// absent, so they do not plan a perpetual change.
func (m *EndpointAgentApprovalResourceModel) keepConfigured(prior EndpointAgentApprovalResourceModel) {
	if m.DeviceGroupIDs.IsNull() && !prior.DeviceGroupIDs.IsNull() && !prior.DeviceGroupIDs.IsUnknown() &&
		len(prior.DeviceGroupIDs.Elements()) == 0 {
		m.DeviceGroupIDs = prior.DeviceGroupIDs
	}
	if m.Description.IsNull() && !prior.Description.IsNull() && prior.Description.ValueString() == "" {
		m.Description = prior.Description
	}
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
