package provider

import (
	"context"
	"errors"
	"fmt"
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

// AIAgentResource is the `ferentin_ai_agent` Terraform resource — a
// constrained subset of OIDC clients. `ai_client_type` is a generated column
// downstream of `grant_types` — see the schema notes on both attributes.
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
	GrantTypes              types.List   `tfsdk:"grant_types"`
	AccessTokenLifetime     types.Int64  `tfsdk:"access_token_lifetime"`
	RoleID                  types.String `tfsdk:"role_id"`
	Active                  types.Bool   `tfsdk:"active"`

	// Computed
	AgentID           types.String `tfsdk:"agent_id"`      // internal UUID
	ClientID          types.String `tfsdk:"client_id"`     // public fc_* identifier
	ClientSecret      types.String `tfsdk:"client_secret"` // sensitive, server-issued
	ClientType        types.String `tfsdk:"client_type"`
	AiClientType      types.String `tfsdk:"ai_client_type"`
	CreatedAt         types.String `tfsdk:"created_at"`
	UpdatedAt         types.String `tfsdk:"updated_at"`
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

func NewAIAgentResource() resource.Resource { return &AIAgentResource{} }

var (
	_ resource.Resource                   = (*AIAgentResource)(nil)
	_ resource.ResourceWithConfigure      = (*AIAgentResource)(nil)
	_ resource.ResourceWithImportState    = (*AIAgentResource)(nil)
	_ resource.ResourceWithValidateConfig = (*AIAgentResource)(nil)
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
		MarkdownDescription: "An AI-agent OIDC client. Set `grant_types = [\"client_credentials\"]` for a " +
			"machine-to-machine agent — the platform derives `ai_client_type` from that and from nothing " +
			"else, so an agent left on the platform default is stored as an `assistant`. " +
			"Scopes are constrained to the macro-only allowlist from ferentin-platform#648 — " +
			"`llm`, `mcp`, `summarizer`, plus OIDC standards (`openid`, `profile`, `email`, `offline_access`).\n\n" +
			"## Import\n\n" +
			"Existing agents can be imported using `<tenant_id>/<agent_id>` " +
			"(or `<agent_id>` alone when the provider's default `tenant_id` matches):\n\n" +
			"```\n" +
			"terraform import ferentin_ai_agent.example <tenant_id>/<agent_id>\n" +
			"```\n\n" +
			"After import, `client_secret` is **not** retrievable (the server returns it only on Create). " +
			"To rotate credentials, destroy and recreate the agent.",

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
			"name":           schema.StringAttribute{Required: true},
			"agent_platform": schema.StringAttribute{Required: true, MarkdownDescription: "Agent platform (`claude`, `chatgpt`, `microsoft_copilot`, …)."},
			"application_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`NATIVE`, `WEB`, or `SERVICE`.",
				Validators: []validator.String{
					stringvalidator.OneOf("NATIVE", "WEB", "SERVICE"),
				},
			},
			"description": schema.StringAttribute{Optional: true, Computed: true},
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
			"grant_types": schema.ListAttribute{
				MarkdownDescription: "OAuth2 grant types the client may use — e.g. `client_credentials` for a " +
					"machine-to-machine agent, or `authorization_code` + `refresh_token` for one that acts on " +
					"behalf of a signed-in user.\n\n" +
					"~> **This is what decides `ai_client_type`.** That column is generated by the platform as " +
					"`client_credentials` present → `agent`, otherwise → `assistant`. Nothing else influences " +
					"it, so an agent intended for M2M use that omits `client_credentials` is stored as an " +
					"`assistant` and cannot mint a token without a user.\n\n" +
					"Left unset, the platform applies its own default, which does **not** include " +
					"`client_credentials`. Set it explicitly for service agents — an " +
					"`application_type = \"SERVICE\"` agent that omits it gets a plan-time warning " +
					"naming the conventional set.\n\n" +
					"Deliberately not defaulted by the provider: this is a capability set, and deriving " +
					"it from `application_type` would change what a client can do without the change " +
					"appearing as an operator-authored diff. It can also emit a set the actor is not " +
					"permitted to write — `AgentClientScopeAllowlist` constrains agent clients to " +
					"`client_credentials` / `authorization_code` for an actor holding " +
					"`clients:agent:rw` but not `clients:rw`.",
				Optional: true, Computed: true,
				ElementType: types.StringType,
			},
			"jwks_uri":              schema.StringAttribute{Optional: true, Computed: true},
			"redirect_uris":         schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType},
			"access_token_lifetime": schema.Int64Attribute{Optional: true, Computed: true},
			"role_id":               schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Bound IaC/agent role UUID."},
			"active": schema.BoolAttribute{
				MarkdownDescription: "Whether the agent's OIDC client is active and accepting tokens. Default `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},

			"agent_id":      schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"client_id":     schema.StringAttribute{Computed: true, MarkdownDescription: "Public fc_* identifier the agent presents at runtime.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"client_secret": schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Server-issued secret; only returned on Create for client_secret_* auth methods.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"client_type":   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"ai_client_type": schema.StringAttribute{
				MarkdownDescription: "`agent` or `assistant`, **generated by the platform** from " +
					"`grant_types`: `client_credentials` present → `agent`, otherwise → `assistant`. " +
					"Read-only — a value sent on the wire is discarded, so this is set by changing " +
					"`grant_types`.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at":           schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"updated_at":           schema.StringAttribute{Computed: true},
			"managed_by":           schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"managed_by_client_id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"managed_by_module":    schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent writer. Equal to `managed_by` at create " +
					"time; **divergence is the drift signal** — `managed_by = \"iac\"` with " +
					"`last_modified_by = \"console\"` means somebody edited a Terraform-managed agent in " +
					"the admin console.\n\n" +
					"Note this reported `unknown` on every read until the platform added the provenance " +
					"columns to its OIDC-client projection; a stale admin-api still will.",
				Computed: true,
			},
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
	// NOT set here: ai_client_type. The platform column is GENERATED ALWAYS AS
	// (client_credentials = ANY(grant_types) -> 'agent', else 'assistant'), so a
	// value sent on the wire is discarded. This code used to force 'agent' and
	// silently got whatever grant_types implied — set `grant_types` instead.
	agent, err := r.sdk.OIDCClients().Create(ctx, tenantID, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create AI agent client", err)
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
		addSDKError(&resp.Diagnostics, "Failed to read AI agent client", err)
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
	// ai_client_type is a generated column — see the note in Create.
	agent, err := r.sdk.OIDCClients().Update(ctx, tenantID, state.AgentID.ValueString(), "", body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update AI agent client", err)
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
		addSDKError(&resp.Diagnostics, "Failed to delete AI agent client", err)
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
	if !plan.GrantTypes.IsNull() && !plan.GrantTypes.IsUnknown() {
		s := stringListToSDK(ctx, plan.GrantTypes)
		body.GrantTypes = &s
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
	m.LastModifiedBy = enumPtrToTF(a.LastModifiedBy)
	m.Scopes = stringSliceToList(a.Scopes)
	m.RedirectUris = stringSliceToList(a.RedirectUris)
	m.GrantTypes = stringSliceToList(a.GrantTypes)
	if a.RoleId != nil {
		m.RoleID = types.StringValue(a.RoleId.String())
	} else {
		m.RoleID = types.StringNull()
	}
	return m
}
