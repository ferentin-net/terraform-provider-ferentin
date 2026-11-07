package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// MCPScopeDefaultsResource is the `ferentin_mcp_scope_defaults` Terraform
// resource: what a local MCP server or tool meets when no MCP policy applies to
// it, per scope (the tenant default, or one device group). ADR 0029 decision 6,
// ferentin-platform#3256 item 5.
//
// The API (/mcp-policies/defaults) is a view over the endpoint posture row, not
// a store of its own (McpScopeDefaultsService), which shapes this resource:
//
//   - It is an upsert addressed by (tenant_id, device_group_id), like
//     ferentin_endpoint_policy_settings, and shares that resource's row and
//     version. See retryStalePostureVersion for how the two avoid 412ing each
//     other.
//   - Every PUT keeps what it omits, so each default is Optional + Computed +
//     UseStateForUnknown and sent only when configured: a default left out of
//     config stays whatever the console set.
//   - There is no DELETE. Destroy leaves the defaults in force and drops the
//     resource from state, as the tenant posture row does.
//   - Each default is a (value, mode) pair; mode report_only rehearses a value
//     that refuses. An omitted mode is the platform's: report_only for the value
//     report_only, enforce otherwise. This resource always sends the pair and
//     plans that same mode, so the plan is what the platform stores.
//
// Flat attributes, not nested blocks: an Optional + Computed nested attribute
// left out of config does not propose its prior value, which made every plan a
// no-op update on ferentin_endpoint_policy_settings (see local_mcp_risk_policy's
// history there).
type MCPScopeDefaultsResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type MCPScopeDefaultsResourceModel struct {
	ID            types.String `tfsdk:"id"`
	TenantID      types.String `tfsdk:"tenant_id"`
	DeviceGroupID types.String `tfsdk:"device_group_id"`

	ServerApproval     types.String `tfsdk:"server_approval"`
	ServerApprovalMode types.String `tfsdk:"server_approval_mode"`
	RemoteEntry        types.String `tfsdk:"remote_entry"`
	RemoteEntryMode    types.String `tfsdk:"remote_entry_mode"`
	Unattributed       types.String `tfsdk:"unattributed"`
	UnattributedMode   types.String `tfsdk:"unattributed_mode"`

	ToolVettingMode           types.String         `tfsdk:"tool_vetting_mode"`
	ToolVettingRiskPolicy     types.Object         `tfsdk:"tool_vetting_risk_policy"`
	ToolVettingApprovalConfig jsontypes.Normalized `tfsdk:"tool_vetting_approval_config"`

	Source                          types.String `tfsdk:"source"`
	ToolVettingModeSource           types.String `tfsdk:"tool_vetting_mode_source"`
	ToolVettingRiskPolicySource     types.String `tfsdk:"tool_vetting_risk_policy_source"`
	ToolVettingApprovalConfigSource types.String `tfsdk:"tool_vetting_approval_config_source"`
	Version                         types.Int64  `tfsdk:"version"`
}

const (
	scopeDefaultReportOnly = "report_only"
	scopeModeEnforce       = "enforce"
	scopeVettingInherit    = "inherit"
	scopeSourceGroup       = "device_group"
	scopeSourceTenant      = "tenant"

	// scopeApprovalFormKey holds, in private state, the platform's approval
	// config as normalized JSON from the last write or refresh, so a
	// formatting difference from the configured JSON is not drift.
	scopeApprovalFormKey = "approval_config_form"
)

func NewMCPScopeDefaultsResource() resource.Resource { return &MCPScopeDefaultsResource{} }

var (
	_ resource.Resource                   = (*MCPScopeDefaultsResource)(nil)
	_ resource.ResourceWithConfigure      = (*MCPScopeDefaultsResource)(nil)
	_ resource.ResourceWithImportState    = (*MCPScopeDefaultsResource)(nil)
	_ resource.ResourceWithValidateConfig = (*MCPScopeDefaultsResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*MCPScopeDefaultsResource)(nil)
)

