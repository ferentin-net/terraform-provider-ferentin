package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// AIAgentResource is the `ferentin_ai_agent` Terraform resource — a
// constrained subset of OIDC clients (where `ai_client_type='agent'`).
// Restricted by the platform's AgentClientScopeAllowlist (#648) to the
// macro-only scope set: `llm`, `mcp`, `summarizer` + OIDC standards.
// Other OIDC client types (admin, SSO, service) stay outside Terraform.
type AIAgentResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type AIAgentResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	// Required
	Name            types.String `tfsdk:"name"`
	AgentPlatform   types.String `tfsdk:"agent_platform"`
	ApplicationType types.String `tfsdk:"application_type"`

	// Optional
	Description             types.String `tfsdk:"description"`
	Scopes                  types.List   `tfsdk:"scopes"`
	TokenEndpointAuthMethod types.String `tfsdk:"token_endpoint_auth_method"`
	JwksURI                 types.String `tfsdk:"jwks_uri"`
	RedirectUris            types.List   `tfsdk:"redirect_uris"`
	AccessTokenLifetime     types.Int64  `tfsdk:"access_token_lifetime"`
	RoleID                  types.String `tfsdk:"role_id"`
	Active                  types.Bool   `tfsdk:"active"`

	// Computed
	AgentID                  types.String `tfsdk:"agent_id"` // internal UUID
	ClientID                 types.String `tfsdk:"client_id"` // public fc_* identifier
	ClientSecret             types.String `tfsdk:"client_secret"` // sensitive, server-issued
	ClientType               types.String `tfsdk:"client_type"`
	AiClientType             types.String `tfsdk:"ai_client_type"`
	CreatedAt                types.String `tfsdk:"created_at"`
	UpdatedAt                types.String `tfsdk:"updated_at"`
	ManagedBy                types.String `tfsdk:"managed_by"`
	ManagedByClientID        types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule          types.String `tfsdk:"managed_by_module"`
}

func NewAIAgentResource() resource.Resource { return &AIAgentResource{} }

var (
	_ resource.Resource                = (*AIAgentResource)(nil)
	_ resource.ResourceWithConfigure   = (*AIAgentResource)(nil)
	_ resource.ResourceWithImportState = (*AIAgentResource)(nil)
)

func (r *AIAgentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_agent"
}

func (r *AIAgentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AIAgentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An AI-agent OIDC client (where the underlying `ai_client_type='agent'`). " +
			"Scopes are constrained to the macro-only allowlist from ferentin-platform#648 — " +
			"`llm`, `mcp`, `summarizer`, plus OIDC standards (`openid`, `profile`, `email`, `offline_access`).",

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
			"name":              schema.StringAttribute{Required: true},
			"agent_platform":    schema.StringAttribute{Required: true, MarkdownDescription: "Agent platform (`claude`, `chatgpt`, `microsoft_copilot`, …)."},
			"application_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`NATIVE`, `WEB`, or `SERVICE`.",
				Validators: []validator.String{
					stringvalidator.OneOf("NATIVE", "WEB", "SERVICE"),
				},
			},
			"description":       schema.StringAttribute{Optional: true, Computed: true},
			"scopes": schema.ListAttribute{
				MarkdownDescription: "Constrained to the agent-client allowlist: `llm`, `mcp`, `summarizer`, " +
					"plus OIDC standards. The platform's AgentClientScopeAllowlist rejects anything else.",
				Optional: true, Computed: true,
				ElementType: types.StringType,
			},
			"token_endpoint_auth_method": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "`private_key_jwt`, `client_secret_basic`, `client_secret_post`, or `none`.",
				Validators: []validator.String{
					stringvalidator.OneOf("private_key_jwt", "client_secret_basic", "client_secret_post", "none"),
				},
			},
			"jwks_uri":              schema.StringAttribute{Optional: true, Computed: true},
			"redirect_uris":         schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType},
			"access_token_lifetime": schema.Int64Attribute{Optional: true, Computed: true},
			"role_id":               schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Bound IaC/agent role UUID."},
			"active":                schema.BoolAttribute{Optional: true, Computed: true},

			"agent_id":      schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"client_id":     schema.StringAttribute{Computed: true, MarkdownDescription: "Public fc_* identifier the agent presents at runtime.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"client_secret": schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Server-issued secret; only returned on Create for client_secret_* auth methods.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"client_type":   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"ai_client_type": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"created_at":           schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"updated_at":           schema.StringAttribute{Computed: true},
			"managed_by":           schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"managed_by_client_id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"managed_by_module":    schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *AIAgentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AIAgentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	body := adminapi.OIDCClientRow{
		Name:            plan.Name.ValueString(),
		ApplicationType: gen.OidcClientApplicationType(plan.ApplicationType.ValueString()),
		ClientType:      clientTypeForApplicationType(plan.ApplicationType.ValueString()),
	}
	if err := r.fillBody(ctx, &plan, &body); err != nil {
		resp.Diagnostics.AddError("Invalid AI agent config", err.Error())
		return
	}
	// Force ai_client_type='agent' — that's the entire point of this resource.
	ait := aiAgentClientType
	body.AiClientType = &ait

	agent, err := r.sdk.OIDCClients().Create(ctx, tenantID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create AI agent client", err.Error())
		return
	}
	state := agentToModel(tenantID, agent)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AIAgentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AIAgentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	agent, err := r.sdk.OIDCClients().Get(ctx, tenantID, state.AgentID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read AI agent client", err.Error())
		return
	}
	refreshed := agentToModel(tenantID, agent)
	// Preserve the server-issued client_secret from prior state (Read doesn't return it).
	refreshed.ClientSecret = state.ClientSecret
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *AIAgentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state AIAgentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	body := adminapi.OIDCClientRow{
		Name:            plan.Name.ValueString(),
		ApplicationType: gen.OidcClientApplicationType(plan.ApplicationType.ValueString()),
		ClientType:      clientTypeForApplicationType(plan.ApplicationType.ValueString()),
	}
	if err := r.fillBody(ctx, &plan, &body); err != nil {
		resp.Diagnostics.AddError("Invalid AI agent config", err.Error())
		return
	}
	ait := aiAgentClientType
	body.AiClientType = &ait

	agent, err := r.sdk.OIDCClients().Update(ctx, tenantID, state.AgentID.ValueString(), "", body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update AI agent client", err.Error())
		return
	}
	refreshed := agentToModel(tenantID, agent)
	refreshed.ClientSecret = state.ClientSecret
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *AIAgentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AIAgentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	err := r.sdk.OIDCClients().Delete(ctx, tenantID, state.AgentID.ValueString(), "")
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		resp.Diagnostics.AddError("Failed to delete AI agent client", err.Error())
	}
}

