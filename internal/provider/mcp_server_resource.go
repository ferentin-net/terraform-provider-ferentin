package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/google/uuid"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// MCPServerResource is the `ferentin_mcp_server` Terraform resource — a
// tenant binding of an MCP provider (catalog entry) to a specific endpoint /
// credential set / routing config. Phase 3 first resource.
//
// The platform's response DTO has ~45 fields; for v0.1 we surface the
// commonly-used subset. Deferred to follow-up versions:
//   - CC federated overrides (`cc_federated_*`)
//   - Per-server provider config / rate-limit / constraint maps
//     (complex nested map<string, map<string, interface{}>> — would require
//     a `dynamic` attribute or per-key schema)
//   - Mint-failure tracking / encryption-key versioning (operational telemetry)
//   - Discovery internals (last_discovered_at, validation_status, etc.)
type MCPServerResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type MCPServerResourceModel struct {
	// Identity
	ID       types.String `tfsdk:"id"` // composite "<tenant>/<server_uuid>"
	TenantID types.String `tfsdk:"tenant_id"`

	// Required user-supplied
	ProviderID types.String `tfsdk:"provider_id"` // UUID of the parent ferentin_mcp_provider
	Name       types.String `tfsdk:"name"`
	Endpoint   types.String `tfsdk:"endpoint"` // upstream MCP server URL

	// Optional user-supplied
	DisplayName          types.String `tfsdk:"display_name"`
	Description          types.String `tfsdk:"description"`
	Icon                 types.String `tfsdk:"icon"`
	Enabled              types.Bool   `tfsdk:"enabled"`
	Priority             types.Int64  `tfsdk:"priority"`
	HealthCheckURL       types.String `tfsdk:"health_check_url"`
	EdgeSiteID           types.String `tfsdk:"edge_site_id"`
	DeploymentMode       types.String `tfsdk:"deployment_mode"`
	UpstreamAuthStrategy types.String `tfsdk:"upstream_auth_strategy"`
	AuthMode             types.String `tfsdk:"auth_mode"`
	TransportType        types.String `tfsdk:"transport_type"`
	EnabledScopes        types.List   `tfsdk:"enabled_scopes"` // []string
	Tags                 types.Map    `tfsdk:"tags"`           // map[string]string
	BearerToken          types.String `tfsdk:"bearer_token"`   // sugar for env = {BEARER_TOKEN: ...}
	Env                  types.Map    `tfsdk:"env"`            // map[string]string — bearer tokens / API keys, encrypted server-side

	// CC-federation overrides — only meaningful when
	// upstream_auth_strategy = "cc_federated". The FK points at a
	// ferentin_workload_oauth_client; the *_override fields narrow that
	// client's `default_*` per-server.
	CcFederatedWorkloadClientID types.String `tfsdk:"cc_federated_workload_client_id"`
	CcFederatedAudienceOverride types.String `tfsdk:"cc_federated_audience_override"`
	CcFederatedResourceOverride types.String `tfsdk:"cc_federated_resource_override"`
	CcFederatedScopesOverride   types.String `tfsdk:"cc_federated_scopes_override"`

	// Computed-only: ProviderAuthType is inferred by the platform from the
	// upstream_auth_strategy + provider config; not user-settable through this
	// input DTO. The response exposes it; v0.1 surfaces it as read-only.
	ProviderAuthType types.String `tfsdk:"provider_auth_type"`

	// Computed / server-set
	ServerID              types.String `tfsdk:"server_id"`
	Slug                  types.String `tfsdk:"slug"`
	Version               types.Int64  `tfsdk:"version"`
	AvailableForRouting   types.Bool   `tfsdk:"available_for_routing"`
	Healthy               types.Bool   `tfsdk:"healthy"`
	HealthStatus          types.String `tfsdk:"health_status"`
	CredentialsConfigured types.Bool   `tfsdk:"credentials_configured"`
	ClientFacingURL       types.String `tfsdk:"client_facing_url"`
	CreatedAt             types.String `tfsdk:"created_at"`
	CreatedBy             types.String `tfsdk:"created_by"`
	UpdatedAt             types.String `tfsdk:"updated_at"`
	UpdatedBy             types.String `tfsdk:"updated_by"`
	ManagedBy             types.String `tfsdk:"managed_by"`
	ManagedByClientID     types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule       types.String `tfsdk:"managed_by_module"`
	LastModifiedBy        types.String `tfsdk:"last_modified_by"`
}