// ModifyPlan plans what a write recomputes as unknown when the plan changes
// anything. keepOrDerivedMode can introduce the only change (a mode derived
// from its value), and the framework marks computed attributes unknown only
// for a change in config; see markComputedUnknownOnChange.
func (r *MCPScopeDefaultsResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	markComputedUnknownOnChange(ctx, req, resp, map[string]attr.Value{
		"version":                             types.Int64Unknown(),
		"remote_entry":                        types.StringUnknown(),
		"remote_entry_mode":                   types.StringUnknown(),
		"source":                              types.StringUnknown(),
		"tool_vetting_mode_source":            types.StringUnknown(),
		"tool_vetting_risk_policy_source":     types.StringUnknown(),
		"tool_vetting_approval_config_source": types.StringUnknown(),
	})
}

func (r *MCPScopeDefaultsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_scope_defaults"
}

func (r *MCPScopeDefaultsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// keepOrDerivedMode is the plan for a default's mode. Configured: as written.
// Its value not configured: the pair is not managed, so the state value is kept
// (UseStateForUnknown). Its value configured and the mode not: the platform's
// rule for an omitted mode, which this resource sends explicitly.
type keepOrDerivedMode struct{ valueAttr string }

func (m keepOrDerivedMode) Description(context.Context) string {
	return "report_only for the value report_only, enforce otherwise, when the mode is not set"
}

func (m keepOrDerivedMode) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (m keepOrDerivedMode) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.Plan.Raw.IsNull() || !req.ConfigValue.IsNull() {
		return
	}
	var value types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(m.valueAttr), &value)...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch {
	case value.IsUnknown():
		resp.PlanValue = types.StringUnknown()
	case value.IsNull():
		if !req.State.Raw.IsNull() && !req.StateValue.IsNull() {
			resp.PlanValue = req.StateValue
		}
	default:
		resp.PlanValue = types.StringValue(derivedMode(value.ValueString()))
	}
}

// derivedMode is the platform's mode for a default written without one
// (McpScopeDefaultsService.normalize).
func derivedMode(value string) string {
	if value == scopeDefaultReportOnly {
		return scopeDefaultReportOnly
	}
	return scopeModeEnforce
}

