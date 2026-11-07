package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
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

// Route target kinds (McpRouteService.KINDS). host and url_prefix select a
// remote server, which the device rewrites to the gateway; package and
// argv_contains select a local one, which it wraps in ferentin-mcp.
var (
	mcpRouteRemoteKinds = []string{"host", "url_prefix"}
	mcpRouteAllKinds    = []string{"host", "url_prefix", "package", "argv_contains"}
)

const (
	// McpRouteService limits: values and app ids are bounded by the steer
	// validation a route shares with endpoint MCP rules, device groups by
	// MAX_DEVICE_GROUP_IDS.
	mcpRouteMaxValues         = 64
	mcpRouteMaxAppBundleIDs   = 64
	mcpRouteMaxDeviceGroupIDs = 64
	mcpRouteMaxName           = 255
	mcpRouteMaxDescription    = 2000
)

// mcpRouteNameShape refuses a name with leading or trailing whitespace:
// McpRouteService stores name.trim(), which would differ from the config.
var mcpRouteNameShape = regexp.MustCompile(`^\S(.*\S)?$`)

// MCPRouteResource is the `ferentin_mcp_route` Terraform resource: an MCP
// route on managed devices (ferentin-platform#3256 item 6, ADR 0029 decision 7).
//
// In the unified MCP policy model a route is what an endpoint MCP rule's
// `steer` became. Allow and deny moved onto `ferentin_mcp_policy` as device
// targets; a route only says how a selected server is reached.
//
// Two platform behaviours shape the model (McpRouteService):
//
//   - The PUT is a full replace. Every write field is restated, and an omitted
//     `enabled` means true, not "keep" — so `enabled` carries a Default.
//     description, steer_to_url, app_bundle_ids and device_group_ids are
//     Optional, not Computed: removing one from config must plan a change.
//   - target.values are canonicalised on write, as a steer rule's are. State
//     keeps the configured spelling in target.values and the server's in
//     stored_target_values; see reconcileRouteValues.
type MCPRouteResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type MCPRouteResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Enabled     types.Bool   `tfsdk:"enabled"`

	Target             *MCPRouteTargetModel `tfsdk:"target"`
	StoredTargetValues types.List           `tfsdk:"stored_target_values"`
	Mechanism          types.String         `tfsdk:"mechanism"`

	SteerToURL     types.String `tfsdk:"steer_to_url"`
	AppBundleIDs   types.List   `tfsdk:"app_bundle_ids"`
	DeviceGroupIDs types.List   `tfsdk:"device_group_ids"`

	RouteID   types.String `tfsdk:"route_id"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`

	Version        types.Int64  `tfsdk:"version"` // for If-Match
	ManagedBy      types.String `tfsdk:"managed_by"`
	LastModifiedBy types.String `tfsdk:"last_modified_by"`
}

// MCPRouteTargetModel mirrors gen.McpRouteTarget.
type MCPRouteTargetModel struct {
	Kind   types.String `tfsdk:"kind"`
	Values types.List   `tfsdk:"values"`
}

func NewMCPRouteResource() resource.Resource { return &MCPRouteResource{} }

var (
	_ resource.Resource                   = (*MCPRouteResource)(nil)
	_ resource.ResourceWithConfigure      = (*MCPRouteResource)(nil)
	_ resource.ResourceWithImportState    = (*MCPRouteResource)(nil)
	_ resource.ResourceWithValidateConfig = (*MCPRouteResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*MCPRouteResource)(nil)
)

func (r *MCPRouteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_route"
}