func NewMCPServerResource() resource.Resource {
	return &MCPServerResource{}
}

var (
	_ resource.Resource                     = (*MCPServerResource)(nil)
	_ resource.ResourceWithConfigure        = (*MCPServerResource)(nil)
	_ resource.ResourceWithImportState      = (*MCPServerResource)(nil)
	_ resource.ResourceWithConfigValidators = (*MCPServerResource)(nil)
)

func (r *MCPServerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_server"
}

// ConfigValidators — bearer_token is sugar for env={BEARER_TOKEN:...};
// setting both is ambiguous, so reject at plan time.
func (r *MCPServerResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.Conflicting(
			path.MatchRoot("bearer_token"),
			path.MatchRoot("env"),
		),
	}
}

func (r *MCPServerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *MCPServerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A tenant-scoped MCP server — a binding of an MCP provider (catalog entry, see " +
			"`ferentin_mcp_provider`) to a specific endpoint / credential set / routing config. Multiple servers " +
			"per provider are allowed (e.g., per-region, per-env).\n\n" +
			"## Import\n\n" +
			"Existing servers can be imported using `<tenant_id>/<server_id>` " +
			"(or `<server_id>` alone when the provider's default `tenant_id` matches):\n\n" +
			"```\n" +
			"terraform import ferentin_mcp_server.example <tenant_id>/<server_id>\n" +
			"```",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Composite Terraform resource ID `<tenant_id>/<server_id>`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Tenant UUID. Defaults to the provider-level value.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"provider_id": schema.StringAttribute{
				MarkdownDescription: "UUID of the parent MCP provider (from `ferentin_mcp_provider.id` or " +
					"the global catalog). Immutable post-create.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Human-readable name for this server instance (e.g. `salesforce-prod-us`).",
				Required:            true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Upstream MCP server URL (e.g. `https://mcp.salesforce.com/sse`).",
				Required:            true,
			},
			"display_name": schema.StringAttribute{
				MarkdownDescription: "Display name shown in the admin console.",
				Optional:            true,
				Computed:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Optional description.",
				Optional:            true,
				Computed:            true,
			},
			"icon": schema.StringAttribute{
				MarkdownDescription: "Icon identifier (Lucide / Simple Icons name) for console rendering.",
				Optional:            true,
				Computed:            true,
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "When false, the server is registered but skipped by routing. " +
					"Equivalent to the platform's Enable / Disable verbs at runtime. Default `true`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"priority": schema.Int64Attribute{
				MarkdownDescription: "Routing priority (lower is higher).",
				Optional:            true,
				Computed:            true,
			},
			"health_check_url": schema.StringAttribute{
				MarkdownDescription: "Optional custom health-check URL.",
				Optional:            true,
				Computed:            true,
			},
			"edge_site_id": schema.StringAttribute{
				MarkdownDescription: "Edge site this server is bound to, when applicable. Pull from " +
					"`ferentin_edge_site.us_east.site_id` for edge-routed servers.",
				Optional: true,
				Computed: true,
			},
			"deployment_mode": schema.StringAttribute{
				MarkdownDescription: "Deployment routing mode. Allowed: `public` (cloud-routed), `edge_routed`, " +
					"or null (cloud default).",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.OneOf("public", "edge_routed"),
				},
			},
			"provider_auth_type": schema.StringAttribute{
				MarkdownDescription: "Computed: authentication type the platform inferred for the provider. " +
					"Not directly user-settable; influenced by `upstream_auth_strategy` and the provider's catalog config.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"upstream_auth_strategy": schema.StringAttribute{
				MarkdownDescription: "Strategy for authenticating to the upstream MCP server. Allowed: " +
					"`none`, `oauth2_user`, `cc_federated`, `custom_headers`, `static_bearer`, " +
					"`xaa_federated`, `xaa_local`.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.OneOf(
						"none", "oauth2_user", "cc_federated", "custom_headers",
						"static_bearer", "xaa_federated", "xaa_local",
					),
				},
			},
			"auth_mode": schema.StringAttribute{
				MarkdownDescription: "Whether upstream credentials are agent-bound (`agent` — one tenant-shared " +
					"credential, no per-user binding) or user-bound (`user` — per-user OAuth with per-identity " +
					"credential binding). When omitted, the provider auto-sends `agent` for non-interactive " +
					"strategies (`static_bearer`, `custom_headers`, `cc_federated`) on the wire and the platform " +
					"infers otherwise; state stays null so omission round-trips. Set explicitly to override. " +
					"Allowed: `agent`, `user`.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf("agent", "user"),
				},
			},
			"bearer_token": schema.StringAttribute{
				MarkdownDescription: "Bearer token to forward to the upstream MCP when " +
					"`upstream_auth_strategy = \"static_bearer\"`. Sugar for `env = { BEARER_TOKEN = ... }` " +
					"— the common case where the upstream wants a single token in the `Authorization` " +
					"header. Mutually exclusive with `env`. Sensitive — redacted in logs and plan output.",
				Optional:  true,
				Sensitive: true,
			},
			"env": schema.MapAttribute{
				MarkdownDescription: "Plain-text upstream credentials. Values are string-only — these are " +
					"env-var assignments, not arbitrary structured data. Use `bearer_token` instead for the " +
					"common single-token case. Encrypted server-side at rest; sensitive — redacted in logs " +
					"and plan output.",
				Optional:    true,
				Sensitive:   true,
				ElementType: types.StringType,
			},
			"transport_type": schema.StringAttribute{
				MarkdownDescription: "MCP transport type. Allowed: `sse`, `stdio_tunnel`, `streamable_http`. " +
					"Defaults to the provider catalog entry.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.OneOf("sse", "stdio_tunnel", "streamable_http"),
				},
			},
			"enabled_scopes": schema.ListAttribute{
				MarkdownDescription: "Allowlist of MCP scopes this server is permitted to use. Restricts the " +
					"provider's full scope catalog to what this tenant's policies allow.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
			},
			"tags": schema.MapAttribute{
				MarkdownDescription: "Free-form key-value tags for organization and `for_each` filtering.",
				Optional:            true,
				Computed:            true,
				ElementType:         types.StringType,
			},

			// Client-credentials federation — only set these when
			// upstream_auth_strategy = "cc_federated".
			"cc_federated_workload_client_id": schema.StringAttribute{
				MarkdownDescription: "UUID of the `ferentin_workload_oauth_client` whose credentials the platform " +
					"uses to mint upstream tokens. Required when `upstream_auth_strategy = cc_federated`; " +
					"ignored otherwise.",
				Optional: true,
				Computed: true,
			},
			"cc_federated_audience_override": schema.StringAttribute{
				MarkdownDescription: "Per-server `audience` value sent at mint time. Narrows the workload OAuth " +
					"client's `default_audience` for just this server. Only meaningful with `cc_federated`.",
				Optional: true,
				Computed: true,
			},
			"cc_federated_resource_override": schema.StringAttribute{
				MarkdownDescription: "Per-server RFC 8707 `resource` value. Narrows the workload OAuth client's " +
					"`default_resource` for just this server. Only meaningful with `cc_federated`.",
				Optional: true,
				Computed: true,
			},
			"cc_federated_scopes_override": schema.StringAttribute{
				MarkdownDescription: "Per-server space-delimited scopes. Narrows the workload OAuth client's " +
					"`default_scopes` for just this server. Only meaningful with `cc_federated`.",
				Optional: true,
				Computed: true,
			},

			// Computed
			"server_id": schema.StringAttribute{
				MarkdownDescription: "Server-generated UUID.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "Server-derived URL slug.",
				Computed:            true,
			},
			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version (platform #649) for If-Match.",
				Computed:            true,
			},
			"available_for_routing": schema.BoolAttribute{
				MarkdownDescription: "Whether this server is eligible for routing right now (combines `enabled`, " +
					"health, and credentials-configured).",
				Computed: true,
			},
			"healthy": schema.BoolAttribute{
				MarkdownDescription: "Boolean health from the most recent check.",
				Computed:            true,
			},
			"health_status": schema.StringAttribute{
				MarkdownDescription: "Detailed health status string.",
				Computed:            true,
			},
			"credentials_configured": schema.BoolAttribute{
				MarkdownDescription: "True when the server has usable credentials configured (e.g., OAuth2 " +
					"registration completed or shared bearer set).",
				Computed: true,
			},
			"client_facing_url": schema.StringAttribute{
				MarkdownDescription: "URL clients connect to (often distinct from `endpoint` — the platform " +
					"fronts the upstream server).",
				Computed: true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp of creation.",
				Computed:            true,
			},
			"created_by": schema.StringAttribute{
				MarkdownDescription: "Principal that created the server.",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp of most recent update.",
				Computed:            true,
			},
			"updated_by": schema.StringAttribute{
				MarkdownDescription: "Principal that performed the most recent update.",
				Computed:            true,
			},
			"managed_by": schema.StringAttribute{
				MarkdownDescription: "Provenance label of the original creator (platform #651). Immutable.",
				Computed:            true,
			},
			"managed_by_client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth2 client_id of the original creator.",
				Computed:            true,
			},
			"managed_by_module": schema.StringAttribute{
				MarkdownDescription: "Module label from `X-Ferentin-Managed-By-Module`.",
				Computed:            true,
			},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent writer; drift signal when ≠ `managed_by`.",
				Computed:            true,
			},
		},
	}
}