func (r *MCPScopeDefaultsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	modeValidators := func(valueAttr string) []validator.String {
		return []validator.String{
			stringvalidator.OneOf(scopeModeEnforce, scopeDefaultReportOnly),
			stringvalidator.AlsoRequires(path.MatchRoot(valueAttr)),
		}
	}
	modeDescription := func(valueAttr string) string {
		return fmt.Sprintf("`enforce`, or `report_only` to rehearse `%s`: devices act as if it were "+
			"`report_only` and the platform records what the value would have refused. Unset, it is "+
			"`report_only` when `%s` is `report_only` and `enforce` otherwise, so setting a refusing value "+
			"alone enforces it. `enforce` is refused with `report_only` (it refuses nothing). Only set "+
			"together with `%s`.", valueAttr, valueAttr, valueAttr)
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "MCP scope defaults: what a local MCP server or tool on a managed device " +
			"meets when **no MCP policy applies to it**, for the tenant default or for one device group. " +
			"Four defaults, each enforced or rehearsed (`report_only`):\n\n" +
			"* **server approval** — a local MCP server config that is not approved\n" +
			"* **remote entry** — a remote MCP server entry that is not approved; it equals server " +
			"approval until devices carry the two apart, so it is read-only here\n" +
			"* **unattributed** — a local MCP server started by a process the device cannot place in " +
			"an identified AI agent's session (for example from Terminal.app)\n" +
			"* **tool vetting** — an unvetted tool on an approved local server: its mode, the action " +
			"per risk rating, and the approval routing an `ask_*` tier needs\n\n" +
			"Omit `device_group_id` for the **tenant default**, which every device without a group " +
			"default of its own runs. Set it for one device group.\n\n" +
			"Each default is sent only when it is in the config: one left out keeps what the tenant " +
			"has, so a default can stay with the admin console.\n\n" +
			"~> **This resource writes the endpoint posture row.** The platform stores these defaults on " +
			"the same row `ferentin_endpoint_policy_settings` manages for the scope, so the two share " +
			"one version. When an apply changes both, the provider retries a write the other made " +
			"stale, as long as the defaults this resource sends were not changed elsewhere. On a device " +
			"group with no posture of its own, the first write here creates the group's row as a copy of " +
			"the tenant default's posture. Destroying a group's `ferentin_endpoint_policy_settings` " +
			"deletes that row, and with it the group's own scope defaults: the group then runs the " +
			"tenant's, and the next plan here shows the difference. When both resources manage one " +
			"group, make this one `depends_on` the settings resource so the group row is created once.\n\n" +
			"~> **Destroy does not reset anything.** The platform has no way to delete the defaults, and " +
			"resetting them to the permissive built-ins would stand enforcement down silently, so " +
			"`terraform destroy` (or removing the block) drops the resource from state and leaves the " +
			"defaults in force, with a warning. To stand them down, apply `report_only` values first.\n\n" +
			"## Import\n\n" +
			"```\n" +
			"terraform import ferentin_mcp_scope_defaults.tenant <tenant_id>\n" +
			"terraform import ferentin_mcp_scope_defaults.contractors <tenant_id>/<device_group_id>\n" +
			"```\n\n" +
			"The `import_id` of each row of `ferentin_endpoint_policy_settings_scopes` works here too.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`<tenant_id>` for the tenant default, `<tenant_id>/<device_group_id>` for a group.",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"tenant_id": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"device_group_id": schema.StringAttribute{
				MarkdownDescription: "Device group these defaults apply to — reference " +
					"`ferentin_device_group.<name>.group_id`. Omit for the tenant default. Part of the " +
					"resource's identity: changing it forces replacement.",
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{policyInstanceRefValidator{
					attrHint: "`ferentin_device_group.<name>.group_id`",
				}},
			},

			"server_approval": schema.StringAttribute{
				MarkdownDescription: "What a device does with a local MCP server config that no policy " +
					"applies to and that is not approved:\n\n" +
					"* `report_only` — report it, change nothing on the machine\n" +
					"* `quarantine` — move the offending config aside so the client cannot load it (the " +
					"entry is preserved in a root-owned sidecar first)\n" +
					"* `block` — block the server's traffic outright (needs the NetworkExtension content " +
					"filter)\n\n" +
					"Omit to keep the current value.",
				Optional: true, Computed: true,
				PlanModifiers: keep,
				Validators:    []validator.String{stringvalidator.OneOf(scopeDefaultReportOnly, "quarantine", "block")},
			},
			"server_approval_mode": schema.StringAttribute{
				MarkdownDescription: modeDescription("server_approval"),
				Optional:            true, Computed: true,
				PlanModifiers: []planmodifier.String{keepOrDerivedMode{valueAttr: "server_approval"}},
				Validators:    modeValidators("server_approval"),
			},
			"remote_entry": schema.StringAttribute{
				MarkdownDescription: "Read-only. The remote entry default, which equals `server_approval` " +
					"until devices carry them apart.",
				Computed: true,
			},
			"remote_entry_mode": schema.StringAttribute{
				MarkdownDescription: "Read-only. The remote entry default's mode.",
				Computed:            true,
			},
			"unattributed": schema.StringAttribute{
				MarkdownDescription: "What a device does with a local MCP server started by a process it " +
					"cannot place in an identified AI agent's session: `report_only` (start it and record " +
					"it as `agent_unattributed`) or `block` (refuse it). Agents the device cannot verify, " +
					"such as Gemini CLI, read as unattributed when started outside another agent, so review " +
					"the recorded `agent_unattributed` admissions (or rehearse with `unattributed_mode = " +
					"\"report_only\"`) before blocking. Only devices on an agent build that reads it " +
					"enforce `block`. Omit to keep the current value.",
				Optional: true, Computed: true,
				PlanModifiers: keep,
				Validators:    []validator.String{stringvalidator.OneOf(scopeDefaultReportOnly, "block")},
			},
			"unattributed_mode": schema.StringAttribute{
				MarkdownDescription: modeDescription("unattributed"),
				Optional:            true, Computed: true,
				PlanModifiers: []planmodifier.String{keepOrDerivedMode{valueAttr: "unattributed"}},
				Validators:    modeValidators("unattributed"),
			},

			"tool_vetting_mode": schema.StringAttribute{
				MarkdownDescription: "What an unvetted tool on an approved local MCP server meets: " +
					"`enforce` (the risk policy decides), or `report_only` to let it run and record what " +
					"the risk policy would have answered. `report_only` round-trips every call to the " +
					"platform and lifts the approved-server gate on allow tiers, so it is a rehearsal, not a " +
					"setting to leave on; an admin's vetting denial, the schema-flap breaker, the tool cap " +
					"and data protection still apply. On a device group, `inherit` follows the tenant " +
					"default's, and is what a group that sets none reads as. Omit to keep the current value.",
				Optional: true, Computed: true,
				PlanModifiers: keep,
				Validators: []validator.String{
					stringvalidator.OneOf(scopeModeEnforce, scopeDefaultReportOnly, scopeVettingInherit),
				},
			},
			// An ObjectAttribute for the reason given on the type: a nested
			// attribute left out of config would plan an update every time.
			"tool_vetting_risk_policy": schema.ObjectAttribute{
				MarkdownDescription: "What an unvetted local MCP tool gets at each risk rating: an object " +
					"with `low`, `medium` and `high`, each one of `allow`, `ask_once`, `ask_every_call` or " +
					"`deny`. All three are required. Unset on the platform is the legacy behaviour (step " +
					"up when approval routing exists, deny otherwise). On a device group it is the group's " +
					"own policy; null means the group follows the tenant default's.\n\n" +
					"~> Any `ask_*` tier needs approval routing with at least one channel " +
					"(`tool_vetting_approval_config`, or on a device group the tenant default's), or the " +
					"platform refuses the write.\n\n" +
					"Omit to keep the current policy. Clearing it (on a group, returning to the tenant's) " +
					"is done in the admin console.",
				Optional:       true,
				Computed:       true,
				AttributeTypes: localMcpRiskPolicyAttrTypes,
				PlanModifiers:  []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
				Validators:     []validator.Object{localMcpRiskPolicyTiers{}},
			},
			"tool_vetting_approval_config": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "Approval routing for step-up approvals raised by unvetted local MCP " +
					"tools, as JSON — write it with `jsonencode({...})`. The object is the platform's " +
					"approval config: `step_up_mode` (`reauth` / `approve`), `channels` (at least one; " +
					"`type` is `invocation_context`, `slack`, `email`, `webhook` or `approver_dm`), and " +
					"optionally `approver_role`, `timeout_seconds`, `escalation`, `context_fields`, " +
					"`redacted_fields` and the risk bounds. On a device group it is the group's own; null " +
					"means the group follows the tenant default's.\n\n" +
					"A key this provider version does not know fails at plan rather than being dropped. A " +
					"formatting difference from the platform's stored form is not drift. Omit to keep the " +
					"current config. Clearing it is done in the admin console: `{}` is refused here.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{approvalConfigJSON{}},
			},

			"source": schema.StringAttribute{
				MarkdownDescription: "Read-only. Where `server_approval`, `remote_entry` and `unattributed` " +
					"come from: `device_group` (this group's own posture), `tenant` (the tenant default's, " +
					"for a group with no posture of its own) or `built_in` (no posture at all).",
				Computed: true,
			},
			"tool_vetting_mode_source": schema.StringAttribute{
				MarkdownDescription: "Read-only. Where the tool vetting mode in force comes from: `device_group`, `tenant` or `built_in`.",
				Computed:            true,
			},
			"tool_vetting_risk_policy_source": schema.StringAttribute{
				MarkdownDescription: "Read-only. Where the risk policy in force comes from: `device_group`, `tenant` or `built_in`.",
				Computed:            true,
			},
			"tool_vetting_approval_config_source": schema.StringAttribute{
				MarkdownDescription: "Read-only. Where the approval routing in force comes from: `device_group`, `tenant` or `built_in`.",
				Computed:            true,
			},
			"version": schema.Int64Attribute{
				MarkdownDescription: "The scope's posture row version, sent as `If-Match`. Shared with " +
					"`ferentin_endpoint_policy_settings` for the same scope. Null while a device group has " +
					"no posture of its own; the first write then sends no `If-Match`.",
				Computed: true,
			},
		},
	}
}

