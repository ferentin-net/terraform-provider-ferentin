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

// WorkloadOAuthClientResource is the `ferentin_workload_oauth_client`
// Terraform resource — the outbound OAuth credential the platform uses when
// an MCP server's `upstream_auth_strategy = cc_federated`. The platform mints
// a token at the customer's IdP using these credentials and forwards it
// upstream on the workload's behalf.
//
// Secret material (`client_secret`, `private_key_jwt_private_key`) is
// WriteOnly — values flow through to the platform during apply but never
// enter Terraform state. Bump the companion `*_wo_version` integer to
// rotate.
type WorkloadOAuthClientResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type WorkloadOAuthClientResourceModel struct {
	// Identity
	ID       types.String `tfsdk:"id"` // composite "<tenant>/<client_id_uuid>"
	TenantID types.String `tfsdk:"tenant_id"`
	ClientID types.String `tfsdk:"client_id_resource"` // server-generated UUID (clientID is taken by the IdP-side string)

	// Required user-supplied
	Name                  types.String `tfsdk:"name"`
	IdpType               types.String `tfsdk:"idp_type"`
	AuthMethod            types.String `tfsdk:"auth_method"`
	AudienceParamStrategy types.String `tfsdk:"audience_param_strategy"`
	OauthClientID         types.String `tfsdk:"oauth_client_id"` // the IdP-side client_id
	Issuer                types.String `tfsdk:"issuer"`
	JwksURI               types.String `tfsdk:"jwks_uri"`
	TokenEndpoint         types.String `tfsdk:"token_endpoint"`

	// Optional WriteOnly secrets
	ClientSecret                     types.String `tfsdk:"client_secret"`
	ClientSecretWOVersion            types.Int64  `tfsdk:"client_secret_wo_version"`
	PrivateKeyJwtPrivateKey          types.String `tfsdk:"private_key_jwt_private_key"`
	PrivateKeyJwtPrivateKeyWOVersion types.Int64  `tfsdk:"private_key_jwt_private_key_wo_version"`

	// Optional
	Description          types.String `tfsdk:"description"`
	DefaultAudience      types.String `tfsdk:"default_audience"`
	DefaultResource      types.String `tfsdk:"default_resource"`
	DefaultScopes        types.String `tfsdk:"default_scopes"`
	PrivateKeyJwtAlg     types.String `tfsdk:"private_key_jwt_alg"`
	PrivateKeyJwtJwksURL types.String `tfsdk:"private_key_jwt_jwks_url"`
	PrivateKeyJwtKid     types.String `tfsdk:"private_key_jwt_kid"`
	SsoIdpID             types.String `tfsdk:"sso_idp_id"`
	IsActive             types.Bool   `tfsdk:"is_active"`

	// Computed / server-set
	Version             types.Int64  `tfsdk:"version"`
	Direction           types.String `tfsdk:"direction"`
	HasClientSecret     types.Bool   `tfsdk:"has_client_secret"`
	HasPrivateKeyJwtKey types.Bool   `tfsdk:"has_private_key_jwt_key"`
	CreatedAt           types.String `tfsdk:"created_at"`
	CreatedBy           types.String `tfsdk:"created_by"`
	UpdatedAt           types.String `tfsdk:"updated_at"`
	UpdatedBy           types.String `tfsdk:"updated_by"`
	ManagedBy           types.String `tfsdk:"managed_by"`
	ManagedByClientID   types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule     types.String `tfsdk:"managed_by_module"`
	LastModifiedBy      types.String `tfsdk:"last_modified_by"`
}

func NewWorkloadOAuthClientResource() resource.Resource {
	return &WorkloadOAuthClientResource{}
}

var (
	_ resource.Resource                = (*WorkloadOAuthClientResource)(nil)
	_ resource.ResourceWithConfigure   = (*WorkloadOAuthClientResource)(nil)
	_ resource.ResourceWithImportState = (*WorkloadOAuthClientResource)(nil)
)

