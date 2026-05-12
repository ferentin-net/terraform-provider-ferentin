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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// MCPPolicyResource is the `ferentin_mcp_policy` Terraform resource — ABAC
// policy for MCP traffic with allow/deny effects on tools/toolsets and
// optional rate-limits. v0.1 covers the common shape (§6.6 design-doc
// example); nested ContextGuards / Redaction / TrustflowCeiling /
// ProtocolVersionTargeting / McpLoggingConfig deferred to v0.2.
type MCPPolicyResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type MCPPolicyResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	Priority          types.Int64  `tfsdk:"priority"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	ProviderInstances types.List   `tfsdk:"provider_instances"`
	ValidateArguments types.Bool   `tfsdk:"validate_arguments"`

	Effect *MCPPolicyEffectModel `tfsdk:"effect"`

	PolicyID  types.String `tfsdk:"policy_id"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

// MCPPolicyEffectModel mirrors gen.McpEffectDto.
type MCPPolicyEffectModel struct {
	Type               types.String `tfsdk:"type"` // allow | deny
	Message            types.String `tfsdk:"message"`
	AllowedTools       types.List   `tfsdk:"allowed_tools"`
	DeniedTools        types.List   `tfsdk:"denied_tools"`
	GrantToolsets      types.List   `tfsdk:"grant_toolsets"`
	DenyToolsets       types.List   `tfsdk:"deny_toolsets"`
	RateLimitPerMinute types.Int64  `tfsdk:"rate_limit_per_minute"`
}

func NewMCPPolicyResource() resource.Resource { return &MCPPolicyResource{} }

var (
	_ resource.Resource                = (*MCPPolicyResource)(nil)
	_ resource.ResourceWithConfigure   = (*MCPPolicyResource)(nil)
	_ resource.ResourceWithImportState = (*MCPPolicyResource)(nil)
)

func (r *MCPPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_policy"
}

func (r *MCPPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *MCPPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Tenant MCP policy — ABAC governance for MCP traffic (allow/deny on tools / " +
			"toolsets, optional rate limiting). Nested context_guards / redaction / trustflow / logging deferred " +
			"to v0.2; see §6.6 of the design doc for an example.\n\n" +
			"## Import\n\n" +
			"Existing policies can be imported using `<tenant_id>/<policy_id>` " +
			"(or `<policy_id>` alone when the provider's default `tenant_id` matches):\n\n" +
			"```\n" +
			"terraform import ferentin_mcp_policy.example <tenant_id>/<policy_id>\n" +
			"```",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant_id": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name":        schema.StringAttribute{Required: true},
			"description": schema.StringAttribute{Optional: true, Computed: true},
			"priority":    schema.Int64Attribute{Optional: true, Computed: true},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the policy is enforced. Default `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			"validate_arguments": schema.BoolAttribute{
				MarkdownDescription: "When true, validate tool-call arguments against the policy schema. Default `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			"provider_instances": schema.ListAttribute{
				MarkdownDescription: "Instance names (`ferentin_mcp_server.*.name`) this policy applies to.",
				Optional:            true, Computed: true,
				ElementType: types.StringType,
			},
			"effect": schema.SingleNestedAttribute{
				MarkdownDescription: "Required effect — allow / deny + tool/toolset constraints + optional rate limit.",
				Required:            true,
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						MarkdownDescription: "Effect type: `allow` or `deny`.",
						Required:            true,
						Validators: []validator.String{
							stringvalidator.OneOf("allow", "deny"),
						},
					},
					"message": schema.StringAttribute{
						MarkdownDescription: "Optional message surfaced to the agent when the policy applies.",
						Optional:            true,
					},
					"allowed_tools":  schema.ListAttribute{Optional: true, ElementType: types.StringType},
					"denied_tools":   schema.ListAttribute{Optional: true, ElementType: types.StringType},
					"grant_toolsets": schema.ListAttribute{Optional: true, ElementType: types.StringType},
					"deny_toolsets":  schema.ListAttribute{Optional: true, ElementType: types.StringType},
					"rate_limit_per_minute": schema.Int64Attribute{
						MarkdownDescription: "Per-minute rate limit for matching traffic.",
						Optional:            true,
					},
				},
			},

			"policy_id":  schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"created_at": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"updated_at": schema.StringAttribute{Computed: true},
		},
	}
}

// Create vs Update body shape note: McpPolicyCreateRequest has Name, Priority,
// ProviderInstances, and Effect as required NON-POINTER fields (gen reflects
// the platform's Create contract). McpPolicyUpdateRequest has all fields as
// pointers (a sparse-update contract). The two code paths look different
// because the contracts differ; don't unify them.