// ValidateConfig mirrors the platform's cheap refusals
// (McpScopeDefaultsService.normalize and toPosture) at plan time.
func (r *MCPScopeDefaultsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg MCPScopeDefaultsResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, pair := range []struct {
		value, mode types.String
		modeAttr    string
	}{
		{cfg.ServerApproval, cfg.ServerApprovalMode, "server_approval_mode"},
		{cfg.Unattributed, cfg.UnattributedMode, "unattributed_mode"},
	} {
		if stringOrEmpty(pair.value) == scopeDefaultReportOnly && stringOrEmpty(pair.mode) == scopeModeEnforce {
			resp.Diagnostics.AddAttributeError(path.Root(pair.modeAttr),
				"report_only cannot be enforced",
				"report_only refuses nothing, so the platform refuses mode enforce with it "+
					"(report_only_value_enforced). Set the mode to report_only, or leave it unset, or "+
					"choose a value that refuses.")
		}
	}
	if stringOrEmpty(cfg.ToolVettingMode) == scopeVettingInherit && cfg.DeviceGroupID.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("tool_vetting_mode"),
			"inherit is for a device group only",
			"inherit follows the tenant default's tool vetting mode, so the tenant default itself cannot "+
				"inherit (inherit_needs_device_group). Use enforce or report_only, or set device_group_id.")
	}
}