func (r *WorkloadOAuthClientResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workload_oauth_client"
}

func (r *WorkloadOAuthClientResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *WorkloadOAuthClientResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An outbound OAuth client the platform uses when an `ferentin_mcp_server` has " +
			"`upstream_auth_strategy = cc_federated`. The platform mints a token at the customer's IdP using these " +
			"credentials and forwards it to the upstream MCP server.\n\n" +
			"Secret material is WriteOnly: `client_secret` and `private_key_jwt_private_key` flow through to the " +
			"platform during apply but never enter Terraform state. Bump the companion `*_wo_version` integer to " +
			"rotate.\n\n" +
			"## Import\n\n" +
			"Existing clients can be imported using `<tenant_id>/<id>` (or `<id>` alone when the provider's default " +
			"`tenant_id` matches):\n\n" +
			"```\n" +
			"terraform import ferentin_workload_oauth_client.example <tenant_id>/<id>\n" +
			"```",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Composite Terraform resource ID `<tenant_id>/<workload_client_uuid>`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Tenant UUID. Defaults to provider-level `tenant_id`.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"client_id_resource": schema.StringAttribute{
				MarkdownDescription: "Server-generated UUID for this workload-client row. Distinct from " +
					"`oauth_client_id` (the IdP-side string identifier).",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},

			"name": schema.StringAttribute{
				MarkdownDescription: "Human-readable name for this client (e.g. `salesforce-prod-cc`).",
				Required:            true,
			},
			"idp_type": schema.StringAttribute{
				MarkdownDescription: "Identity-provider type. Allowed: `auth0`, `entra`, `generic_oidc`, `okta`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.OneOf("auth0", "entra", "generic_oidc", "okta"),
				},
			},
			"auth_method": schema.StringAttribute{
				MarkdownDescription: "Client authentication method. Allowed: `client_secret_basic`, `client_secret_post`, `private_key_jwt`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.OneOf("client_secret_basic", "client_secret_post", "private_key_jwt"),
				},
			},
			"audience_param_strategy": schema.StringAttribute{
				MarkdownDescription: "How the audience binds to the issued token. Allowed: `audience_param`, `pre_configured`, `resource_param`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.OneOf("audience_param", "pre_configured", "resource_param"),
				},
			},
			"oauth_client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth `client_id` at the customer's IdP (not Ferentin's internal UUID — see `client_id_resource`).",
				Required:            true,
			},
			"issuer": schema.StringAttribute{
				MarkdownDescription: "OIDC issuer URL of the customer's IdP.",
				Required:            true,
			},
			"jwks_uri": schema.StringAttribute{
				MarkdownDescription: "JWKS endpoint URL where the IdP publishes its signing keys.",
				Required:            true,
			},
			"token_endpoint": schema.StringAttribute{
				MarkdownDescription: "OAuth token endpoint URL.",
				Required:            true,
			},

			"client_secret": schema.StringAttribute{
				MarkdownDescription: "Plaintext client_secret. **WriteOnly** — flows through to the platform but " +
					"never enters Terraform state. Only meaningful when `auth_method` is `client_secret_*`. " +
					"Bump `client_secret_wo_version` to force a re-send on rotation.",
				Optional:  true,
				WriteOnly: true,
				Sensitive: true,
			},
			"client_secret_wo_version": schema.Int64Attribute{
				MarkdownDescription: "Companion to write-only `client_secret`. Bump to rotate.",
				Optional:            true,
			},
			"private_key_jwt_private_key": schema.StringAttribute{
				MarkdownDescription: "PEM-encoded private key for `private_key_jwt` auth. **WriteOnly**. " +
					"Bump `private_key_jwt_private_key_wo_version` to rotate.",
				Optional:  true,
				WriteOnly: true,
				Sensitive: true,
			},
			"private_key_jwt_private_key_wo_version": schema.Int64Attribute{
				MarkdownDescription: "Companion to write-only `private_key_jwt_private_key`. Bump to rotate.",
				Optional:            true,
			},

			"description":      schema.StringAttribute{Optional: true, Computed: true},
			"default_audience": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Default `audience` claim value. Overridable per MCP server."},
			"default_resource": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Default RFC 8707 `resource` parameter. Overridable per MCP server."},
			"default_scopes":   schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Default space-delimited scopes. Overridable per MCP server."},
			"private_key_jwt_alg": schema.StringAttribute{
				MarkdownDescription: "JWT signature algorithm for `private_key_jwt`. Allowed: `ES256`, `PS256`, `RS256`.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.OneOf("ES256", "PS256", "RS256"),
				},
			},
			"private_key_jwt_jwks_url": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "JWKS URL where the customer publishes the public key for this private_key_jwt config."},
			"private_key_jwt_kid":      schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "JWT `kid` header value."},
			"sso_idp_id":               schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Optional FK to a `ferentin_identity_provider` for SSO inheritance."},
			"is_active": schema.BoolAttribute{
				MarkdownDescription: "Whether the client is active for outbound mints. Default `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},

			"version":                 schema.Int64Attribute{Computed: true, MarkdownDescription: "Optimistic-concurrency version (platform #649)."},
			"direction":               schema.StringAttribute{Computed: true, MarkdownDescription: "Always `outbound` for cc_federated."},
			"has_client_secret":       schema.BoolAttribute{Computed: true},
			"has_private_key_jwt_key": schema.BoolAttribute{Computed: true},
			"created_at":              schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"created_by":              schema.StringAttribute{Computed: true},
			"updated_at":              schema.StringAttribute{Computed: true},
			"updated_by":              schema.StringAttribute{Computed: true},
			"managed_by":              schema.StringAttribute{Computed: true},
			"managed_by_client_id":    schema.StringAttribute{Computed: true},
			"managed_by_module":       schema.StringAttribute{Computed: true},
			"last_modified_by":        schema.StringAttribute{Computed: true},
		},
	}
}

