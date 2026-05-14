package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// MCPServerFromCardResource — the `ferentin_mcp_server_from_card` resource.
// Wraps the platform's `POST /admin/tenants/{tid}/mcp-providers/import-server-card`
// endpoint so a tenant-custom provider + the matching server instance can be
// stamped out from a single MCP-spec server-card.json. Replaces the
// jsondecode + transport-lookup + auth-mapping HCL gymnastics that the
// `examples/mcp-server-from-card/` example demonstrates.
//
// Owns BOTH the provider catalog entry and the instance binding as a single
// lifecycle unit: destroy removes both. Operator-supplied overrides (env /
// enabled / priority / edge_site_id) flow through to the resulting instance
// via a follow-up Update call on the server.
type MCPServerFromCardResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type MCPServerFromCardResourceModel struct {
	ID       types.String `tfsdk:"id"` // composite "<tenant>/<provider_id>/<server_id>"
	TenantID types.String `tfsdk:"tenant_id"`

	// Required user-supplied
	CardJSON types.String `tfsdk:"card_json"` // raw server-card JSON

	// Optional user-supplied overrides
	EdgeSiteID   types.String `tfsdk:"edge_site_id"`
	InstanceName types.String `tfsdk:"instance_name"`
	BearerToken  types.String `tfsdk:"bearer_token"`
	Env          types.Map    `tfsdk:"env"`
	Enabled      types.Bool   `tfsdk:"enabled"`
	Priority     types.Int64  `tfsdk:"priority"`

	// Computed identity
	ProviderID types.String `tfsdk:"provider_id"`
	ServerID   types.String `tfsdk:"server_id"`

	// Computed snapshot of card-derived fields (for plan readability)
	Slug                 types.String `tfsdk:"slug"`
	DisplayName          types.String `tfsdk:"display_name"`
	Endpoint             types.String `tfsdk:"endpoint"`
	TransportType        types.String `tfsdk:"transport_type"`
	UpstreamAuthStrategy types.String `tfsdk:"upstream_auth_strategy"`
	ClientFacingURL      types.String `tfsdk:"client_facing_url"`

	// Computed snapshot of the last import's action: created / refreshed /
	// unchanged. Useful as an output for CI ("unchanged" → checksum hit, no
	// writes happened). The full tool/prompt/resource delta is available
	// via the platform API; v0.1 only surfaces the headline action.
	ImportAction    types.String `tfsdk:"import_action"`
	ImportUnchanged types.Bool   `tfsdk:"import_unchanged"`
}

func NewMCPServerFromCardResource() resource.Resource {
	return &MCPServerFromCardResource{}
}

var (
	_ resource.Resource                     = (*MCPServerFromCardResource)(nil)
	_ resource.ResourceWithConfigure        = (*MCPServerFromCardResource)(nil)
	_ resource.ResourceWithImportState      = (*MCPServerFromCardResource)(nil)
	_ resource.ResourceWithConfigValidators = (*MCPServerFromCardResource)(nil)
)

// ConfigValidators — bearer_token and env are two ways to express the same
// thing (the latter is the general form, the former is sugar for the common
// single-token case). Setting both is ambiguous: which value wins for the
// BEARER_TOKEN key? Reject at plan time instead of silently picking one.
func (r *MCPServerFromCardResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.Conflicting(
			path.MatchRoot("bearer_token"),
			path.MatchRoot("env"),
		),
	}
}

func (r *MCPServerFromCardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_server_from_card"
}