func (r *MCPServerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MCPServerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(plan.TenantID)
	body, err := plan.toCreateBody(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Invalid MCP server config", err.Error())
		return
	}

	srv, err := r.sdk.MCPServers().Create(ctx, tenantID, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create MCP server", err)
		return
	}

	state := mcpServerToModel(tenantID, srv)
	// auth_mode and env aren't echoed by the response DTO — carry the
	// planned values forward into state so TF doesn't report
	// "inconsistent result after apply".
	state.AuthMode = plan.AuthMode
	state.BearerToken = plan.BearerToken
	state.Env = plan.Env
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *MCPServerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MCPServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	serverID := state.ServerID.ValueString()

	srv, err := r.sdk.MCPServers().Get(ctx, tenantID, serverID)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read MCP server", err)
		return
	}

	refreshed := mcpServerToModel(tenantID, srv)
	// auth_mode and env aren't echoed by the response DTO — preserve
	// prior state so they don't drift to null on every refresh.
	refreshed.AuthMode = state.AuthMode
	refreshed.BearerToken = state.BearerToken
	refreshed.Env = state.Env
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPServerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state MCPServerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	serverID := state.ServerID.ValueString()
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)

	body, err := plan.toUpdateBody(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Invalid MCP server config", err.Error())
		return
	}

	srv, err := r.sdk.MCPServers().Update(ctx, tenantID, serverID, version, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update MCP server", err)
		return
	}

	refreshed := mcpServerToModel(tenantID, srv)
	// auth_mode + bearer_token + env carry-forward — see Create handler.
	refreshed.AuthMode = plan.AuthMode
	refreshed.BearerToken = plan.BearerToken
	refreshed.Env = plan.Env
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPServerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MCPServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	serverID := state.ServerID.ValueString()
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)

	err := r.sdk.MCPServers().Delete(ctx, tenantID, serverID, version)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to delete MCP server", err)
	}
}