func (r *MCPRouteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *MCPRouteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "MCP route — sends an MCP server that a managed device starts through " +
			"Ferentin.\n\n" +
			"* A **remote** target (`host`, `url_prefix`) is rewritten to the gateway " +
			"(`mechanism` `rewrite_to_gateway`): to `steer_to_url`, or the scope's own gateway when it " +
			"is unset.\n" +
			"* A **local** target (`package`, `argv_contains`) is wrapped in `ferentin-mcp` " +
			"(`mechanism` `shim_wrap`), so its tool calls pass through Ferentin.\n\n" +
			"In the unified MCP policy model a route is what an endpoint MCP rule's `steer` action " +
			"became. Allowing or denying a server is not a route's job: that is `ferentin_mcp_policy`, " +
			"with device `targets`. A route never grants.\n\n" +
			"The platform refuses a route that sends a server somewhere other than where an enabled " +
			"route already sends it, in an overlapping scope and app (`route_conflict`), and a " +
			"`steer_to_url` that is not this tenant's MCP gateway or one of its edge sites. Changes " +
			"reach devices on the tenant's next activation.\n\n" +
			"## Import\n\n" +
			"```\n" +
			"terraform import ferentin_mcp_route.example <tenant_id>/<route_id>\n" +
			"```\n\n" +
			"An import has no configured spelling of `target.values`, so it takes the platform's " +
			"canonical form. Where that differs from your config's spelling, the first plan after " +
			"import shows `target.values` changing to it; applying it changes nothing on the platform.",

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
				MarkdownDescription: "Route name, unique within the tenant. 1-255 characters, with no " +
					"leading or trailing whitespace (the platform trims it).",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, mcpRouteMaxName),
					stringvalidator.RegexMatches(mcpRouteNameShape,
						"must not start or end with whitespace: the platform trims it, and the stored name would differ from the config"),
				},
			},
			// description, steer_to_url, app_bundle_ids and device_group_ids are
			// Optional, NOT Computed: the PUT replaces them, so removing one from
			// config must plan a change. mcpRouteToModel reads the platform's
			// empty forms as null.
			"description": schema.StringAttribute{
				Optional:   true,
				Validators: []validator.String{stringvalidator.LengthAtMost(mcpRouteMaxDescription)},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether devices apply the route. Default `true`.",
				// A Default: the PUT reads an omitted enabled as true, so state must
				// say true when config says nothing.
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(true),
			},

			"target": schema.SingleNestedAttribute{
				MarkdownDescription: "What the route selects on a device.",
				Required:            true,
				Attributes: map[string]schema.Attribute{
					"kind": schema.StringAttribute{
						MarkdownDescription: "`host` or `url_prefix` (a remote server's address), or " +
							"`package` or `argv_contains` (a local server's command line).",
						Required:   true,
						Validators: []validator.String{stringvalidator.OneOf(mcpRouteAllKinds...)},
					},
					"values": schema.ListAttribute{
						MarkdownDescription: "Values to match, interpreted per `kind`. 1-64 entries. " +
							"`host` values are bare hostnames (no scheme, port or path); `package` values " +
							"are one registry-qualified id each.\n\n" +
							"The platform stores them in the canonical spelling a steer rule's values " +
							"take. Terraform keeps your spelling here and the platform's in " +
							"`stored_target_values`, so canonicalisation does not show as drift.",
						Required:    true,
						ElementType: types.StringType,
						Validators: []validator.List{
							listvalidator.SizeBetween(1, mcpRouteMaxValues),
							listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
						},
					},
				},
			},
			"stored_target_values": schema.ListAttribute{
				MarkdownDescription: "Read-only. `target.values` as the platform stores them, after " +
					"canonicalisation. A console edit shows up as a change here and in `target.values` " +
					"on the next plan.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"mechanism": schema.StringAttribute{
				MarkdownDescription: "Read-only. `rewrite_to_gateway` for a remote target, `shim_wrap` " +
					"for a local one.",
				Computed: true,
			},

			"steer_to_url": schema.StringAttribute{
				MarkdownDescription: "Remote targets only: the gateway route to rewrite the server to, " +
					"for example `https://<tenant-short-name>.mcp.ferentin.net/v1/<route>`. It must be this " +
					"tenant's MCP gateway or one of its edge sites, and `https://`. Unset: the scope's own " +
					"gateway. Refused on a `package` or `argv_contains` target, which is wrapped in " +
					"`ferentin-mcp` and has no URL.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(httpsOnlyURL,
						"must be an https:// URL — http:// would downgrade routed MCP sessions to cleartext"),
				},
			},
			"app_bundle_ids": schema.ListAttribute{
				MarkdownDescription: "Narrow the route to the config files of these MCP **clients** " +
					"(Claude Desktop, Cursor, …) by macOS bundle identifier, e.g. " +
					"`com.anthropic.claudefordesktop`. Empty or unset = every app. At most 64.",
				Optional: true, ElementType: types.StringType,
				Validators: []validator.List{listvalidator.SizeAtMost(mcpRouteMaxAppBundleIDs)},
			},
			"device_group_ids": schema.ListAttribute{
				MarkdownDescription: "Device groups the route applies to — reference " +
					"`ferentin_device_group.<name>.group_id`. Empty or unset = **every device in the " +
					"tenant**. At most 64.",
				Optional: true, ElementType: types.StringType,
				Validators: []validator.List{listvalidator.SizeAtMost(mcpRouteMaxDeviceGroupIDs)},
			},

			"route_id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{Computed: true},

			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version, sent as `If-Match` on update and " +
					"delete so a concurrent console edit is rejected with 412 rather than overwritten.",
				Computed: true,
			},
			"managed_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the original creator (`iac` for this provider). " +
					"Immutable after create.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent writer. Divergence from `managed_by` " +
					"is the drift signal.",
				Computed: true,
			},
		},
	}
}