func (r *MCPServerFromCardResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *MCPServerFromCardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Stamps out a tenant-scoped MCP provider + server instance from an MCP-spec " +
			"server-card.json in a single resource. Wraps the platform's " +
			"`POST /admin/tenants/{tenantId}/mcp-providers/import-server-card` endpoint — the platform " +
			"handles slug / transport / auth-mode mapping, EdgeSite-routed promotion for private endpoints, " +
			"and idempotent re-import (checksum-matched re-uploads return `action=\"unchanged\"` with no " +
			"writes). The resource owns BOTH the catalog entry and the binding as one lifecycle unit; " +
			"destroy removes both.\n\n" +
			"Replaces the jsondecode + transport-lookup HCL pattern that the `examples/mcp-server-from-card/` " +
			"example demonstrates. Use the standalone `ferentin_mcp_provider` + `ferentin_mcp_server` " +
			"resources when you need overrides the card can't express (custom slug, different transport, " +
			"multiple instances per provider).\n\n" +
			"## Import\n\n" +
			"Existing card-imported pairs are imported using `<tenant_id>/<provider_id>/<server_id>`:\n\n" +
			"```\n" +
			"terraform import ferentin_mcp_server_from_card.example <tenant_id>/<provider_id>/<server_id>\n" +
			"```",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Tenant the imported provider + server are created under. Defaults to the " +
					"tenant resolved from the provider block's JWT.",
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},

			"card_json": schema.StringAttribute{
				MarkdownDescription: "Raw MCP-spec server-card.json content. Pass via " +
					"`file(\"${path.module}/server-card.json\")` for the canonical workflow. The platform " +
					"parses this server-side; the body bytes are also what the import checksum is computed " +
					"over, so a no-op refresh of the same card returns `action=\"unchanged\"` cheaply.",
				Required: true,
			},
			"edge_site_id": schema.StringAttribute{
				MarkdownDescription: "Edge-site slug to bind the resulting instance to. Required when the card's " +
					"remote URL is private (RFC1918, `.local`, `.internal`, unresolvable DNS) — otherwise the " +
					"platform creates the provider in `draft` status and the corresponding " +
					"`import_result.edge_site_required` flag is set true. Omit for publicly-resolvable HTTPS " +
					"upstreams.",
				Optional: true,
			},
			"instance_name": schema.StringAttribute{
				MarkdownDescription: "Override the platform's default `<slug>-1` instance naming on first " +
					"import. Ignored on subsequent re-imports (the existing instance is reused).",
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},

			"bearer_token": schema.StringAttribute{
				MarkdownDescription: "Bearer token to forward to the upstream MCP when " +
					"`upstream_auth_strategy` (derived from the card) resolves to `static_bearer`. Sugar for " +
					"`env = { BEARER_TOKEN = ... }` — the common case where the card declares a single " +
					"`BEARER_TOKEN` credential field. Mutually exclusive with `env`. Sensitive — redacted " +
					"in logs and plan output.",
				Optional:  true,
				Sensitive: true,
			},
			"env": schema.MapAttribute{
				MarkdownDescription: "Plain-text upstream credentials. Field names match the card's " +
					"`_meta.net.ferentin.curation.credential_fields[].name`. Use `bearer_token` instead for the " +
					"common single-token case. Encrypted server-side at rest; sensitive — redacted in logs " +
					"and plan output.",
				Optional:    true,
				Sensitive:   true,
				ElementType: types.StringType,
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the instance is available for routing. Defaults to the platform's " +
					"behaviour (typically true on first import; preserved on re-import).",
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"priority": schema.Int64Attribute{
				MarkdownDescription: "Routing priority for the instance. Defaults to the platform's behaviour.",
				Optional:            true, Computed: true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},

			"provider_id": schema.StringAttribute{
				MarkdownDescription: "Server-generated UUID for the tenant-custom MCP provider catalog entry.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"server_id": schema.StringAttribute{
				MarkdownDescription: "Server-generated UUID for the provider instance binding.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "Slug derived from the card's `name` last segment (e.g. `threat-intel`).",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				MarkdownDescription: "Display name pulled from the card's `title`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Upstream endpoint URL pulled from the card's `remotes[0].url`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"transport_type": schema.StringAttribute{
				MarkdownDescription: "MCP transport on the instance binding (e.g. `sse`, `streamable_http`, " +
					"`stdio_tunnel`). Inferred from the card's `remotes[0].type`.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"upstream_auth_strategy": schema.StringAttribute{
				MarkdownDescription: "Auth strategy on the instance binding (e.g. `static_bearer`, " +
					"`cc_federated`, `oauth2_user`). Inferred from the card's " +
					"`_meta.net.ferentin.transport.auth_type`.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"client_facing_url": schema.StringAttribute{
				MarkdownDescription: "Client-facing URL agents will connect to for this server. Routed by " +
					"the platform — distinct from the upstream `endpoint`.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},

			"import_action": schema.StringAttribute{
				MarkdownDescription: "What the last create / re-import did: `created` on first import, " +
					"`refreshed` when tools/prompts/resources changed, or `unchanged` when the platform's " +
					"checksum matched the prior upload and no writes happened. Useful as a CI signal — pair " +
					"with `import_unchanged` for a boolean.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"import_unchanged": schema.BoolAttribute{
				MarkdownDescription: "True iff the last import was a checksum-matched no-op " +
					"(equivalent to `import_action == \"unchanged\"`).",
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// Create posts the card, then (if the user supplied operator overrides) issues
// a follow-up mcp_server Update to apply env / enabled / priority.
func (r *MCPServerFromCardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MCPServerFromCardResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	opts := adminapi.MCPCardImportOptions{
		EdgeSiteID:   plan.EdgeSiteID.ValueString(),
		InstanceName: plan.InstanceName.ValueString(),
	}
	out, err := r.sdk.MCPProviders().ImportCard(ctx, tenantID, plan.CardJSON.ValueString(), opts)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to import MCP server card", err)
		return
	}

	state := cardImportToModel(tenantID, plan, out)

	// Operator overrides (env / enabled / priority) aren't carried by the
	// import endpoint — apply them with a follow-up server Update if any of
	// them are set in the plan. If the follow-up fails the user gets an
	// actionable error and the resource is left in a state where the
	// provider+instance exist but the overrides didn't land.
	if r.needsFollowUpUpdate(plan) {
		if err := r.applyFollowUpUpdate(ctx, tenantID, state.ServerID.ValueString(), plan); err != nil {
			addSDKError(&resp.Diagnostics, "Server-card import succeeded but follow-up update failed", err)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
		state.BearerToken = plan.BearerToken
		state.Env = plan.Env
		if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
			state.Enabled = plan.Enabled
		}
		if !plan.Priority.IsNull() && !plan.Priority.IsUnknown() {
			state.Priority = plan.Priority
		}
	}

	// Pull client_facing_url + the platform's canonical echo of endpoint /
	// transport from the response DTO. Best-effort: a NotFound here is
	// fatal (the row should exist post-import), but a transient read error
	// surfaces as a warning rather than rolling the resource back.
	if srv, err := r.sdk.MCPServers().Get(ctx, tenantID, state.ServerID.ValueString()); err == nil {
		enrichFromServer(&state, srv)
	} else if !errors.Is(err, adminapi.ErrNotFound) {
		resp.Diagnostics.AddWarning("Card import succeeded but post-create read failed",
			"client_facing_url won't be populated until the next apply: "+err.Error())
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read refreshes the provider + server pair. import_result stays whatever the
// last create / re-import wrote — the platform doesn't expose a "describe the
// import I did last week" endpoint, so refreshing it would require re-posting
// the card which would be surprising on a Read.
func (r *MCPServerFromCardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MCPServerFromCardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	srv, err := r.sdk.MCPServers().Get(ctx, tenantID, state.ServerID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read MCP server", err)
		return
	}
	prov, err := r.sdk.MCPProviders().Get(ctx, tenantID, state.ProviderID.ValueString())
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to read MCP provider", err)
		return
	}

	refreshed := state // start from prior state to preserve card_json, env, import_result
	if prov != nil {
		refreshed.Slug = strPtrToTF(prov.McpSlug)
		refreshed.DisplayName = strPtrToTF(prov.DisplayName)
	}
	refreshed.Endpoint = strPtrToTF(srv.Endpoint)
	refreshed.TransportType = strPtrToTF(srv.TransportType)
	refreshed.UpstreamAuthStrategy = enumPtrToTF(srv.UpstreamAuthStrategy)
	refreshed.ClientFacingURL = strPtrToTF(srv.ClientFacingUrl)
	refreshed.Enabled = boolPtrOrDefault(srv.Enabled)
	refreshed.Priority = int32PtrToTF(srv.Priority)
	refreshed.EdgeSiteID = strPtrToTF(srv.EdgeSiteId)

	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

// Update re-imports the card whenever card_json or instance_name / edge_site_id
// change (the platform handles checksum-matched no-ops). When only operator
// overrides changed, just call the server Update — no card POST.
func (r *MCPServerFromCardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state MCPServerFromCardResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	cardChanged := !plan.CardJSON.Equal(state.CardJSON)
	edgeSiteChanged := !plan.EdgeSiteID.Equal(state.EdgeSiteID)
	instanceNameChanged := !plan.InstanceName.Equal(state.InstanceName)
	bearerChanged := !plan.BearerToken.Equal(state.BearerToken)
	envChanged := !plan.Env.Equal(state.Env)
	enabledChanged := !plan.Enabled.Equal(state.Enabled)
	priorityChanged := !plan.Priority.Equal(state.Priority)

	reimported := false
	next := state
	if cardChanged || edgeSiteChanged || instanceNameChanged {
		opts := adminapi.MCPCardImportOptions{
			EdgeSiteID:   plan.EdgeSiteID.ValueString(),
			InstanceName: plan.InstanceName.ValueString(),
		}
		out, err := r.sdk.MCPProviders().ImportCard(ctx, tenantID, plan.CardJSON.ValueString(), opts)
		if err != nil {
			addSDKError(&resp.Diagnostics, "Failed to re-import MCP server card", err)
			return
		}
		next = cardImportToModel(tenantID, plan, out)
		reimported = true
	}

	// Follow-up Update fires when one of two things is true:
	//   1. We just re-imported — the import endpoint doesn't apply env /
	//      enabled / priority, so the resulting instance is back to platform
	//      defaults and we need to re-stamp the overrides.
	//   2. An override-field changed between plan and state, with overrides
	//      set in the plan to send.
	// Gate explicitly to avoid re-sending env / enabled / priority on every
	// terraform apply that touched an unrelated attribute.
	overridesDirty := bearerChanged || envChanged || enabledChanged || priorityChanged
	if (reimported || overridesDirty) && r.needsFollowUpUpdate(plan) {
		if err := r.applyFollowUpUpdate(ctx, tenantID, next.ServerID.ValueString(), plan); err != nil {
			addSDKError(&resp.Diagnostics, "Follow-up server update failed", err)
			resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
			return
		}
		next.BearerToken = plan.BearerToken
		next.Env = plan.Env
		if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
			next.Enabled = plan.Enabled
		}
		if !plan.Priority.IsNull() && !plan.Priority.IsUnknown() {
			next.Priority = plan.Priority
		}
	}

	if srv, err := r.sdk.MCPServers().Get(ctx, tenantID, next.ServerID.ValueString()); err == nil {
		enrichFromServer(&next, srv)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

// Delete drops both halves of the imported pair — the server first (referrers
// to the provider), then the provider. NotFound on either is OK (lets the user
// terraform-import a half-deleted pair and clean it up).
func (r *MCPServerFromCardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MCPServerFromCardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	if err := r.sdk.MCPServers().Delete(ctx, tenantID, state.ServerID.ValueString(), ""); err != nil &&
		!errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete MCP server", err)
		return
	}
	if err := r.sdk.MCPProviders().Delete(ctx, tenantID, state.ProviderID.ValueString(), ""); err != nil &&
		!errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete MCP provider", err)
	}
}

func (r *MCPServerFromCardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 3)
	var tenantID, providerID, serverID string
	switch len(parts) {
	case 2:
		tenantID, providerID, serverID = r.tenantID, parts[0], parts[1]
	case 3:
		tenantID, providerID, serverID = parts[0], parts[1], parts[2]
	}
	if tenantID == "" || providerID == "" || serverID == "" {
		resp.Diagnostics.AddError("Cannot determine import identifiers",
			"Pass `<tenant_id>/<provider_id>/<server_id>` or `<provider_id>/<server_id>` (with `tenant_id` configured on the provider block).")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("provider_id"), providerID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("server_id"), serverID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+providerID+"/"+serverID)...)
}

// --- helpers --------------------------------------------------------------

func (r *MCPServerFromCardResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

func (r *MCPServerFromCardResource) needsFollowUpUpdate(plan MCPServerFromCardResourceModel) bool {
	if !plan.Env.IsNull() && !plan.Env.IsUnknown() && len(plan.Env.Elements()) > 0 {
		return true
	}
	if !plan.BearerToken.IsNull() && !plan.BearerToken.IsUnknown() && plan.BearerToken.ValueString() != "" {
		return true
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		return true
	}
	if !plan.Priority.IsNull() && !plan.Priority.IsUnknown() {
		return true
	}
	return false
}

func (r *MCPServerFromCardResource) applyFollowUpUpdate(ctx context.Context, tenantID, serverID string, plan MCPServerFromCardResourceModel) error {
	body := adminapi.MCPServerUpdate{}
	env := resolveEnv(ctx, plan.BearerToken, plan.Env)
	if env != nil {
		body.Env = env
	}
	setBoolPtr(plan.Enabled, &body.Enabled)
	setInt32Ptr(plan.Priority, &body.Priority)

	// No If-Match — we just created the row, no concurrent writer expected.
	if _, err := r.sdk.MCPServers().Update(ctx, tenantID, serverID, "", body); err != nil {
		return err
	}
	return nil
}

// resolveEnv merges the bearer_token sugar attribute into the env map.
// ConfigValidators rejects setting both — so at runtime at most one of the
// two is present. Returns nil when neither is set so callers can skip
// setting body.Env entirely (vs sending an empty map, which the platform
// might interpret as "clear all credentials").
func resolveEnv(ctx context.Context, bearer types.String, envMap types.Map) *map[string]string {
	if !bearer.IsNull() && !bearer.IsUnknown() && bearer.ValueString() != "" {
		m := map[string]string{"BEARER_TOKEN": bearer.ValueString()}
		return &m
	}
	if !envMap.IsNull() && !envMap.IsUnknown() && len(envMap.Elements()) > 0 {
		var m map[string]string
		_ = envMap.ElementsAs(ctx, &m, false)
		return &m
	}
	return nil
}

// cardImportToModel populates the identity + import_result delta from the
// raw import response. Server-side computed fields (client_facing_url,
// transport_type echo, etc.) come from a follow-up MCPServers().Get call;
// see enrichFromServer below.
func cardImportToModel(tenantID string, plan MCPServerFromCardResourceModel, out *adminapi.MCPServerCardImportResponse) MCPServerFromCardResourceModel {
	m := MCPServerFromCardResourceModel{
		TenantID:     types.StringValue(tenantID),
		CardJSON:     plan.CardJSON,
		EdgeSiteID:   plan.EdgeSiteID,
		InstanceName: plan.InstanceName,
		Env:          plan.Env,
	}

	if out.Provider != nil && out.Provider.Id != nil {
		m.ProviderID = types.StringValue(out.Provider.Id.String())
		m.Slug = strPtrToTF(out.Provider.McpSlug)
		m.DisplayName = strPtrToTF(out.Provider.DisplayName)
	} else {
		m.ProviderID = types.StringNull()
		m.Slug = types.StringNull()
		m.DisplayName = types.StringNull()
	}

	// Post ferentin-platform#853 Instance arrives as McpServerResponseDto
	// (DTO) — all fields pointer-typed, so we can use the standard helpers
	// without the entity's required-field gymnastics.
	if out.Instance != nil && out.Instance.Id != nil {
		m.ServerID = types.StringValue(out.Instance.Id.String())
		m.Endpoint = strPtrToTF(out.Instance.Endpoint)
		m.TransportType = strPtrToTF(out.Instance.TransportType)
		m.UpstreamAuthStrategy = enumPtrToTF(out.Instance.UpstreamAuthStrategy)
		m.Priority = int32PtrToTF(out.Instance.Priority)
		m.Enabled = boolPtrOrDefault(out.Instance.Enabled)
		m.EdgeSiteID = strPtrToTF(out.Instance.EdgeSiteId)
		m.InstanceName = strPtrToTF(out.Instance.Name)
		m.ClientFacingURL = strPtrToTF(out.Instance.ClientFacingUrl)
	} else {
		m.ServerID = types.StringNull()
		m.Endpoint = types.StringNull()
		m.TransportType = types.StringNull()
		m.UpstreamAuthStrategy = types.StringNull()
		m.Enabled = types.BoolNull()
		m.Priority = types.Int64Null()
		m.ClientFacingURL = types.StringNull()
	}

	m.ImportAction, m.ImportUnchanged = importActionFromResult(out.ImportResult)
	m.ID = types.StringValue(tenantID + "/" + m.ProviderID.ValueString() + "/" + m.ServerID.ValueString())
	return m
}

func importActionFromResult(ir *adminapi.MCPServerCardImportResult) (types.String, types.Bool) {
	if ir == nil || ir.Action == nil {
		// Platform shipped a 201 with no ImportResult — treat as `created`
		// rather than null so the resource always exposes a known action.
		return types.StringValue("created"), types.BoolValue(false)
	}
	return types.StringValue(*ir.Action), types.BoolValue(*ir.Action == "unchanged")
}

// enrichFromServer overlays server-side computed fields that the import
// response's entity-typed Instance doesn't expose. Idempotent — overwrites
// only the fields it covers.
func enrichFromServer(m *MCPServerFromCardResourceModel, srv *adminapi.MCPServer) {
	if srv == nil {
		return
	}
	m.ClientFacingURL = strPtrToTF(srv.ClientFacingUrl)
	// Prefer the response DTO's echoed values for these — they reflect any
	// platform-side normalisation (case, default fill-in) the entity doesn't.
	if srv.Endpoint != nil {
		m.Endpoint = strPtrToTF(srv.Endpoint)
	}
	if srv.TransportType != nil {
		m.TransportType = strPtrToTF(srv.TransportType)
	}
}