func (r *AIAgentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, agentID string
	switch len(parts) {
	case 1:
		tenantID, agentID = r.tenantID, parts[0]
	case 2:
		tenantID, agentID = parts[0], parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>/<agent_id>` or configure `tenant_id` on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("agent_id"), agentID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+agentID)...)
}

func (r *AIAgentResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// aiAgentClientType is the constant ai_client_type used for every agent
// row this resource creates — M1 from the review (consolidate string literals
// that were duplicated in Create + Update).
const aiAgentClientType gen.OidcClientAiClientType = "agent"

// clientTypeForApplicationType picks the OAuth client_type based on the
// agent's application_type. M2 from the review: NATIVE / WEB are public
// clients (no secret round-trip), SERVICE is confidential. Defaults to
// CONFIDENTIAL when the application_type is unrecognized — the conservative
// choice.
func clientTypeForApplicationType(appType string) gen.OidcClientClientType {
	switch strings.ToUpper(appType) {
	case "NATIVE", "WEB":
		return gen.OidcClientClientType("PUBLIC")
	default:
		return gen.OidcClientClientType("CONFIDENTIAL")
	}
}

func (r *AIAgentResource) fillBody(ctx context.Context, plan *AIAgentResourceModel, body *adminapi.OIDCClientRow) error {
	setStringPtr(plan.Description, &body.Description)
	setStringPtr(plan.TokenEndpointAuthMethod, &body.TokenEndpointAuthMethod)
	setStringPtr(plan.JwksURI, &body.ClientJwksUri)
	setInt32Ptr(plan.AccessTokenLifetime, &body.AccessTokenLifetime)
	setBoolPtr(plan.Active, &body.Active)
	if !plan.AgentPlatform.IsNull() && !plan.AgentPlatform.IsUnknown() {
		v := gen.OidcClientAgentPlatform(plan.AgentPlatform.ValueString())
		body.AgentPlatform = &v
	}
	if !plan.Scopes.IsNull() && !plan.Scopes.IsUnknown() {
		s := stringListToSDK(ctx, plan.Scopes)
		body.Scopes = &s
	}
	if !plan.RedirectUris.IsNull() && !plan.RedirectUris.IsUnknown() {
		s := stringListToSDK(ctx, plan.RedirectUris)
		body.RedirectUris = &s
	}
	if !plan.RoleID.IsNull() && !plan.RoleID.IsUnknown() {
		rid, err := parseUUID(plan.RoleID.ValueString())
		if err != nil {
			return fmt.Errorf("role_id %q is not a valid UUID: %w", plan.RoleID.ValueString(), err)
		}
		body.RoleId = &rid
	}
	return nil
}

func agentToModel(tenantID string, a *adminapi.OIDCClientRow) AIAgentResourceModel {
	m := AIAgentResourceModel{TenantID: types.StringValue(tenantID)}
	if a.Id != nil {
		m.AgentID = types.StringValue(a.Id.String())
	} else {
		m.AgentID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.AgentID.ValueString())
	m.Name = types.StringValue(a.Name)
	m.ApplicationType = types.StringValue(string(a.ApplicationType))
	m.ClientType = types.StringValue(string(a.ClientType))
	m.ClientID = strPtrToTF(a.ClientId)
	if a.ClientSecret != nil {
		m.ClientSecret = types.StringValue(*a.ClientSecret)
	} else {
		m.ClientSecret = types.StringNull()
	}
	m.AgentPlatform = enumPtrToTF(a.AgentPlatform)
	m.AiClientType = enumPtrToTF(a.AiClientType)
	m.Description = strPtrToTF(a.Description)
	m.TokenEndpointAuthMethod = strPtrToTF(a.TokenEndpointAuthMethod)
	m.JwksURI = strPtrToTF(a.ClientJwksUri)
	m.AccessTokenLifetime = int32PtrToTF(a.AccessTokenLifetime)
	m.Active = boolPtrOrDefault(a.Active)
	m.CreatedAt = timePtrToTF(a.CreatedAt)
	m.UpdatedAt = timePtrToTF(a.UpdatedAt)
	m.ManagedBy = enumPtrToTF(a.ManagedBy)
	m.ManagedByClientID = strPtrToTF(a.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(a.ManagedByModule)
	m.Scopes = stringSliceToList(a.Scopes)
	m.RedirectUris = stringSliceToList(a.RedirectUris)
	if a.RoleId != nil {
		m.RoleID = types.StringValue(a.RoleId.String())
	} else {
		m.RoleID = types.StringNull()
	}
	return m
}