// ValidateConfig moves McpRouteService's and the shared steer validation's
// cheap checks to plan time. The checks that need the database (route_conflict,
// the steer URL being this tenant's gateway, device groups belonging to the
// tenant) stay server-side. Unknown values are skipped.
func (r *MCPRouteResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var kind, steerToURL types.String
	var values types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("target").AtName("kind"), &kind)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("target").AtName("values"), &values)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("steer_to_url"), &steerToURL)...)
	if resp.Diagnostics.HasError() || kind.IsUnknown() || kind.IsNull() {
		return
	}
	k := kind.ValueString()

	if !steerToURL.IsUnknown() && !isUnsetString(steerToURL) && !slices.Contains(mcpRouteRemoteKinds, k) {
		resp.Diagnostics.AddAttributeError(path.Root("steer_to_url"),
			fmt.Sprintf("steer_to_url is remote-only; omit it on a %q route", k),
			"Only host and url_prefix routes rewrite to a gateway URL. A local route wraps the entry "+
				"in ferentin-mcp and has no URL to name.")
	}

	// A host value is a bare hostname: the steer validation refuses a scheme,
	// port, path, query or userinfo with 400, by which time other resources in
	// the run may already exist.
	if k == "host" && !values.IsUnknown() && !values.IsNull() {
		for _, v := range values.Elements() {
			s, ok := v.(types.String)
			if !ok || s.IsUnknown() || s.IsNull() {
				continue
			}
			if strings.ContainsAny(s.ValueString(), "/?#@:") {
				resp.Diagnostics.AddAttributeError(path.Root("target").AtName("values"),
					fmt.Sprintf("%q is not a bare hostname", s.ValueString()),
					"A host value has no scheme, port, path, query or userinfo. Use kind "+
						"\"url_prefix\" to match a URL.")
			}
		}
	}
}

func (r *MCPRouteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MCPRouteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	body, invalidGroups := plan.toWrite(ctx)
	if len(invalidGroups) > 0 {
		addInvalidGroupDiag(&resp.Diagnostics, invalidGroups)
		return
	}
	route, err := r.sdk.MCPRoutes().Create(ctx, tenantID, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create MCP route", err)
		return
	}
	state := mcpRouteToModel(tenantID, route)
	// target.values is Required, so state must hold the configured spelling;
	// the platform's canonical form lives in stored_target_values.
	state.keepConfiguredValues(plan)
	state.keepEmptyForms(plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *MCPRouteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MCPRouteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	route, err := r.sdk.MCPRoutes().Get(ctx, tenantID, state.RouteID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read MCP route", err)
		return
	}
	refreshed := mcpRouteToModel(tenantID, route)
	if refreshed.Target != nil {
		refreshed.Target.Values = reconcileRouteValues(state, refreshed)
	}
	refreshed.keepEmptyForms(state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPRouteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state MCPRouteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	body, invalidGroups := plan.toWrite(ctx)
	if len(invalidGroups) > 0 {
		addInvalidGroupDiag(&resp.Diagnostics, invalidGroups)
		return
	}
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	route, err := r.sdk.MCPRoutes().Update(ctx, tenantID, state.RouteID.ValueString(), version, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update MCP route", err)
		return
	}
	refreshed := mcpRouteToModel(tenantID, route)
	refreshed.keepConfiguredValues(plan)
	refreshed.keepEmptyForms(plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPRouteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MCPRouteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	err := r.sdk.MCPRoutes().Delete(ctx, tenantID, state.RouteID.ValueString(), version)
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete MCP route", err)
	}
}

func (r *MCPRouteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	tenantID, routeID, diags := parseTenantScopedImportID(req.ID, r.tenantID, "route")
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("route_id"), routeID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+routeID)...)
}

// ModifyPlan warns when an apply widens the route, and plans `mechanism`, which
// follows from the target kind alone.
func (r *MCPRouteResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	warnOnTargetRemoval(ctx, req, resp, "device_group_ids", "every device in the tenant", "")
	warnOnTargetRemoval(ctx, req, resp, "app_bundle_ids", "every app", "")

	var kind types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("target").AtName("kind"), &kind)...)
	if resp.Diagnostics.HasError() || kind.IsUnknown() || kind.IsNull() {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("mechanism"), mcpRouteMechanism(kind.ValueString()))...)
}

// mcpRouteMechanism is McpRouteService.toResponse's rule.
func mcpRouteMechanism(kind string) string {
	if slices.Contains(mcpRouteRemoteKinds, kind) {
		return "rewrite_to_gateway"
	}
	return "shim_wrap"
}