func (r *MCPServerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, serverID string
	switch len(parts) {
	case 1:
		tenantID = r.tenantID
		serverID = parts[0]
	case 2:
		tenantID = parts[0]
		serverID = parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError(
			"Cannot determine tenant for import",
			"Pass `<tenant_id>/<server_id>` or configure `tenant_id` on the provider block.",
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("server_id"), serverID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+serverID)...)
}

func (r *MCPServerResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// Below: model ↔ SDK conversion.
//
// Post-ferentin-platform#838: McpServerCreateRequest and McpServerUpdateRequest
// are now distinct types — Create has Name + ProviderId required, Update is a
// sparse all-pointer patch. Create and Update body builders are separate
// to reflect that.

func (m *MCPServerResourceModel) toCreateBody(ctx context.Context) (adminapi.MCPServerCreate, error) {
	body := adminapi.MCPServerCreate{
		Name: m.Name.ValueString(),
	}
	if !m.ProviderID.IsNull() && !m.ProviderID.IsUnknown() {
		pid, err := uuid.Parse(m.ProviderID.ValueString())
		if err != nil {
			return body, fmt.Errorf("provider_id %q is not a UUID: %w", m.ProviderID.ValueString(), err)
		}
		body.ProviderId = openapi_types.UUID(pid)
	}
	setStringPtr(m.DisplayName, &body.DisplayName)
	setStringPtr(m.Description, &body.Description)
	setStringPtr(m.Icon, &body.Icon)
	setStringPtr(m.Endpoint, &body.Endpoint)
	setStringPtr(m.HealthCheckURL, &body.HealthCheckUrl)
	setStringPtr(m.EdgeSiteID, &body.EdgeSiteId)
	setBoolPtr(m.Enabled, &body.Enabled)
	setInt32Ptr(m.Priority, &body.Priority)
	if !m.TransportType.IsNull() && !m.TransportType.IsUnknown() {
		v := gen.McpServerCreateRequestTransportType(m.TransportType.ValueString())
		body.TransportType = &v
	}
	if !m.UpstreamAuthStrategy.IsNull() && !m.UpstreamAuthStrategy.IsUnknown() {
		v := gen.McpServerCreateRequestUpstreamAuthStrategy(m.UpstreamAuthStrategy.ValueString())
		body.UpstreamAuthStrategy = &v
	}
	// auth_mode/strategy interlock (ferentin-platform mig 845): a
	// non-interactive strategy must pair with `agent`, never `user`. Take
	// the user's value if set; otherwise auto-pick `agent` for the three
	// non-interactive strategies so the demo doesn't need to know about
	// the invariant.
	if !m.AuthMode.IsNull() && !m.AuthMode.IsUnknown() {
		v := gen.McpServerCreateRequestAuthMode(m.AuthMode.ValueString())
		body.AuthMode = &v
	} else if !m.UpstreamAuthStrategy.IsNull() && !m.UpstreamAuthStrategy.IsUnknown() {
		switch m.UpstreamAuthStrategy.ValueString() {
		case "static_bearer", "custom_headers", "cc_federated":
			v := gen.McpServerCreateRequestAuthModeAgent
			body.AuthMode = &v
		}
	}
	if env := resolveEnv(ctx, m.BearerToken, m.Env); env != nil {
		body.Env = env
	}
	if !m.DeploymentMode.IsNull() && !m.DeploymentMode.IsUnknown() {
		v := gen.McpServerCreateRequestDeploymentMode(m.DeploymentMode.ValueString())
		body.DeploymentMode = &v
	}
	if !m.EnabledScopes.IsNull() && !m.EnabledScopes.IsUnknown() {
		var scopes []string
		_ = m.EnabledScopes.ElementsAs(ctx, &scopes, false)
		body.EnabledScopes = &scopes
	}
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		var tags map[string]string
		_ = m.Tags.ElementsAs(ctx, &tags, false)
		body.Tags = &tags
	}
	if !m.CcFederatedWorkloadClientID.IsNull() && !m.CcFederatedWorkloadClientID.IsUnknown() && m.CcFederatedWorkloadClientID.ValueString() != "" {
		id, err := uuid.Parse(m.CcFederatedWorkloadClientID.ValueString())
		if err != nil {
			return body, fmt.Errorf("cc_federated_workload_client_id %q is not a UUID: %w", m.CcFederatedWorkloadClientID.ValueString(), err)
		}
		u := openapi_types.UUID(id)
		body.CcFederatedWorkloadClientId = &u
	}
	setStringPtr(m.CcFederatedAudienceOverride, &body.CcFederatedAudienceOverride)
	setStringPtr(m.CcFederatedResourceOverride, &body.CcFederatedResourceOverride)
	setStringPtr(m.CcFederatedScopesOverride, &body.CcFederatedScopesOverride)
	return body, nil
}