func (r *MCPPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MCPPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	body := adminapi.MCPPolicyCreate{
		Name:              plan.Name.ValueString(),
		Priority:          int32(plan.Priority.ValueInt64()),
		ProviderInstances: stringListToSDK(ctx, plan.ProviderInstances),
		Effect:            plan.toEffect(ctx),
	}
	if !plan.Priority.IsNull() && !plan.Priority.IsUnknown() {
		body.Priority = int32(plan.Priority.ValueInt64())
	} else {
		body.Priority = 100
	}
	setStringPtr(plan.Description, &body.Description)
	setBoolPtr(plan.Enabled, &body.Enabled)
	setBoolPtr(plan.ValidateArguments, &body.ValidateArguments)

	pol, err := r.sdk.MCPPolicies().Create(ctx, tenantID, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create MCP policy", err)
		return
	}
	state := mcpPolicyToModel(tenantID, pol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *MCPPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MCPPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	pol, err := r.sdk.MCPPolicies().Get(ctx, tenantID, state.PolicyID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read MCP policy", err)
		return
	}
	refreshed := mcpPolicyToModel(tenantID, pol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state MCPPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	// MCPPolicyUpdate fields are all pointers (vs Create where Name and
	// Priority and ProviderInstances are required non-pointers).
	body := adminapi.MCPPolicyUpdate{}
	setStringPtr(plan.Name, &body.Name)
	setStringPtr(plan.Description, &body.Description)
	setInt32Ptr(plan.Priority, &body.Priority)
	setBoolPtr(plan.Enabled, &body.Enabled)
	setBoolPtr(plan.ValidateArguments, &body.ValidateArguments)
	if !plan.ProviderInstances.IsNull() && !plan.ProviderInstances.IsUnknown() {
		s := stringListToSDK(ctx, plan.ProviderInstances)
		body.ProviderInstances = &s
	}
	if plan.Effect != nil {
		eff := plan.toEffect(ctx)
		body.Effect = &eff
	}

	v := strconv.FormatInt(0, 10)
	pol, err := r.sdk.MCPPolicies().Update(ctx, tenantID, state.PolicyID.ValueString(), v, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update MCP policy", err)
		return
	}
	refreshed := mcpPolicyToModel(tenantID, pol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MCPPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	err := r.sdk.MCPPolicies().Delete(ctx, tenantID, state.PolicyID.ValueString(), "")
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete MCP policy", err)
	}
}

func (r *MCPPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, policyID string
	switch len(parts) {
	case 1:
		tenantID, policyID = r.tenantID, parts[0]
	case 2:
		tenantID, policyID = parts[0], parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>/<policy_id>` or configure `tenant_id` on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_id"), policyID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+policyID)...)
}

func (r *MCPPolicyResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

func (m *MCPPolicyResourceModel) toEffect(ctx context.Context) gen.McpEffectDto {
	if m.Effect == nil {
		return gen.McpEffectDto{Effect: gen.McpEffectDtoEffect("allow")}
	}
	e := gen.McpEffectDto{
		Effect: gen.McpEffectDtoEffect(m.Effect.Type.ValueString()),
	}
	if !m.Effect.Message.IsNull() && !m.Effect.Message.IsUnknown() {
		v := m.Effect.Message.ValueString()
		e.Message = &v
	}
	if !m.Effect.RateLimitPerMinute.IsNull() && !m.Effect.RateLimitPerMinute.IsUnknown() {
		v := int32(m.Effect.RateLimitPerMinute.ValueInt64())
		e.RateLimitPerMinute = &v
	}
	if !m.Effect.AllowedTools.IsNull() && !m.Effect.AllowedTools.IsUnknown() {
		s := stringListToSDK(ctx, m.Effect.AllowedTools)
		e.AllowedTools = &s
	}
	if !m.Effect.DeniedTools.IsNull() && !m.Effect.DeniedTools.IsUnknown() {
		s := stringListToSDK(ctx, m.Effect.DeniedTools)
		e.DeniedTools = &s
	}
	if !m.Effect.GrantToolsets.IsNull() && !m.Effect.GrantToolsets.IsUnknown() {
		s := stringListToSDK(ctx, m.Effect.GrantToolsets)
		e.GrantToolsets = &s
	}
	if !m.Effect.DenyToolsets.IsNull() && !m.Effect.DenyToolsets.IsUnknown() {
		s := stringListToSDK(ctx, m.Effect.DenyToolsets)
		e.DenyToolsets = &s
	}
	return e
}

func mcpPolicyToModel(tenantID string, pol *adminapi.MCPPolicy) MCPPolicyResourceModel {
	m := MCPPolicyResourceModel{TenantID: types.StringValue(tenantID)}
	if pol.Id != nil {
		m.PolicyID = types.StringValue(pol.Id.String())
	} else {
		m.PolicyID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.PolicyID.ValueString())
	m.Name = strPtrToTF(pol.Name)
	m.Description = strPtrToTF(pol.Description)
	m.Priority = int32PtrToTF(pol.Priority)
	m.Enabled = boolPtrOrDefault(pol.Enabled)
	m.ValidateArguments = boolPtrOrDefault(pol.ValidateArguments)
	m.CreatedAt = timePtrToTF(pol.CreatedAt)
	m.UpdatedAt = timePtrToTF(pol.UpdatedAt)
	m.ProviderInstances = stringSliceToList(pol.ProviderInstances)

	if pol.Effect != nil {
		m.Effect = &MCPPolicyEffectModel{
			Type:               types.StringValue(string(pol.Effect.Effect)),
			Message:            strPtrToTF(pol.Effect.Message),
			AllowedTools:       stringSliceToList(pol.Effect.AllowedTools),
			DeniedTools:        stringSliceToList(pol.Effect.DeniedTools),
			GrantToolsets:      stringSliceToList(pol.Effect.GrantToolsets),
			DenyToolsets:       stringSliceToList(pol.Effect.DenyToolsets),
			RateLimitPerMinute: int32PtrToTF(pol.Effect.RateLimitPerMinute),
		}
	}
	return m
}