// stringOrEmpty is the value of a known, non-null string, else "".
func stringOrEmpty(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func (r *MCPScopeDefaultsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config MCPScopeDefaultsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)
	body, d := scopeDefaultsToWrite(plan, config)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	// No If-Match: the scope's row may not exist, and against a missing row the
	// platform answers an If-Match with 412. An existing row is adopted, as
	// ferentin_endpoint_policy_settings does.
	got, err := r.put(ctx, tenantID, plan.DeviceGroupID, "", body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to write MCP scope defaults", err)
		return
	}
	r.storeResult(ctx, tenantID, plan, body, got, nil, resp.Private, &resp.State, &resp.Diagnostics)
}

func (r *MCPScopeDefaultsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MCPScopeDefaultsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	got, err := r.get(ctx, tenantID, state.DeviceGroupID)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			// Only a device group answers 404: it was deleted.
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read MCP scope defaults", err)
		return
	}
	recorded, d := loadApprovalForm(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	refreshed := scopeDefaultsToModel(tenantID, state.DeviceGroupID, got)
	form := reconcileScopeApprovalConfig(&refreshed, state, false, recorded)
	resp.Diagnostics.Append(storeApprovalForm(ctx, resp.Private, form)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPScopeDefaultsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, config MCPScopeDefaultsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	body, d := scopeDefaultsToWrite(plan, config)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	recorded, d := loadApprovalForm(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	got, err := r.update(ctx, tenantID, state, body, recorded)
	if err != nil {
		if addPostureConflictError(&resp.Diagnostics, "Failed to update MCP scope defaults", err) {
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to update MCP scope defaults", err)
		return
	}
	r.storeResult(ctx, tenantID, plan, body, got, recorded, resp.Private, &resp.State, &resp.Diagnostics)
}

// update writes body with state's version, retrying once when the version is
// stale only because of a write this resource does not own (see
// retryStalePostureVersion).
func (r *MCPScopeDefaultsResource) update(ctx context.Context, tenantID string, state MCPScopeDefaultsResourceModel,
	body adminapi.MCPScopeDefaultsWrite, recorded *string) (*adminapi.MCPScopeDefaults, error) {
	return retryStalePostureVersion(versionString(state.Version),
		func(v string) (*adminapi.MCPScopeDefaults, error) {
			return r.put(ctx, tenantID, state.DeviceGroupID, v, body)
		},
		func() (string, []string, error) {
			fresh, err := r.get(ctx, tenantID, state.DeviceGroupID)
			if err != nil {
				return "", nil, err
			}
			m := scopeDefaultsToModel(tenantID, state.DeviceGroupID, fresh)
			reconcileScopeApprovalConfig(&m, state, false, recorded)
			return int64PtrString(fresh.Version), scopeDefaultsOwnedChanges(state, m, body), nil
		})
}

// Delete calls nothing: the platform has no delete for the defaults, and a
// reset to the built-ins would be fail-open (see the resource docs and
// EndpointPolicySettingsResource.Delete, which reasons the same way).
func (r *MCPScopeDefaultsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MCPScopeDefaultsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	scope := "the tenant default"
	if !isUnsetString(state.DeviceGroupID) {
		scope = "device group " + state.DeviceGroupID.ValueString()
	}
	resp.Diagnostics.AddWarning("MCP scope defaults removed from state, but STILL IN FORCE",
		fmt.Sprintf("The platform has no way to delete MCP scope defaults, so Terraform has stopped "+
			"managing those of %s and left them as they are (server_approval = %q (%s), unattributed = "+
			"%q (%s), tool_vetting_mode = %q).\n\n"+
			"Resetting them on destroy would silently stand enforcement down, and removing a resource "+
			"block or dropping a module reaches this path just as `terraform destroy` does. To stand "+
			"them down, set server_approval and unattributed to \"report_only\" and apply first, then "+
			"destroy.", scope,
			state.ServerApproval.ValueString(), state.ServerApprovalMode.ValueString(),
			state.Unattributed.ValueString(), state.UnattributedMode.ValueString(),
			state.ToolVettingMode.ValueString()))
}

func (r *MCPScopeDefaultsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// As ferentin_endpoint_policy_settings: a bare group id is not accepted,
	// because it cannot be told from a bare tenant id.
	parts := strings.Split(req.ID, "/")
	if len(parts) > 2 || parts[0] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<tenant_id>` for the tenant default or `<tenant_id>/<device_group_id>` "+
				"for a device group, got %q.", req.ID))
		return
	}
	tenantID := parts[0]
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	if len(parts) == 1 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("device_group_id"), types.StringNull())...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID)...)
		return
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("The device group id must be a UUID, got %q (from import ID %q).", parts[1], req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("device_group_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+parts[1])...)
}

func (r *MCPScopeDefaultsResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

func (r *MCPScopeDefaultsResource) get(ctx context.Context, tenantID string, groupID types.String) (*adminapi.MCPScopeDefaults, error) {
	if isUnsetString(groupID) {
		return r.sdk.MCPScopeDefaults().GetTenant(ctx, tenantID)
	}
	return r.sdk.MCPScopeDefaults().GetGroup(ctx, tenantID, groupID.ValueString())
}

// put routes to the tenant or the group endpoint; one helper so Create and
// Update cannot disagree on which scope they write.
func (r *MCPScopeDefaultsResource) put(ctx context.Context, tenantID string, groupID types.String,
	version string, body adminapi.MCPScopeDefaultsWrite) (*adminapi.MCPScopeDefaults, error) {
	if isUnsetString(groupID) {
		return r.sdk.MCPScopeDefaults().PutTenant(ctx, tenantID, version, body)
	}
	return r.sdk.MCPScopeDefaults().PutGroup(ctx, tenantID, groupID.ValueString(), version, body)
}

// storeResult sets state from a write's answer, keeping the configured
// approval config JSON where the platform stored the same thing.
func (r *MCPScopeDefaultsResource) storeResult(ctx context.Context, tenantID string, plan MCPScopeDefaultsResourceModel,
	body adminapi.MCPScopeDefaultsWrite, got *adminapi.MCPScopeDefaults, recorded *string,
	priv privateStateSetter, st interface {
		Set(context.Context, any) diag.Diagnostics
	}, diags *diag.Diagnostics) {
	m := scopeDefaultsToModel(tenantID, plan.DeviceGroupID, got)
	sent := body.ToolVetting != nil && body.ToolVetting.ApprovalConfig != nil
	if sent && m.ToolVettingApprovalConfig.IsNull() {
		diags.AddAttributeError(path.Root("tool_vetting_approval_config"),
			"The platform did not store tool_vetting_approval_config",
			"The request sent it and the platform's response has none for this scope. A platform build "+
				"that predates this field ignores it; check the admin-api version.")
	}
	form := reconcileScopeApprovalConfig(&m, plan, sent, recorded)
	diags.Append(storeApprovalForm(ctx, priv, form)...)
	diags.Append(st.Set(ctx, &m)...)
}

// scopeDefaultsToWrite builds the request from the plan, sending only what the
// config sets: the platform keeps every default a write omits. A configured
// value is sent with its planned mode (keepOrDerivedMode), so what is stored is
// what was planned.
func scopeDefaultsToWrite(plan, config MCPScopeDefaultsResourceModel) (adminapi.MCPScopeDefaultsWrite, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := adminapi.MCPScopeDefaultsWrite{}
	pair := func(value, mode types.String) *adminapi.MCPScopeDefault {
		v := gen.McpScopeDefaultValue(value.ValueString())
		md := gen.McpScopeDefaultMode(derivedMode(value.ValueString()))
		if !mode.IsNull() && !mode.IsUnknown() {
			md = gen.McpScopeDefaultMode(mode.ValueString())
		}
		return &adminapi.MCPScopeDefault{Value: &v, Mode: &md}
	}
	if !config.ServerApproval.IsNull() {
		body.ServerApproval = pair(plan.ServerApproval, plan.ServerApprovalMode)
	}
	if !config.Unattributed.IsNull() {
		body.Unattributed = pair(plan.Unattributed, plan.UnattributedMode)
	}

	var tv adminapi.MCPScopeToolVettingWrite
	vetting := false
	if !config.ToolVettingMode.IsNull() {
		mode := gen.McpScopeToolVettingWriteMode(plan.ToolVettingMode.ValueString())
		tv.Mode = &mode
		vetting = true
	}
	if !config.ToolVettingRiskPolicy.IsNull() {
		tv.RiskPolicy = localMcpRiskPolicyToSDK(plan.ToolVettingRiskPolicy)
		vetting = true
	}
	if !config.ToolVettingApprovalConfig.IsNull() {
		// The validator proved it decodes at plan; a failure here is an error,
		// never a skipped field, or state would record what was never sent.
		cfg, err := decodeApprovalConfig(plan.ToolVettingApprovalConfig.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("tool_vetting_approval_config"),
				"tool_vetting_approval_config could not be read", err.Error())
		} else {
			tv.ApprovalConfig = cfg
			vetting = true
		}
	}
	if vetting {
		body.ToolVetting = &tv
	}
	return body, diags
}

// scopeDefaultsToModel renders the platform's answer. For a device group the
// tool vetting fields are the group's OWN: one that follows the tenant
// default's reads as null (mode `inherit`), so a group config that sets none
// does not show the tenant's values as its own.
func scopeDefaultsToModel(tenantID string, groupID types.String, d *adminapi.MCPScopeDefaults) MCPScopeDefaultsResourceModel {
	m := MCPScopeDefaultsResourceModel{TenantID: types.StringValue(tenantID)}
	own := scopeSourceTenant
	if isUnsetString(groupID) {
		m.DeviceGroupID = types.StringNull()
		m.ID = types.StringValue(tenantID)
	} else {
		m.DeviceGroupID = groupID
		m.ID = types.StringValue(tenantID + "/" + groupID.ValueString())
		own = scopeSourceGroup
	}
	pair := func(p *adminapi.MCPScopeDefault) (types.String, types.String) {
		if p == nil {
			return types.StringNull(), types.StringNull()
		}
		return enumPtrToTF(p.Value), enumPtrToTF(p.Mode)
	}
	m.ServerApproval, m.ServerApprovalMode = pair(d.ServerApproval)
	m.RemoteEntry, m.RemoteEntryMode = pair(d.RemoteEntry)
	m.Unattributed, m.UnattributedMode = pair(d.Unattributed)
	m.Source = enumPtrToTF(d.Source)
	m.Version = int64PtrToTF(d.Version)

	m.ToolVettingMode = types.StringNull()
	m.ToolVettingRiskPolicy = types.ObjectNull(localMcpRiskPolicyAttrTypes)
	m.ToolVettingApprovalConfig = jsontypes.NewNormalizedNull()
	m.ToolVettingModeSource = types.StringNull()
	m.ToolVettingRiskPolicySource = types.StringNull()
	m.ToolVettingApprovalConfigSource = types.StringNull()
	if tv := d.ToolVetting; tv != nil {
		m.ToolVettingModeSource = enumPtrToTF(tv.ModeSource)
		m.ToolVettingRiskPolicySource = enumPtrToTF(tv.RiskPolicySource)
		m.ToolVettingApprovalConfigSource = enumPtrToTF(tv.ApprovalConfigSource)
		m.ToolVettingMode = enumPtrToTF(tv.Mode)
		if own == scopeSourceGroup && m.ToolVettingModeSource.ValueString() != scopeSourceGroup {
			m.ToolVettingMode = types.StringValue(scopeVettingInherit)
		}
		if m.ToolVettingRiskPolicySource.ValueString() == own {
			m.ToolVettingRiskPolicy = localMcpRiskPolicyToTF(tv.RiskPolicy)
		}
		if m.ToolVettingApprovalConfigSource.ValueString() == own {
			m.ToolVettingApprovalConfig = approvalConfigToTF(tv.ApprovalConfig)
		}
	}
	return m
}

// reconcileScopeApprovalConfig decides tool_vetting_approval_config in m,
// which holds the platform's form, and returns that form for private state.
// Sent and stored: the prior (planned) JSON. Not sent and unchanged since the
// form recorded last time: the prior JSON. Otherwise (changed elsewhere, or
// nothing recorded, as after an import): the platform's form, so a console
// edit shows as drift.
func reconcileScopeApprovalConfig(m *MCPScopeDefaultsResourceModel, prior MCPScopeDefaultsResourceModel,
	sent bool, recorded *string) *string {
	var form *string
	if !m.ToolVettingApprovalConfig.IsNull() {
		v := m.ToolVettingApprovalConfig.ValueString()
		form = &v
	}
	priorKnown := !prior.ToolVettingApprovalConfig.IsNull() && !prior.ToolVettingApprovalConfig.IsUnknown()
	switch {
	case sent:
		if form != nil && priorKnown {
			m.ToolVettingApprovalConfig = prior.ToolVettingApprovalConfig
		}
	case priorKnown && recorded != nil && form != nil && *recorded == *form:
		m.ToolVettingApprovalConfig = prior.ToolVettingApprovalConfig
	}
	return form
}

type approvalFormRecord struct {
	Form *string `json:"form,omitempty"`
}

// loadApprovalForm returns the recorded platform form, nil when nothing (or
// "none") was recorded.
func loadApprovalForm(ctx context.Context, p privateStateGetter) (*string, diag.Diagnostics) {
	if p == nil {
		return nil, nil
	}
	raw, diags := p.GetKey(ctx, scopeApprovalFormKey)
	if diags.HasError() || len(raw) == 0 {
		return nil, diags
	}
	var rec approvalFormRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, diags
	}
	return rec.Form, diags
}

func storeApprovalForm(ctx context.Context, p privateStateSetter, form *string) diag.Diagnostics {
	if p == nil {
		return nil
	}
	raw, err := json.Marshal(approvalFormRecord{Form: form})
	if err != nil {
		var d diag.Diagnostics
		d.AddError("Failed to record the platform's approval config", err.Error())
		return d
	}
	return p.SetKey(ctx, scopeApprovalFormKey, raw)
}

// scopeDefaultsOwnedChanges names the defaults body writes whose value in
// fresh differs from prior: what retryStalePostureVersion must not overwrite.
func scopeDefaultsOwnedChanges(prior, fresh MCPScopeDefaultsResourceModel, body adminapi.MCPScopeDefaultsWrite) []string {
	var fields []ownedField
	if body.ServerApproval != nil {
		fields = append(fields,
			ownedField{"server_approval", prior.ServerApproval, fresh.ServerApproval},
			ownedField{"server_approval_mode", prior.ServerApprovalMode, fresh.ServerApprovalMode})
	}
	if body.Unattributed != nil {
		fields = append(fields,
			ownedField{"unattributed", prior.Unattributed, fresh.Unattributed},
			ownedField{"unattributed_mode", prior.UnattributedMode, fresh.UnattributedMode})
	}
	if tv := body.ToolVetting; tv != nil {
		if tv.Mode != nil {
			fields = append(fields, ownedField{"tool_vetting_mode", prior.ToolVettingMode, fresh.ToolVettingMode})
		}
		if tv.RiskPolicy != nil {
			fields = append(fields, ownedField{"tool_vetting_risk_policy", prior.ToolVettingRiskPolicy, fresh.ToolVettingRiskPolicy})
		}
		if tv.ApprovalConfig != nil {
			fields = append(fields, ownedField{"tool_vetting_approval_config", prior.ToolVettingApprovalConfig, fresh.ToolVettingApprovalConfig})
		}
	}
	return changedFields(fields)
}
