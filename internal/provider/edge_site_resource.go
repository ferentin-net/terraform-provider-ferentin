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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// EdgeSiteResource is the `ferentin_edge_site` Terraform resource — the
// reference implementation for Phase 2. See §6.2 of the design doc for
// the user-facing model: site_id is a user-supplied slug (3-50 chars),
// the platform mints a synthetic UUID on create, and the resource ID
// composite is `{tenant_id}/{site_id}` for portability across workspaces.
type EdgeSiteResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

// EdgeSiteResourceModel is the Terraform-state shape for a single edge
// site. Field tags match the schema attribute names; pointer-like Terraform
// types (types.String, types.Bool, …) allow distinguishing Unknown / Null
// / Set states.
type EdgeSiteResourceModel struct {
	// Identity
	ID       types.String `tfsdk:"id"` // composite "tenant/site"
	TenantID types.String `tfsdk:"tenant_id"`
	SiteID   types.String `tfsdk:"site_id"`

	// Required / user-supplied
	SiteName    types.String `tfsdk:"site_name"`
	Description types.String `tfsdk:"description"`

	// Optional / user-supplied with platform default
	Status            types.String `tfsdk:"status"`
	ContactEmail      types.String `tfsdk:"contact_email"`
	TimeZone          types.String `tfsdk:"time_zone"`
	MaintenanceWindow types.String `tfsdk:"maintenance_window"`
	MaxEdgeDevices    types.Int64  `tfsdk:"max_edge_devices"`
	AllowHTTPUpstream types.Bool   `tfsdk:"allow_http_upstream"`
	VerifyUpstreamTLS types.Bool   `tfsdk:"verify_upstream_tls"`
	BundleCloudMCP    types.Bool   `tfsdk:"bundle_cloud_mcp"`
	LLMEnabled        types.Bool   `tfsdk:"llm_enabled"`
	MCPEnabled        types.Bool   `tfsdk:"mcp_enabled"`
	MonitoringEnabled types.Bool   `tfsdk:"monitoring_enabled"`
	McpGatewayURL     types.String `tfsdk:"mcp_gateway_url"`
	TunnelURL         types.String `tfsdk:"tunnel_url"`
	Tags              types.Map    `tfsdk:"tags"`

	// Computed / server-set
	Version           types.Int64  `tfsdk:"version"` // for If-Match
	SyntheticID       types.String `tfsdk:"synthetic_id"`
	CurrentDevices    types.Int64  `tfsdk:"current_devices"`
	CreatedAt         types.String `tfsdk:"created_at"`
	UpdatedAt         types.String `tfsdk:"updated_at"`
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

// NewEdgeSiteResource is the framework-compatible constructor.
func NewEdgeSiteResource() resource.Resource {
	return &EdgeSiteResource{}
}

// Compile-time interface assertions.
var (
	_ resource.Resource                = (*EdgeSiteResource)(nil)
	_ resource.ResourceWithConfigure   = (*EdgeSiteResource)(nil)
	_ resource.ResourceWithImportState = (*EdgeSiteResource)(nil)
)

func (r *EdgeSiteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_edge_site"
}

func (r *EdgeSiteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		// Configure is called twice (once with nil ProviderData) on first apply.
		return
	}
	pd, ok := req.ProviderData.(ProviderData)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected ProviderData, got %T. This is a provider bug; please file an issue.", req.ProviderData),
		)
		return
	}
	r.sdk = pd.SDK
	r.tenantID = pd.TenantID
}