func (m *MCPServerResourceModel) toUpdateBody(ctx context.Context) (adminapi.MCPServerUpdate, error) {
	body := adminapi.MCPServerUpdate{}
	setStringPtr(m.Name, &body.Name)
	setStringPtr(m.DisplayName, &body.DisplayName)
	setStringPtr(m.Description, &body.Description)
	setStringPtr(m.Icon, &body.Icon)
	setStringPtr(m.Endpoint, &body.Endpoint)
	setStringPtr(m.HealthCheckURL, &body.HealthCheckUrl)
	setStringPtr(m.EdgeSiteID, &body.EdgeSiteId)
	setBoolPtr(m.Enabled, &body.Enabled)
	setInt32Ptr(m.Priority, &body.Priority)
	if !m.TransportType.IsNull() && !m.TransportType.IsUnknown() {
		v := gen.McpServerUpdateRequestTransportType(m.TransportType.ValueString())
		body.TransportType = &v
	}
	if !m.UpstreamAuthStrategy.IsNull() && !m.UpstreamAuthStrategy.IsUnknown() {
		v := gen.McpServerUpdateRequestUpstreamAuthStrategy(m.UpstreamAuthStrategy.ValueString())
		body.UpstreamAuthStrategy = &v
	}
	if !m.AuthMode.IsNull() && !m.AuthMode.IsUnknown() {
		v := gen.McpServerUpdateRequestAuthMode(m.AuthMode.ValueString())
		body.AuthMode = &v
	} else if !m.UpstreamAuthStrategy.IsNull() && !m.UpstreamAuthStrategy.IsUnknown() {
		// Mirror toCreateBody — same auth_mode/strategy interlock.
		switch m.UpstreamAuthStrategy.ValueString() {
		case "static_bearer", "custom_headers", "cc_federated":
			v := gen.McpServerUpdateRequestAuthModeAgent
			body.AuthMode = &v
		}
	}
	if env := resolveEnv(ctx, m.BearerToken, m.Env); env != nil {
		body.Env = env
	}
	if !m.DeploymentMode.IsNull() && !m.DeploymentMode.IsUnknown() {
		v := gen.McpServerUpdateRequestDeploymentMode(m.DeploymentMode.ValueString())
		body.DeploymentMode = &v
	}
	if !m.EnabledScopes.IsNull() && !m.EnabledScopes.IsUnknown() {
		var scopes []string
		_ = m.EnabledScopes.ElementsAs(ctx, &scopes, false)
		body.EnabledScopes = &scopes
	}
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		var tags map[string]string
		_ = m.Tags.ElementsAs(ctx, &tags, false)
		body.Tags = &tags
	}
	if !m.CcFederatedWorkloadClientID.IsNull() && !m.CcFederatedWorkloadClientID.IsUnknown() && m.CcFederatedWorkloadClientID.ValueString() != "" {
		id, err := uuid.Parse(m.CcFederatedWorkloadClientID.ValueString())
		if err != nil {
			return body, fmt.Errorf("cc_federated_workload_client_id %q is not a UUID: %w", m.CcFederatedWorkloadClientID.ValueString(), err)
		}
		u := openapi_types.UUID(id)
		body.CcFederatedWorkloadClientId = &u
	}
	setStringPtr(m.CcFederatedAudienceOverride, &body.CcFederatedAudienceOverride)
	setStringPtr(m.CcFederatedResourceOverride, &body.CcFederatedResourceOverride)
	setStringPtr(m.CcFederatedScopesOverride, &body.CcFederatedScopesOverride)
	return body, nil
}