func (r *WorkloadOAuthClientResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config WorkloadOAuthClientResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(plan.TenantID)

	body := adminapi.WorkloadOAuthClientCreate{
		Name:                  plan.Name.ValueString(),
		Issuer:                plan.Issuer.ValueString(),
		JwksUri:               plan.JwksURI.ValueString(),
		TokenEndpoint:         plan.TokenEndpoint.ValueString(),
		ClientId:              plan.OauthClientID.ValueString(),
		IdpType:               gen.WorkloadOAuthClientCreateRequestIdpType(plan.IdpType.ValueString()),
		AuthMethod:            gen.WorkloadOAuthClientCreateRequestAuthMethod(plan.AuthMethod.ValueString()),
		AudienceParamStrategy: gen.WorkloadOAuthClientCreateRequestAudienceParamStrategy(plan.AudienceParamStrategy.ValueString()),
	}
	setStringPtr(plan.Description, &body.Description)
	setStringPtr(plan.DefaultAudience, &body.DefaultAudience)
	setStringPtr(plan.DefaultResource, &body.DefaultResource)
	setStringPtr(plan.DefaultScopes, &body.DefaultScopes)
	setStringPtr(plan.PrivateKeyJwtJwksURL, &body.PrivateKeyJwtJwksUrl)
	setStringPtr(plan.PrivateKeyJwtKid, &body.PrivateKeyJwtKid)
	if !plan.PrivateKeyJwtAlg.IsNull() && !plan.PrivateKeyJwtAlg.IsUnknown() {
		v := gen.WorkloadOAuthClientCreateRequestPrivateKeyJwtAlg(plan.PrivateKeyJwtAlg.ValueString())
		body.PrivateKeyJwtAlg = &v
	}
	if !plan.SsoIdpID.IsNull() && !plan.SsoIdpID.IsUnknown() && plan.SsoIdpID.ValueString() != "" {
		id, err := parseUUID(plan.SsoIdpID.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("sso_idp_id"), "Invalid UUID", err.Error())
			return
		}
		body.SsoIdpId = &id
	}
	// WriteOnly secrets — pull from Config (Plan strips them).
	setStringPtr(config.ClientSecret, &body.ClientSecret)
	setStringPtr(config.PrivateKeyJwtPrivateKey, &body.PrivateKeyJwtPrivateKey)

	out, err := r.sdk.WorkloadOAuthClients().Create(ctx, tenantID, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create workload OAuth client", err)
		return
	}

	state := workloadOAuthClientToModel(tenantID, out)
	state.ClientSecretWOVersion = plan.ClientSecretWOVersion
	state.PrivateKeyJwtPrivateKeyWOVersion = plan.PrivateKeyJwtPrivateKeyWOVersion
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *WorkloadOAuthClientResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state WorkloadOAuthClientResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	out, err := r.sdk.WorkloadOAuthClients().Get(ctx, tenantID, state.ClientID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read workload OAuth client", err)
		return
	}

	refreshed := workloadOAuthClientToModel(tenantID, out)
	refreshed.ClientSecretWOVersion = state.ClientSecretWOVersion
	refreshed.PrivateKeyJwtPrivateKeyWOVersion = state.PrivateKeyJwtPrivateKeyWOVersion
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *WorkloadOAuthClientResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, config WorkloadOAuthClientResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)

	body := adminapi.WorkloadOAuthClientUpdate{}
	setStringPtr(plan.Name, &body.Name)
	setStringPtr(plan.Description, &body.Description)
	setStringPtr(plan.Issuer, &body.Issuer)
	setStringPtr(plan.JwksURI, &body.JwksUri)
	setStringPtr(plan.TokenEndpoint, &body.TokenEndpoint)
	setStringPtr(plan.OauthClientID, &body.ClientId)
	setStringPtr(plan.DefaultAudience, &body.DefaultAudience)
	setStringPtr(plan.DefaultResource, &body.DefaultResource)
	setStringPtr(plan.DefaultScopes, &body.DefaultScopes)
	setStringPtr(plan.PrivateKeyJwtJwksURL, &body.PrivateKeyJwtJwksUrl)
	setStringPtr(plan.PrivateKeyJwtKid, &body.PrivateKeyJwtKid)
	setBoolPtr(plan.IsActive, &body.IsActive)
	if !plan.IdpType.IsNull() && !plan.IdpType.IsUnknown() {
		v := gen.WorkloadOAuthClientUpdateRequestIdpType(plan.IdpType.ValueString())
		body.IdpType = &v
	}
	if !plan.AuthMethod.IsNull() && !plan.AuthMethod.IsUnknown() {
		v := gen.WorkloadOAuthClientUpdateRequestAuthMethod(plan.AuthMethod.ValueString())
		body.AuthMethod = &v
	}
	if !plan.AudienceParamStrategy.IsNull() && !plan.AudienceParamStrategy.IsUnknown() {
		v := gen.WorkloadOAuthClientUpdateRequestAudienceParamStrategy(plan.AudienceParamStrategy.ValueString())
		body.AudienceParamStrategy = &v
	}
	if !plan.PrivateKeyJwtAlg.IsNull() && !plan.PrivateKeyJwtAlg.IsUnknown() {
		v := gen.WorkloadOAuthClientUpdateRequestPrivateKeyJwtAlg(plan.PrivateKeyJwtAlg.ValueString())
		body.PrivateKeyJwtAlg = &v
	}
	// WriteOnly secrets — only send when the user bumped the *_wo_version companion.
	if !plan.ClientSecretWOVersion.Equal(state.ClientSecretWOVersion) {
		setStringPtr(config.ClientSecret, &body.ClientSecret)
	}
	if !plan.PrivateKeyJwtPrivateKeyWOVersion.Equal(state.PrivateKeyJwtPrivateKeyWOVersion) {
		setStringPtr(config.PrivateKeyJwtPrivateKey, &body.PrivateKeyJwtPrivateKey)
	}

	out, err := r.sdk.WorkloadOAuthClients().Update(ctx, tenantID, state.ClientID.ValueString(), version, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update workload OAuth client", err)
		return
	}

	refreshed := workloadOAuthClientToModel(tenantID, out)
	refreshed.ClientSecretWOVersion = plan.ClientSecretWOVersion
	refreshed.PrivateKeyJwtPrivateKeyWOVersion = plan.PrivateKeyJwtPrivateKeyWOVersion
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *WorkloadOAuthClientResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state WorkloadOAuthClientResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	err := r.sdk.WorkloadOAuthClients().Delete(ctx, tenantID, state.ClientID.ValueString(), version)
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete workload OAuth client", err)
	}
}