func (r *EdgeSiteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Ferentin edge site — a logical region/datacenter where service-edge " +
			"instances enroll and serve LLM / MCP traffic. The `site_id` attribute is a user-supplied slug " +
			"that becomes the resource's primary identifier; the platform also assigns a synthetic UUID " +
			"available as `synthetic_id`.\n\n" +
			"## Import\n\n" +
			"Existing edge sites can be imported using `<tenant_id>/<site_id>` " +
			"(or `<site_id>` alone when the provider's default `tenant_id` matches):\n\n" +
			"```\n" +
			"terraform import ferentin_edge_site.example <tenant_id>/<site_id>\n" +
			"```",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Composite Terraform resource ID `<tenant_id>/<site_id>` for portable " +
					"`terraform import`.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Tenant UUID this site belongs to. Defaults to the provider-level " +
					"`tenant_id`; override here to manage multiple tenants from one config.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"site_id": schema.StringAttribute{
				MarkdownDescription: "User-supplied slug for the site (3-50 chars, e.g. `prod-us-east-1a`). " +
					"Immutable post-create; renames require destroy + recreate.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"site_name": schema.StringAttribute{
				MarkdownDescription: "Human-readable name for the site.",
				Required:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Optional description.",
				Optional:            true,
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Site runtime status. Allowed: `active`, `inactive`, `maintenance`.",
				Optional:            true,
				Computed:            true,
			},
			"contact_email": schema.StringAttribute{
				MarkdownDescription: "Contact email for site operations.",
				Optional:            true,
				Computed:            true,
			},
			"time_zone": schema.StringAttribute{
				MarkdownDescription: "IANA time zone name.",
				Optional:            true,
				Computed:            true,
			},
			"maintenance_window": schema.StringAttribute{
				MarkdownDescription: "Scheduled maintenance window (free-form, e.g. cron expression).",
				Optional:            true,
				Computed:            true,
			},
			"max_edge_devices": schema.Int64Attribute{
				MarkdownDescription: "Maximum number of edge devices allowed at this site.",
				Optional:            true,
				Computed:            true,
			},
			"allow_http_upstream": schema.BoolAttribute{
				MarkdownDescription: "When true, service-edge accepts `http://` (cleartext) for customer LLM / MCP " +
					"upstream URLs. Default `false` (https-only). See platform #662.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"verify_upstream_tls": schema.BoolAttribute{
				MarkdownDescription: "When false, service-edge skips TLS verification on customer upstream calls. " +
					"Default `true` (strict). See platform #662.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"bundle_cloud_mcp": schema.BoolAttribute{
				MarkdownDescription: "When `true` (default), cloud-routed MCP provider instances and their credentials " +
					"flow into this site's policy bundle. `false` = least-privilege; only edge-routed servers appear. " +
					"See platform #723.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"llm_enabled": schema.BoolAttribute{
				MarkdownDescription: "When `true` (default), edges at this site receive a non-empty LLM bundle section. " +
					"Tenant-level `features.llm` continues to gate at the tenant level.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"mcp_enabled": schema.BoolAttribute{
				MarkdownDescription: "When `true` (default), edges at this site receive a non-empty MCP bundle section. " +
					"Tenant-level `features.mcp` continues to gate at the tenant level.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"monitoring_enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether monitoring is enabled for this site. Default `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"mcp_gateway_url": schema.StringAttribute{
				MarkdownDescription: "Customer-hosted L7 proxy URL fronting this site's service-edge replicas. " +
					"Used to compute copyable MCP URLs for edge-routed provider instances.",
				Optional: true,
				Computed: true,
			},
			"tunnel_url": schema.StringAttribute{
				MarkdownDescription: "WebSocket URL for tunnel-agent connections. Overrides the global default.",
				Optional:            true,
				Computed:            true,
			},
			"tags": schema.MapAttribute{
				MarkdownDescription: "Free-form key/value tags for organizing and filtering sites " +
					"(e.g. `{ tier = \"primary\", team = \"platform\" }`). Not interpreted by the " +
					"platform — routing and bundling are driven by the typed attributes above.\n\n" +
					"~> Like every optional attribute on this resource, dropping the block leaves the " +
					"server-side value untouched rather than clearing it. To remove tags, set " +
					"`tags = {}` explicitly.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
			},

			// Computed / server-set
			"synthetic_id": schema.StringAttribute{
				MarkdownDescription: "Server-generated UUID. Distinct from `site_id` (the user slug).",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version (platform #649). The SDK threads this as a " +
					"weak ETag on update/delete so concurrent writers fail fast with 412 rather than silently " +
					"overwriting.",
				Computed: true,
			},
			"current_devices": schema.Int64Attribute{
				MarkdownDescription: "Number of edge devices currently enrolled. Runtime statistic.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp of creation.",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp of most recent update.",
				Computed:            true,
			},
			"managed_by": schema.StringAttribute{
				MarkdownDescription: "Provenance label of the original creator (per platform #651). Immutable.",
				Computed:            true,
			},
			"managed_by_client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth2 client_id of the original creator. Null when the creator was a user " +
					"via auth-code grant.",
				Computed: true,
			},
			"managed_by_module": schema.StringAttribute{
				MarkdownDescription: "Module label the creator passed via `X-Ferentin-Managed-By-Module`.",
				Computed:            true,
			},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance label of the most recent writer. Diverges from `managed_by` when " +
					"an out-of-band writer (console, CLI) edits the resource — the drift signal per platform #651.",
				Computed: true,
			},
		},
	}
}