// mcpServerToModel maps the SDK response (gen.McpServerResponseDto) into
// Terraform state.
func mcpServerToModel(tenantID string, srv *adminapi.MCPServer) MCPServerResourceModel {
	m := MCPServerResourceModel{
		TenantID: types.StringValue(tenantID),
	}
	if srv.Id != nil {
		m.ServerID = types.StringValue(srv.Id.String())
	} else {
		m.ServerID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.ServerID.ValueString())

	if srv.ProviderId != nil {
		m.ProviderID = types.StringValue(srv.ProviderId.String())
	} else {
		m.ProviderID = types.StringNull()
	}

	m.Version = int64PtrToTF(srv.Version)
	m.Name = strPtrToTF(srv.Name)
	m.Endpoint = strPtrToTF(srv.Endpoint)
	m.DisplayName = strPtrToTF(srv.DisplayName)
	m.Description = strPtrToTF(srv.Description)
	m.Icon = strPtrToTF(srv.Icon)
	m.Enabled = boolPtrOrDefault(srv.Enabled)
	m.Priority = int32PtrToTF(srv.Priority)
	m.HealthCheckURL = strPtrToTF(srv.HealthCheckUrl)
	m.EdgeSiteID = strPtrToTF(srv.EdgeSiteId)
	m.DeploymentMode = enumPtrToTF(srv.DeploymentMode)
	m.ProviderAuthType = enumPtrToTF(srv.ProviderAuthType)
	m.UpstreamAuthStrategy = enumPtrToTF(srv.UpstreamAuthStrategy)
	m.TransportType = strPtrToTF(srv.TransportType)
	// auth_mode and env aren't echoed by the response DTO. Create / Read
	// / Update carry the plan or prior-state value forward into state
	// (see the handlers above); mapper leaves both untouched here.

	m.Slug = strPtrToTF(srv.Slug)
	m.AvailableForRouting = boolPtrOrDefault(srv.AvailableForRouting)
	m.Healthy = boolPtrOrDefault(srv.Healthy)
	m.HealthStatus = strPtrToTF(srv.HealthStatus)
	m.CredentialsConfigured = boolPtrOrDefault(srv.CredentialsConfigured)
	m.ClientFacingURL = strPtrToTF(srv.ClientFacingUrl)
	m.CreatedAt = timePtrToTF(srv.CreatedAt)
	m.CreatedBy = strPtrToTF(srv.CreatedBy)
	m.UpdatedAt = timePtrToTF(srv.UpdatedAt)
	m.UpdatedBy = strPtrToTF(srv.UpdatedBy)
	m.ManagedBy = enumPtrToTF(srv.ManagedBy)
	m.ManagedByClientID = strPtrToTF(srv.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(srv.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(srv.LastModifiedBy)

	// enabled_scopes
	if srv.EnabledScopes != nil {
		elems := make([]attr.Value, 0, len(*srv.EnabledScopes))
		for _, s := range *srv.EnabledScopes {
			elems = append(elems, types.StringValue(s))
		}
		lv, _ := types.ListValue(types.StringType, elems)
		m.EnabledScopes = lv
	} else {
		m.EnabledScopes = types.ListNull(types.StringType)
	}

	// tags
	if srv.Tags != nil {
		elems := make(map[string]attr.Value, len(*srv.Tags))
		for k, v := range *srv.Tags {
			elems[k] = types.StringValue(v)
		}
		mv, _ := types.MapValue(types.StringType, elems)
		m.Tags = mv
	} else {
		m.Tags = types.MapNull(types.StringType)
	}

	// cc_federated_* fields. The FK is only populated when the platform's
	// upstream_auth_strategy is cc_federated; otherwise these read as null.
	if srv.CcFederatedWorkloadClientId != nil {
		m.CcFederatedWorkloadClientID = types.StringValue(srv.CcFederatedWorkloadClientId.String())
	} else {
		m.CcFederatedWorkloadClientID = types.StringNull()
	}
	m.CcFederatedAudienceOverride = strPtrToTF(srv.CcFederatedAudienceOverride)
	m.CcFederatedResourceOverride = strPtrToTF(srv.CcFederatedResourceOverride)
	m.CcFederatedScopesOverride = strPtrToTF(srv.CcFederatedScopesOverride)

	return m
}

// Suppress unused imports warning for openapi_types — only needed by callers
// that build a UUID directly. Keep present for future use.
var _ = openapi_types.UUID{}