func (r *WorkloadOAuthClientResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, id string
	switch len(parts) {
	case 1:
		tenantID = r.tenantID
		id = parts[0]
	case 2:
		tenantID = parts[0]
		id = parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>/<id>` or configure `tenant_id` on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("client_id_resource"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+id)...)
}

func (r *WorkloadOAuthClientResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// workloadOAuthClientToModel maps SDK response → Terraform state. WriteOnly
// inputs (client_secret, private_key_jwt_private_key) are not stored; their
// *_wo_version companions are carried over by the caller.
func workloadOAuthClientToModel(tenantID string, c *adminapi.WorkloadOAuthClient) WorkloadOAuthClientResourceModel {
	m := WorkloadOAuthClientResourceModel{
		TenantID:                types.StringValue(tenantID),
		ClientSecret:            types.StringNull(),
		PrivateKeyJwtPrivateKey: types.StringNull(),
	}
	if c.Id != nil {
		m.ClientID = types.StringValue(c.Id.String())
	} else {
		m.ClientID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.ClientID.ValueString())

	m.Name = strPtrToTF(c.Name)
	m.IdpType = strPtrToTF(c.IdpType)
	m.AuthMethod = strPtrToTF(c.AuthMethod)
	m.AudienceParamStrategy = strPtrToTF(c.AudienceParamStrategy)
	m.OauthClientID = strPtrToTF(c.ClientId)
	m.Issuer = strPtrToTF(c.Issuer)
	m.JwksURI = strPtrToTF(c.JwksUri)
	m.TokenEndpoint = strPtrToTF(c.TokenEndpoint)
	m.Description = strPtrToTF(c.Description)
	m.DefaultAudience = strPtrToTF(c.DefaultAudience)
	m.DefaultResource = strPtrToTF(c.DefaultResource)
	m.DefaultScopes = strPtrToTF(c.DefaultScopes)
	m.PrivateKeyJwtAlg = strPtrToTF(c.PrivateKeyJwtAlg)
	m.PrivateKeyJwtJwksURL = strPtrToTF(c.PrivateKeyJwtJwksUrl)
	m.PrivateKeyJwtKid = strPtrToTF(c.PrivateKeyJwtKid)
	if c.SsoIdpId != nil {
		m.SsoIdpID = types.StringValue(c.SsoIdpId.String())
	} else {
		m.SsoIdpID = types.StringNull()
	}
	m.IsActive = boolPtrOrDefault(c.IsActive)
	m.Direction = strPtrToTF(c.Direction)
	m.HasClientSecret = boolPtrOrDefault(c.HasClientSecret)
	m.HasPrivateKeyJwtKey = boolPtrOrDefault(c.HasPrivateKeyJwtKey)
	m.Version = int64PtrToTF(c.Version)
	m.CreatedAt = timePtrToTF(c.CreatedAt)
	m.CreatedBy = strPtrToTF(c.CreatedBy)
	m.UpdatedAt = timePtrToTF(c.UpdatedAt)
	m.UpdatedBy = strPtrToTF(c.UpdatedBy)
	m.ManagedBy = strPtrToTF(c.ManagedBy)
	m.ManagedByClientID = strPtrToTF(c.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(c.ManagedByModule)
	m.LastModifiedBy = strPtrToTF(c.LastModifiedBy)
	return m
}