func (r *EdgeSiteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan EdgeSiteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(plan.TenantID)

	create := adminapi.EdgeSiteCreate{
		SiteId:   plan.SiteID.ValueString(),
		SiteName: plan.SiteName.ValueString(),
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		v := plan.Description.ValueString()
		create.Description = &v
	}
	if !plan.ContactEmail.IsNull() && !plan.ContactEmail.IsUnknown() {
		v := plan.ContactEmail.ValueString()
		e := openapi_types.Email(v)
		create.ContactEmail = &e
	}
	if !plan.TimeZone.IsNull() && !plan.TimeZone.IsUnknown() {
		v := plan.TimeZone.ValueString()
		create.TimeZone = &v
	}
	if !plan.MaintenanceWindow.IsNull() && !plan.MaintenanceWindow.IsUnknown() {
		v := plan.MaintenanceWindow.ValueString()
		create.MaintenanceWindow = &v
	}
	if !plan.MaxEdgeDevices.IsNull() && !plan.MaxEdgeDevices.IsUnknown() {
		v := int32(plan.MaxEdgeDevices.ValueInt64())
		create.MaxEdgeDevices = &v
	}
	setBoolPtr(plan.AllowHTTPUpstream, &create.AllowHttpUpstream)
	setBoolPtr(plan.VerifyUpstreamTLS, &create.VerifyUpstreamTls)
	setBoolPtr(plan.BundleCloudMCP, &create.BundleCloudMcp)
	setBoolPtr(plan.LLMEnabled, &create.LlmEnabled)
	setBoolPtr(plan.MCPEnabled, &create.McpEnabled)
	setBoolPtr(plan.MonitoringEnabled, &create.MonitoringEnabled)
	if !plan.McpGatewayURL.IsNull() && !plan.McpGatewayURL.IsUnknown() {
		v := plan.McpGatewayURL.ValueString()
		create.McpGatewayUrl = &v
	}
	if !plan.TunnelURL.IsNull() && !plan.TunnelURL.IsUnknown() {
		v := plan.TunnelURL.ValueString()
		create.TunnelUrl = &v
	}
	create.Tags = stringMapToSDK(ctx, plan.Tags)

	site, err := r.sdk.EdgeSites().Create(ctx, tenantID, create)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create edge site", err)
		return
	}

	state := edgeSiteToModel(tenantID, site)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *EdgeSiteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state EdgeSiteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	siteID := state.SiteID.ValueString()

	site, err := r.sdk.EdgeSites().Get(ctx, tenantID, siteID)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			// Standard Terraform read-on-404: drop the resource from state.
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read edge site", err)
		return
	}

	refreshed := edgeSiteToModel(tenantID, site)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *EdgeSiteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state EdgeSiteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	siteID := state.SiteID.ValueString()
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)

	update := adminapi.EdgeSiteUpdate{}
	if !plan.SiteName.IsNull() && !plan.SiteName.IsUnknown() {
		v := plan.SiteName.ValueString()
		update.SiteName = &v
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		v := plan.Description.ValueString()
		update.Description = &v
	}
	if !plan.ContactEmail.IsNull() && !plan.ContactEmail.IsUnknown() {
		v := plan.ContactEmail.ValueString()
		e := openapi_types.Email(v)
		update.ContactEmail = &e
	}
	if !plan.TimeZone.IsNull() && !plan.TimeZone.IsUnknown() {
		v := plan.TimeZone.ValueString()
		update.TimeZone = &v
	}
	if !plan.MaintenanceWindow.IsNull() && !plan.MaintenanceWindow.IsUnknown() {
		v := plan.MaintenanceWindow.ValueString()
		update.MaintenanceWindow = &v
	}
	if !plan.MaxEdgeDevices.IsNull() && !plan.MaxEdgeDevices.IsUnknown() {
		v := int32(plan.MaxEdgeDevices.ValueInt64())
		update.MaxEdgeDevices = &v
	}
	setBoolPtr(plan.AllowHTTPUpstream, &update.AllowHttpUpstream)
	setBoolPtr(plan.VerifyUpstreamTLS, &update.VerifyUpstreamTls)
	setBoolPtr(plan.BundleCloudMCP, &update.BundleCloudMcp)
	setBoolPtr(plan.LLMEnabled, &update.LlmEnabled)
	setBoolPtr(plan.MCPEnabled, &update.McpEnabled)
	setBoolPtr(plan.MonitoringEnabled, &update.MonitoringEnabled)
	if !plan.McpGatewayURL.IsNull() && !plan.McpGatewayURL.IsUnknown() {
		v := plan.McpGatewayURL.ValueString()
		update.McpGatewayUrl = &v
	}
	if !plan.TunnelURL.IsNull() && !plan.TunnelURL.IsUnknown() {
		v := plan.TunnelURL.ValueString()
		update.TunnelUrl = &v
	}
	update.Tags = stringMapToSDK(ctx, plan.Tags)

	site, err := r.sdk.EdgeSites().Update(ctx, tenantID, siteID, version, update)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update edge site", err)
		return
	}

	refreshed := edgeSiteToModel(tenantID, site)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *EdgeSiteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state EdgeSiteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	siteID := state.SiteID.ValueString()
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)

	err := r.sdk.EdgeSites().Delete(ctx, tenantID, siteID, version)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			// Already deleted out of band — fine.
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to delete edge site", err)
	}
}