func (r *MCPRouteResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// toWrite builds the request. The PUT is a full replace and reads an omitted
// enabled as true, so the whole route is restated on every write. The second
// return value carries device_group_ids entries that are not UUIDs; callers
// must fail on it, because an empty list means every device in the tenant.
func (m *MCPRouteResourceModel) toWrite(ctx context.Context) (adminapi.MCPRouteWrite, []string) {
	body := adminapi.MCPRouteWrite{}
	setStringPtr(m.Name, &body.Name)
	setStringPtr(m.Description, &body.Description)
	setBoolPtr(m.Enabled, &body.Enabled)
	setStringPtr(m.SteerToURL, &body.SteerToUrl)
	setStringListPtr(ctx, m.AppBundleIDs, &body.AppBundleIds)
	invalidGroups := setUUIDListPtr(ctx, m.DeviceGroupIDs, &body.DeviceGroupIds)
	if m.Target != nil {
		t := adminapi.MCPRouteTarget{}
		if !m.Target.Kind.IsNull() && !m.Target.Kind.IsUnknown() {
			k := gen.McpRouteTargetKind(m.Target.Kind.ValueString())
			t.Kind = &k
		}
		setStringListPtr(ctx, m.Target.Values, &t.Values)
		body.Target = &t
	}
	return body, invalidGroups
}

// reconcileRouteValues decides what target.values holds after a refresh, as
// reconcileMatchValues does for an endpoint MCP rule: when the server's list
// still equals stored_target_values from the last apply, nothing changed out of
// band and the configured spelling is kept; otherwise the server's list is
// taken so the plan shows the drift. On import there is no prior list.
func reconcileRouteValues(prior, refreshed MCPRouteResourceModel) types.List {
	if prior.Target == nil || prior.Target.Values.IsNull() || prior.Target.Values.IsUnknown() ||
		prior.StoredTargetValues.IsNull() || prior.StoredTargetValues.IsUnknown() {
		return refreshed.Target.Values
	}
	if refreshed.StoredTargetValues.Equal(prior.StoredTargetValues) {
		return prior.Target.Values
	}
	return refreshed.Target.Values
}

func mcpRouteToModel(tenantID string, route *adminapi.MCPRoute) MCPRouteResourceModel {
	m := MCPRouteResourceModel{TenantID: types.StringValue(tenantID)}
	if route.Id != nil {
		m.RouteID = types.StringValue(route.Id.String())
	} else {
		m.RouteID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.RouteID.ValueString())
	m.Name = strPtrToTF(route.Name)
	m.Description = emptyStringAsNull(route.Description)
	m.Enabled = boolPtrOrDefault(route.Enabled)

	m.StoredTargetValues = types.ListNull(types.StringType)
	if route.Target != nil {
		m.StoredTargetValues = stringSliceToList(route.Target.Values)
		m.Target = &MCPRouteTargetModel{
			Kind:   enumPtrToTF(route.Target.Kind),
			Values: m.StoredTargetValues,
		}
	}
	m.Mechanism = enumPtrToTF(route.Mechanism)

	m.SteerToURL = emptyStringAsNull(route.SteerToUrl)
	m.AppBundleIDs = emptyListAsNull(stringSliceToList(route.AppBundleIds))
	m.DeviceGroupIDs = emptyListAsNull(stringSliceToList(route.DeviceGroupIds))
	m.CreatedAt = timePtrToTF(route.CreatedAt)
	m.UpdatedAt = timePtrToTF(route.UpdatedAt)

	m.Version = int64PtrToTF(route.Version)
	m.ManagedBy = strPtrToTF(route.ManagedBy)
	m.LastModifiedBy = strPtrToTF(route.LastModifiedBy)
	return m
}

// keepConfiguredValues puts the configured spelling of target.values back
// after a write; the platform's lives in stored_target_values.
func (m *MCPRouteResourceModel) keepConfiguredValues(plan MCPRouteResourceModel) {
	if m.Target != nil && plan.Target != nil {
		m.Target.Values = plan.Target.Values
	}
}

// keepEmptyForms restores an empty form the config wrote explicitly for the
// Optional, non-Computed attributes, which mcpRouteToModel reads as null.
func (m *MCPRouteResourceModel) keepEmptyForms(prior MCPRouteResourceModel) {
	m.Description = keepEmptyString(prior.Description, m.Description)
	m.SteerToURL = keepEmptyString(prior.SteerToURL, m.SteerToURL)
	m.AppBundleIDs = keepEmptyList(prior.AppBundleIDs, m.AppBundleIDs)
	m.DeviceGroupIDs = keepEmptyList(prior.DeviceGroupIDs, m.DeviceGroupIDs)
}