// ImportState accepts either:
//   - "<tenant_id>/<site_id>" (preferred — portable across workspaces)
//   - "<site_id>" (falls back to the provider-level tenant_id)
func (r *EdgeSiteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, siteID string
	switch len(parts) {
	case 1:
		tenantID = r.tenantID
		siteID = parts[0]
	case 2:
		tenantID = parts[0]
		siteID = parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError(
			"Cannot determine tenant for import",
			"Pass `<tenant_id>/<site_id>` or configure `tenant_id` on the provider block.",
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("site_id"), siteID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+siteID)...)
	// Read() runs next and populates the rest.
}

// resolveTenant returns the per-resource tenant_id if set, falling back to
// the provider-level default. Empty string surfaces as an error in the
// platform call; we don't add a friendly diag here because Configure already
// errored out if both are missing.
func (r *EdgeSiteResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// edgeSiteToModel maps the SDK's EdgeSite response into Terraform-state
// shape. Distinguish:
//
//   - `site.Id` is the SYNTHETIC UUID the platform mints
//   - `site.SiteId` is the user-supplied slug (the primary user-facing handle)
//
// The Terraform resource ID is `<tenant>/<site_id>` for portable imports;
// `synthetic_id` exposes the UUID as a computed attribute.
func edgeSiteToModel(tenantID string, site *adminapi.EdgeSite) EdgeSiteResourceModel {
	m := EdgeSiteResourceModel{
		TenantID:    types.StringValue(tenantID),
		SiteID:      strPtrToTF(site.SiteId),
		SyntheticID: strPtrToTF(site.Id),
	}
	m.ID = types.StringValue(tenantID + "/" + m.SiteID.ValueString())

	m.Version = int64PtrToTF(site.Version)
	m.SiteName = strPtrToTF(site.SiteName)
	m.Description = strPtrToTF(site.Description)
	m.Status = enumPtrToTF(site.Status)
	m.ContactEmail = strPtrToTF(site.ContactEmail)
	m.TimeZone = strPtrToTF(site.TimeZone)
	m.MaintenanceWindow = strPtrToTF(site.MaintenanceWindow)
	m.MaxEdgeDevices = int32PtrToTF(site.MaxEdgeDevices)
	m.AllowHTTPUpstream = boolPtrOrDefault(site.AllowHttpUpstream)
	m.VerifyUpstreamTLS = boolPtrOrDefault(site.VerifyUpstreamTls)
	m.BundleCloudMCP = boolPtrOrDefault(site.BundleCloudMcp)
	m.LLMEnabled = boolPtrOrDefault(site.LlmEnabled)
	m.MCPEnabled = boolPtrOrDefault(site.McpEnabled)
	m.MonitoringEnabled = boolPtrOrDefault(site.MonitoringEnabled)
	m.McpGatewayURL = strPtrToTF(site.McpGatewayUrl)
	m.TunnelURL = strPtrToTF(site.TunnelUrl)
	// A site with no tags comes back as absent, not `{}` — map to a null map so
	// a config that never mentions tags stays a no-op plan.
	m.Tags = stringMapToTF(site.Tags)
	m.CurrentDevices = int32PtrToTF(site.CurrentDevices)
	m.CreatedAt = timePtrToTF(site.CreatedAt)
	m.UpdatedAt = timePtrToTF(site.UpdatedAt)
	m.ManagedBy = enumPtrToTF(site.ManagedBy)
	m.ManagedByClientID = strPtrToTF(site.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(site.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(site.LastModifiedBy)
	return m
}
