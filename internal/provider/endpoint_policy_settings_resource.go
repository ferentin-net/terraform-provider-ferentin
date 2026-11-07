package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// A platform column default, mirrored here as a schema default so a config that
// omits the field plans to the same value the server would pick.
//
// Mirroring a server default into a provider constant is a liability, and the list
// is one entry shorter than it used to be for that reason. A `Default` on an
// Optional+Computed attribute is a silent data migration: when the mirrored value
// changes, every config that omits the attribute plans the new value and APPLIES it
// to rows that already exist. GH #2127 hit exactly that with the unapproved-MCP
// action (since moved to ferentin_mcp_scope_defaults), which is why the
// server-defaulted attributes here defer to the server via Computed +
// UseStateForUnknown. Prefer that shape for anything new; keep a mirrored Default
// only where an omitted value genuinely must resolve client-side.
//
// NOTE: this is NOT what Delete writes. Destroy deliberately leaves the
// tenant-default row enforcing whatever was last applied; see Delete.
const (
	postureDefaultDestinationAction = "allow"
)

// EndpointPolicySettingsResource is the `ferentin_endpoint_policy_settings`
// Terraform resource — endpoint posture for a tenant, or for one device group
// (platform#2010, IaC-ready per platform#2038).
//
// # Why this resource looks different from every other one here
//
// The platform API is UPSERT-ONLY: there is no POST, no GET-by-id, and identity
// is (tenant_id, device_group_id) enforced by two partial unique indexes rather
// than a surrogate key. That drives four deliberate deviations:
//
//  1. Create is a PUT that may ADOPT a pre-existing row. A tenant that
//     configured posture in the console before adopting Terraform will find
//     that `terraform apply` takes ownership of the existing row rather than
//     failing on conflict. `managed_by` still reports the original creator, so
//     the adoption is visible.
//  2. Read LISTS /settings and filters on device_group_id (null ⇒ the tenant
//     default). There is no single-row endpoint to call.
//  3. `device_group_id` is RequiresReplace — it is the identity, not a field.
//  4. Delete is a NO-OP for the tenant-default row — the platform refuses to
//     delete it (every device, including ungrouped ones, resolves through it),
//     so Terraform drops it from state and leaves it enforcing. Group overrides
//     get a real DELETE. See Delete for why this is not a reset-to-defaults.
type EndpointPolicySettingsResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type EndpointPolicySettingsResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	DeviceGroupID types.String `tfsdk:"device_group_id"`

	McpGatewayURL            types.String `tfsdk:"mcp_gateway_url"`
	DefaultDestinationAction types.String `tfsdk:"default_destination_action"`
	EchStripEnabled          types.Bool   `tfsdk:"ech_strip_enabled"`
	DohBlockEnabled          types.Bool   `tfsdk:"doh_block_enabled"`
	QuicBlockEnabled         types.Bool   `tfsdk:"quic_block_enabled"`

	// Server-defaulted and console-editable (platform#3011): Optional + Computed +
	// UseStateForUnknown, no Default, so omitting it keeps what the tenant has.
	// The upsert keeps any field the request leaves out.
	HookRuleEnforcement types.String `tfsdk:"hook_rule_enforcement"`

	// More server-defaulted, console-editable posture fields, same shape as the
	// one above: Optional + Computed + UseStateForUnknown, no Default, sent only
	// when configured. The three sets are normalized by the platform; see
	// reconcilePlatformForms.
	UnapprovedAgentAction   types.String `tfsdk:"unapproved_agent_action"`
	LocalMcpBlockEnabled    types.Bool   `tfsdk:"local_mcp_block_enabled"`
	UnnamedFlowBlockEnabled types.Bool   `tfsdk:"unnamed_flow_block_enabled"`
	UnnamedFlowExemptCIDRs  types.Set    `tfsdk:"unnamed_flow_exempt_cidrs"`
	DohResolverHosts        types.Set    `tfsdk:"doh_resolver_hosts"`
	DohResolverIPs          types.Set    `tfsdk:"doh_resolver_ips"`

	SettingsID types.String `tfsdk:"settings_id"`
	CreatedAt  types.String `tfsdk:"created_at"`
	UpdatedAt  types.String `tfsdk:"updated_at"`

	// IaC readiness (platform#2038).
	Version           types.Int64  `tfsdk:"version"` // for If-Match
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

func NewEndpointPolicySettingsResource() resource.Resource {
	return &EndpointPolicySettingsResource{}
}

var (
	_ resource.Resource                = (*EndpointPolicySettingsResource)(nil)
	_ resource.ResourceWithConfigure   = (*EndpointPolicySettingsResource)(nil)
	_ resource.ResourceWithImportState = (*EndpointPolicySettingsResource)(nil)
)

func (r *EndpointPolicySettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_endpoint_policy_settings"
}

func (r *EndpointPolicySettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *EndpointPolicySettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Endpoint posture for managed devices — the MCP gateway target, the " +
			"DNS/QUIC network flags, the default action for AI destinations with no matching " +
			"`ferentin_endpoint_destination_rule`, call-time MCP rule enforcement, the unapproved-agent " +
			"action and the local MCP kill switch.\n\n" +
			"What a local MCP server or tool meets when no MCP policy applies to it (server approval, " +
			"unattributed agents, tool vetting with its risk policy and approval routing) is set on " +
			"[`ferentin_mcp_scope_defaults`](mcp_scope_defaults.md), which writes the same posture row; " +
			"see that resource for how the two share it.\n\n" +
			"Omit `device_group_id` for the **tenant default** row (which every device, including " +
			"ungrouped ones, resolves through). Set it for a **per-group override**.\n\n" +
			"~> **This resource is an upsert, not a create.** The platform API has no POST and no " +
			"GET-by-id; identity is `(tenant_id, device_group_id)`. Consequences:\n\n" +
			"* If posture already exists (e.g. configured in the console), `terraform apply` **adopts** " +
			"the existing row rather than failing. `managed_by` still reports the original creator, so " +
			"the adoption is visible as drift.\n" +
			"* `terraform destroy` on the **tenant default** row does not delete it and does not " +
			"change it — the platform refuses to delete a row every device resolves through. " +
			"Terraform drops it from state and **the fleet keeps enforcing the last applied " +
			"posture**, with a warning saying so. This is fail-closed on purpose: resetting to the " +
			"permissive defaults would silently de-enforce the whole fleet, and removing the " +
			"resource block or dropping a module reaches the same code path as an explicit " +
			"destroy. To stand enforcement down, apply the permissive posture first, then " +
			"destroy. A group override *is* genuinely deleted, and the group falls back to the " +
			"tenant default.\n\n" +
			"## Import\n\n" +
			"Tenant default row:\n\n" +
			"```\n" +
			"terraform import ferentin_endpoint_policy_settings.default <tenant_id>\n" +
			"```\n\n" +
			"Per-group override:\n\n" +
			"```\n" +
			"terraform import ferentin_endpoint_policy_settings.contractors <tenant_id>/<device_group_id>\n" +
			"```",

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
			"device_group_id": schema.StringAttribute{
				MarkdownDescription: "Device group this posture overrides — reference " +
					"`ferentin_device_group.<name>.group_id`. Omit for the tenant default row.\n\n" +
					"This is part of the resource **identity**, not a mutable field: changing it forces " +
					"replacement, because a different value addresses a different row.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			"mcp_gateway_url": schema.StringAttribute{
				MarkdownDescription: "Tenant MCP gateway base URL that approved server configs are " +
					"rewritten to point at: the tenant's gateway on the public edge, " +
					"`https://<tenant-short-name>.mcp.ferentin.net/v1`. Unset = the agent reports what it " +
					"finds and rewrites nothing.\n\n" +
					"Must be `https://` — an `http://` value would downgrade every governed MCP session " +
					"on the fleet to cleartext, which is strictly worse than the ungoverned status quo it " +
					"replaces. The platform rejects it; this fails at plan.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(httpsOnlyURL,
						"must be an https:// URL — http:// would downgrade governed MCP sessions to cleartext"),
				},
			},
			"default_destination_action": schema.StringAttribute{
				MarkdownDescription: "Action for an AI destination with no matching destination rule: " +
					"`allow` (default) or `block`. Setting `block` makes the rule set an allowlist — " +
					"verify your rules cover every sanctioned provider first.",
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString(postureDefaultDestinationAction),
				Validators: []validator.String{stringvalidator.OneOf("allow", "block")},
			},
			"ech_strip_enabled": schema.BoolAttribute{
				MarkdownDescription: "Strip HTTPS/SVCB DNS records so Encrypted Client Hello is not " +
					"advertised, keeping SNI inspectable. Default `false`.",
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
			},
			"doh_block_enabled": schema.BoolAttribute{
				MarkdownDescription: "Block external DNS-over-HTTPS resolvers so name resolution stays " +
					"observable. Default `false`.",
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
			},
			"quic_block_enabled": schema.BoolAttribute{
				MarkdownDescription: "Block QUIC / UDP-443 to force TCP fallback where SNI is " +
					"inspectable. Default `false`.",
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
			},
			"hook_rule_enforcement": schema.StringAttribute{
				MarkdownDescription: "Call-time enforcement of MCP `block` rules:\n\n" +
					"* `shadow` — count what a `block` rule would deny, deny nothing\n" +
					"* `enforce` — deny the tool call (Claude Code `PreToolUse`, Cursor " +
					"`beforeShellExecution` / `beforeMCPExecution`)\n\n" +
					"Omit to keep the current value (the platform's default on a new row).\n\n" +
					"~> Moving a row to `enforce` is refused with 409 until every active device in its " +
					"scope runs an agent build that enforces call-time rules (see " +
					"`hookRuleEnforcementCoverage` on the posture fleet status in the console). A row " +
					"already on `enforce` saves normally.",
				// Computed + UseStateForUnknown, no Default — see the note at the top of the file.
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.OneOf("shadow", "enforce")},
			},
			"unapproved_agent_action": schema.StringAttribute{
				MarkdownDescription: "What the agent does about a catalogued AI agent found on a device that is " +
					"not approved: `report_only` (report it) or `block`. Omit to keep the current value " +
					"(`report_only` on a new row).",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.OneOf("report_only", "block")},
			},
			"local_mcp_block_enabled": schema.BoolAttribute{
				MarkdownDescription: "Block every locally spawned (stdio) MCP server in this scope. Absolute: " +
					"it beats an approved registry entry and a matching `allow` rule. Remote MCP servers " +
					"are unaffected. Exceptions are made by turning this off for a device group. Omit to " +
					"keep the current value.\n\n" +
					"~> Turning it on requires the fleet on an agent that implements the control: an older " +
					"agent refuses the posture section and keeps its last-known-good policy.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"unnamed_flow_block_enabled": schema.BoolAttribute{
				MarkdownDescription: "Drop port-443 flows (TCP and QUIC) to public addresses that have no " +
					"hostname attribution: the raw-IP / external-DoH / ECH-degraded traffic no host rule " +
					"can match. Private, CGNAT and link-local ranges are out of scope. Omit to keep the " +
					"current value.\n\n" +
					"~> Turning it on requires the fleet on an agent that implements the control: an older " +
					"agent refuses the posture section and keeps its last-known-good policy.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"unnamed_flow_exempt_cidrs": schema.SetAttribute{
				MarkdownDescription: "Addresses or CIDRs exempt from the unnamed-flow block, for infrastructure " +
					"reached by bare IP on 443 (VPN concentrators, monitoring). At most 64; a `/0` prefix is " +
					"refused. The platform canonicalizes them (host bits masked, duplicates collapsed); your " +
					"spelling is kept here. Omit to keep the current list; `[]` clears it.",
				Optional:      true,
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				Validators: []validator.Set{
					setvalidator.SizeAtMost(64),
					setvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1), noZeroPrefix{}),
				},
			},
			"doh_resolver_hosts": schema.SetAttribute{
				MarkdownDescription: "DoH resolver hostnames the DoH block matches; `*.` and leading-`.` suffix " +
					"patterns are allowed. At most 256. Lowercased by the platform; your spelling is kept " +
					"here. Omit to keep the current list; `[]` clears it, returning the agent to its built-in " +
					"list.\n\n" +
					"~> Authoring either DoH list requires the fleet on agent 0.9 or later: older agents " +
					"refuse a posture section with unknown keys and keep their last-known-good policy.",
				Optional:      true,
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				Validators: []validator.Set{
					setvalidator.SizeAtMost(256),
					setvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 255)),
				},
			},
			"doh_resolver_ips": schema.SetAttribute{
				MarkdownDescription: "DoH bootstrap IP addresses the DoH block matches, on port 443 only. At most " +
					"256. Canonicalized by the platform; your spelling is kept here. Omit to keep the " +
					"current list; `[]` clears it. Same agent requirement as `doh_resolver_hosts`.",
				Optional:      true,
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				Validators: []validator.Set{
					setvalidator.SizeAtMost(256),
					setvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
				},
			},

			"settings_id": schema.StringAttribute{
				MarkdownDescription: "Platform row UUID. Informational — the resource is addressed by " +
					"`(tenant_id, device_group_id)`, not by this id.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{Computed: true},

			// ===== IaC-readiness attributes (platform#2038) =====
			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version, threaded as `If-Match` on " +
					"update, and on delete of a group override (the tenant-default row is never " +
					"deleted, so nothing is sent for it).\n\n" +
					"~> On this upsert resource the platform treats an `If-Match` against a row that " +
					"does **not** exist as a failed precondition (412), not a create — it refuses to " +
					"resurrect a deleted row at version 0 with fresh provenance. The provider therefore " +
					"sends no `If-Match` on the first write.",
				Computed: true,
			},
			"managed_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the original creator (`iac` for this provider). " +
					"Immutable after create. On an adopted row this reports whoever created it first — " +
					"`console`, for posture configured in the admin console before Terraform took over.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"managed_by_client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth2 `client_id` of the creator, from the authenticated principal.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"managed_by_module": schema.StringAttribute{
				MarkdownDescription: "Module label this provider sent via `X-Ferentin-Managed-By-Module`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent writer. Divergence from `managed_by` " +
					"is the drift signal.",
				Computed: true,
			},
		},
	}
}

func (r *EndpointPolicySettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan EndpointPolicySettingsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	// No If-Match on the first write: the row may not exist, and on this upsert
	// the platform answers an If-Match against a missing row with 412.
	// On Create an attribute the config leaves out is unknown in the plan, so
	// toWrite already omits it. The row may exist (adoption), so its prior
	// hook_rule_enforcement is unknown here; treat any `enforce` as a move.
	body, d := plan.toWrite(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.upsert(ctx, tenantID, plan.DeviceGroupID, "", body)
	if err != nil {
		addEndpointSettingsWriteError(&resp.Diagnostics, "Failed to write endpoint policy settings", err,
			body, types.StringNull())
		return
	}
	state := endpointSettingsToModel(tenantID, row)
	forms, d := reconcilePlatformForms(&state, plan, body, platformForms{})
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(storePlatformForms(ctx, resp.Private, forms)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *EndpointPolicySettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state EndpointPolicySettingsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	row, err := r.fetch(ctx, tenantID, state.DeviceGroupID)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			// For the tenant-default row ErrNotFound means "the tenant has
			// authored no posture", which after a destroy-reset is the expected
			// end state; either way the row Terraform tracked is gone.
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read endpoint policy settings", err)
		return
	}
	refreshed := endpointSettingsToModel(tenantID, row)
	prior, d := loadPlatformForms(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	forms, d := reconcilePlatformForms(&refreshed, state, adminapi.EndpointPolicySettingsWrite{}, prior)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(storePlatformForms(ctx, resp.Private, forms)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *EndpointPolicySettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, config EndpointPolicySettingsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	body, d := plan.toWrite(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	omitUnconfigured(&body, config)
	prior, d := loadPlatformForms(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	// ferentin_mcp_scope_defaults writes the same row; see retryStalePostureVersion.
	row, err := retryStalePostureVersion(version,
		func(v string) (*adminapi.EndpointPolicySettings, error) {
			return r.upsert(ctx, tenantID, state.DeviceGroupID, v, body)
		},
		r.rereadOwned(ctx, tenantID, state, prior, settingsSentFields(body)))
	if err != nil {
		if addPostureConflictError(&resp.Diagnostics, "Failed to update endpoint policy settings", err) {
			return
		}
		addEndpointSettingsWriteError(&resp.Diagnostics, "Failed to update endpoint policy settings", err,
			body, state.HookRuleEnforcement)
		return
	}
	refreshed := endpointSettingsToModel(tenantID, row)
	// A value the request sent takes the planned (configured) value, once the
	// response shows it was stored; one it did not send reconciles against
	// state like a refresh. plan carries state for an omitted attribute
	// (UseStateForUnknown), so it serves as the prior too.
	forms, d := reconcilePlatformForms(&refreshed, plan, body, prior)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(storePlatformForms(ctx, resp.Private, forms)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

// Delete has two behaviours because the platform has two shapes of row.
//
//   - A GROUP OVERRIDE is genuinely deleted. The group then falls back to the
//     tenant default, which is real inheritance, not a weakening.
//
//   - The TENANT DEFAULT row cannot be deleted — the platform returns 400,
//     because every device including ungrouped ones resolves through it. So
//     destroy DROPS IT FROM STATE AND LEAVES THE ROW ENFORCING AS-IS.
//
// # Why not reset it to the platform defaults
//
// Resetting to `report_only` / `allow` / flags-off is tempting: it looks like
// "undo the Terraform-managed configuration". It is fail-OPEN, and the path is
// reachable by ordinary refactoring — deleting the resource block from HCL, or
// dropping a module, runs Delete just as `terraform destroy` does. A fleet
// would silently fall from "quarantine unapproved MCP servers, allowlist AI
// destinations" to "observe only", arriving on-device as a routine bundle
// update with no operator signal beyond an audit row.
//
// Leaving the row alone matches how Terraform providers handle an adopted
// singleton they cannot delete: `aws_default_vpc` drops state and touches
// nothing, and `aws_default_security_group` resets to MAXIMALLY RESTRICTIVE.
// Neither resets to permissive.
//
// Standing enforcement down is therefore an explicit two-step an operator has
// to mean: apply the permissive posture, then destroy. Both steps are audited.
func (r *EndpointPolicySettingsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state EndpointPolicySettingsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	if !isUnsetString(state.DeviceGroupID) {
		// No stale-version retry here, unlike Update. A DELETE removes the whole
		// row, including the group's own MCP scope defaults
		// (ferentin_mcp_scope_defaults or the console), which this resource does
		// not model. A 412 is the only sign one of them changed since the
		// refresh, so it is reported rather than retried past.
		version := strconv.FormatInt(state.Version.ValueInt64(), 10)
		err := r.sdk.EndpointPolicies().DeleteGroupSettings(ctx, tenantID,
			state.DeviceGroupID.ValueString(), version)
		if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
			if addPostureConflictError(&resp.Diagnostics, "Failed to delete endpoint policy settings override", err) {
				return
			}
			addSDKError(&resp.Diagnostics, "Failed to delete endpoint policy settings override", err)
		}
		return
	}

	// Tenant default: no API call at all. Terraform removes the resource from
	// state when Delete returns without error.
	resp.Diagnostics.AddWarning(
		"Tenant endpoint posture removed from state, but STILL IN FORCE",
		fmt.Sprintf("The platform does not allow deleting the tenant-default endpoint posture — every "+
			"managed device, including ungrouped ones, resolves through it. Terraform has stopped "+
			"managing the row; the fleet continues to enforce the last applied posture "+
			"(default_destination_action = %q).\n\n"+
			"This is deliberate: resetting to the permissive defaults on destroy would silently "+
			"de-enforce the whole fleet, and removing a resource block or dropping a module reaches "+
			"this path just as `terraform destroy` does.\n\n"+
			"To actually stand enforcement down, do it explicitly and then destroy:\n"+
			"  1. Set unapproved_agent_action = \"report_only\", "+
			"default_destination_action = \"allow\", "+
			"hook_rule_enforcement = \"shadow\", clear mcp_gateway_url, and turn the network flags, "+
			"local_mcp_block_enabled and unnamed_flow_block_enabled off. The DoH resolver lists stay "+
			"as they are; clear them in the console if needed. The MCP scope defaults are "+
			"ferentin_mcp_scope_defaults' and are not changed by this destroy.\n"+
			"  2. terraform apply\n"+
			"  3. terraform destroy (or remove the resource block)",
			state.DefaultDestinationAction.ValueString()))
}

func (r *EndpointPolicySettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// "<tenant_id>" = the tenant-default row; "<tenant_id>/<device_group_id>" =
	// an override. A bare "<device_group_id>" is NOT accepted: it is
	// indistinguishable from a bare tenant id, and guessing wrong would import
	// the tenant default over a group override (or vice versa) and then
	// overwrite the wrong row on the next apply.
	parts := strings.SplitN(req.ID, "/", 2)
	tenantID, groupID := parts[0], ""
	if len(parts) == 2 {
		groupID = parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>` for the tenant-default row or `<tenant_id>/<device_group_id>` for an override.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	if groupID == "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("device_group_id"), types.StringNull())...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID)...)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("device_group_id"), groupID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+groupID)...)
}

// settingsOwnedAttrs is every posture attribute this resource writes.
var settingsOwnedAttrs = []string{
	"mcp_gateway_url", "default_destination_action", "ech_strip_enabled", "doh_block_enabled",
	"quic_block_enabled", "hook_rule_enforcement", "unapproved_agent_action", "local_mcp_block_enabled",
	"unnamed_flow_block_enabled", "unnamed_flow_exempt_cidrs", "doh_resolver_hosts", "doh_resolver_ips",
}

// settingsSentFields names the attributes body writes.
func settingsSentFields(body adminapi.EndpointPolicySettingsWrite) []string {
	var out []string
	add := func(sent bool, name string) {
		if sent {
			out = append(out, name)
		}
	}
	add(body.McpGatewayUrl != nil, "mcp_gateway_url")
	add(body.DefaultDestinationAction != nil, "default_destination_action")
	add(body.EchStripEnabled != nil, "ech_strip_enabled")
	add(body.DohBlockEnabled != nil, "doh_block_enabled")
	add(body.QuicBlockEnabled != nil, "quic_block_enabled")
	add(body.HookRuleEnforcement != nil, "hook_rule_enforcement")
	add(body.UnapprovedAgentAction != nil, "unapproved_agent_action")
	add(body.LocalMcpBlockEnabled != nil, "local_mcp_block_enabled")
	add(body.UnnamedFlowBlockEnabled != nil, "unnamed_flow_block_enabled")
	add(body.UnnamedFlowExemptCidrs != nil, "unnamed_flow_exempt_cidrs")
	add(body.DohResolverHosts != nil, "doh_resolver_hosts")
	add(body.DohResolverIps != nil, "doh_resolver_ips")
	return out
}

// settingsAttrValue returns the model's value for one attribute in
// settingsOwnedAttrs.
func settingsAttrValue(m EndpointPolicySettingsResourceModel, name string) attr.Value {
	switch name {
	case "mcp_gateway_url":
		return m.McpGatewayURL
	case "default_destination_action":
		return m.DefaultDestinationAction
	case "ech_strip_enabled":
		return m.EchStripEnabled
	case "doh_block_enabled":
		return m.DohBlockEnabled
	case "quic_block_enabled":
		return m.QuicBlockEnabled
	case "hook_rule_enforcement":
		return m.HookRuleEnforcement
	case "unapproved_agent_action":
		return m.UnapprovedAgentAction
	case "local_mcp_block_enabled":
		return m.LocalMcpBlockEnabled
	case "unnamed_flow_block_enabled":
		return m.UnnamedFlowBlockEnabled
	case "unnamed_flow_exempt_cidrs":
		return m.UnnamedFlowExemptCIDRs
	case "doh_resolver_hosts":
		return m.DohResolverHosts
	case "doh_resolver_ips":
		return m.DohResolverIPs
	}
	return nil
}

// settingsOwnedChanges names the attributes among names whose value in fresh
// (the platform's row, reconciled against prior the way a refresh is) differs
// from prior (state).
func settingsOwnedChanges(prior, fresh EndpointPolicySettingsResourceModel, names []string) []string {
	fields := make([]ownedField, 0, len(names))
	for _, n := range names {
		fields = append(fields, ownedField{name: n, prior: settingsAttrValue(prior, n), fresh: settingsAttrValue(fresh, n)})
	}
	return changedFields(fields)
}

// rereadOwned is retryStalePostureVersion's re-read for this resource: the
// row's fresh version, and which of names changed since state was read.
func (r *EndpointPolicySettingsResource) rereadOwned(ctx context.Context, tenantID string,
	state EndpointPolicySettingsResourceModel, recorded platformForms, names []string) func() (string, []string, error) {
	return func() (string, []string, error) {
		row, err := r.fetch(ctx, tenantID, state.DeviceGroupID)
		if err != nil {
			return "", nil, err
		}
		fresh := endpointSettingsToModel(tenantID, row)
		_, _ = reconcilePlatformForms(&fresh, state, adminapi.EndpointPolicySettingsWrite{}, recorded)
		return int64PtrString(row.Version), settingsOwnedChanges(state, fresh, names), nil
	}
}

// upsert routes to the tenant-default or the per-group endpoint. One helper so
// Create and Update cannot drift on which URL they hit — writing a group's
// posture over the tenant default would silently apply one group's strictness
// to the entire fleet.
func (r *EndpointPolicySettingsResource) upsert(ctx context.Context, tenantID string,
	groupID types.String, version string,
	body adminapi.EndpointPolicySettingsWrite) (*adminapi.EndpointPolicySettings, error) {
	if isUnsetString(groupID) {
		return r.sdk.EndpointPolicies().UpsertTenantSettings(ctx, tenantID, version, body)
	}
	return r.sdk.EndpointPolicies().UpsertGroupSettings(ctx, tenantID, groupID.ValueString(), version, body)
}

func (r *EndpointPolicySettingsResource) fetch(ctx context.Context, tenantID string,
	groupID types.String) (*adminapi.EndpointPolicySettings, error) {
	if isUnsetString(groupID) {
		return r.sdk.EndpointPolicies().GetTenantSettings(ctx, tenantID)
	}
	return r.sdk.EndpointPolicies().GetGroupSettings(ctx, tenantID, groupID.ValueString())
}

func (r *EndpointPolicySettingsResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

func (m *EndpointPolicySettingsResourceModel) toWrite(ctx context.Context) (adminapi.EndpointPolicySettingsWrite, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := adminapi.EndpointPolicySettingsWrite{}
	setStringPtr(m.DefaultDestinationAction, &body.DefaultDestinationAction)
	setBoolPtr(m.EchStripEnabled, &body.EchStripEnabled)
	setBoolPtr(m.DohBlockEnabled, &body.DohBlockEnabled)
	setBoolPtr(m.QuicBlockEnabled, &body.QuicBlockEnabled)
	// Sent only when configured: omitted, the upsert keeps the stored value, which
	// is how a console-set value survives a config that does not mention it.
	if !m.HookRuleEnforcement.IsNull() && !m.HookRuleEnforcement.IsUnknown() {
		h := gen.EndpointPolicySettingsRequestHookRuleEnforcement(m.HookRuleEnforcement.ValueString())
		body.HookRuleEnforcement = &h
	}
	setStringPtr(m.UnapprovedAgentAction, &body.UnapprovedAgentAction)
	setBoolPtr(m.LocalMcpBlockEnabled, &body.LocalMcpBlockEnabled)
	setBoolPtr(m.UnnamedFlowBlockEnabled, &body.UnnamedFlowBlockEnabled)
	// A known empty set is sent as [] (the platform's clear).
	body.UnnamedFlowExemptCidrs = setToSDK(ctx, m.UnnamedFlowExemptCIDRs)
	body.DohResolverHosts = setToSDK(ctx, m.DohResolverHosts)
	body.DohResolverIps = setToSDK(ctx, m.DohResolverIPs)

	// mcp_gateway_url is optional-not-computed, so a config that drops it must
	// actively CLEAR the stored value. The platform's upsert preserves omitted
	// fields, so sending nothing would leave a stale gateway URL rewriting every
	// approved MCP config on the fleet after the operator thought they removed
	// it. An explicit empty string is how the API clears it.
	if isUnsetString(m.McpGatewayURL) {
		empty := ""
		body.McpGatewayUrl = &empty
	} else {
		setStringPtr(m.McpGatewayURL, &body.McpGatewayUrl)
	}
	return body, diags
}

func endpointSettingsToModel(tenantID string, row *adminapi.EndpointPolicySettings) EndpointPolicySettingsResourceModel {
	m := EndpointPolicySettingsResourceModel{TenantID: types.StringValue(tenantID)}
	if row.DeviceGroupId != nil {
		m.DeviceGroupID = types.StringValue(row.DeviceGroupId.String())
		m.ID = types.StringValue(tenantID + "/" + row.DeviceGroupId.String())
	} else {
		m.DeviceGroupID = types.StringNull()
		m.ID = types.StringValue(tenantID)
	}
	if row.Id != nil {
		m.SettingsID = types.StringValue(row.Id.String())
	} else {
		m.SettingsID = types.StringNull()
	}
	m.McpGatewayURL = strPtrToTF(row.McpGatewayUrl)
	m.DefaultDestinationAction = strPtrToTF(row.DefaultDestinationAction)
	m.EchStripEnabled = boolPtrOrDefault(row.EchStripEnabled)
	m.DohBlockEnabled = boolPtrOrDefault(row.DohBlockEnabled)
	m.QuicBlockEnabled = boolPtrOrDefault(row.QuicBlockEnabled)
	m.HookRuleEnforcement = enumPtrToTF(row.HookRuleEnforcement)
	m.UnapprovedAgentAction = strPtrToTF(row.UnapprovedAgentAction)
	m.LocalMcpBlockEnabled = boolPtrOrDefault(row.LocalMcpBlockEnabled)
	m.UnnamedFlowBlockEnabled = boolPtrOrDefault(row.UnnamedFlowBlockEnabled)
	m.UnnamedFlowExemptCIDRs = stringSliceToSetOrNull(row.UnnamedFlowExemptCidrs)
	m.DohResolverHosts = stringSliceToSetOrNull(row.DohResolverHosts)
	m.DohResolverIPs = stringSliceToSetOrNull(row.DohResolverIps)
	m.CreatedAt = timePtrToTF(row.CreatedAt)
	m.UpdatedAt = timePtrToTF(row.UpdatedAt)

	m.Version = int64PtrToTF(row.Version)
	m.ManagedBy = enumPtrToTF(row.ManagedBy)
	m.ManagedByClientID = strPtrToTF(row.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(row.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(row.LastModifiedBy)
	return m
}

// omitUnconfigured drops the server-defaulted fields the config does not set.
//
// UseStateForUnknown puts the STATE value into the plan for an attribute the
// config leaves out, so on Update toWrite would send it back. The upsert keeps
// an omitted field, so dropping it here is what makes "sent only when set in
// config" true on Update as well as Create, and keeps an apply from rewriting a
// value it does not manage.
func omitUnconfigured(body *adminapi.EndpointPolicySettingsWrite, config EndpointPolicySettingsResourceModel) {
	if config.HookRuleEnforcement.IsNull() {
		body.HookRuleEnforcement = nil
	}
	if config.UnapprovedAgentAction.IsNull() {
		body.UnapprovedAgentAction = nil
	}
	if config.LocalMcpBlockEnabled.IsNull() {
		body.LocalMcpBlockEnabled = nil
	}
	if config.UnnamedFlowBlockEnabled.IsNull() {
		body.UnnamedFlowBlockEnabled = nil
	}
	if config.UnnamedFlowExemptCIDRs.IsNull() {
		body.UnnamedFlowExemptCidrs = nil
	}
	if config.DohResolverHosts.IsNull() {
		body.DohResolverHosts = nil
	}
	if config.DohResolverIPs.IsNull() {
		body.DohResolverIps = nil
	}
}

// addEndpointSettingsWriteError gives the one 409 this upsert can produce its own
// message. addSDKError's conflict text is about a name or slug already in use,
// which is wrong here: the platform refuses a MOVE to `enforce` with 409 until
// the scope's fleet can enforce call-time rules.
//
// Only a move qualifies: the request must send `enforce` and the prior value must
// not already be `enforce` (a row already enforcing saves normally, so a 409 on it
// is something else). prior is null on Create, where the row may or may not exist.
func addEndpointSettingsWriteError(diags *diag.Diagnostics, op string, err error,
	sent adminapi.EndpointPolicySettingsWrite, prior types.String) {
	movesToEnforce := sent.HookRuleEnforcement != nil &&
		string(*sent.HookRuleEnforcement) == "enforce" && prior.ValueString() != "enforce"
	if errors.Is(err, adminapi.ErrConflict) && movesToEnforce {
		diags.AddError(
			fmt.Sprintf("%s — fleet not ready for hook_rule_enforcement = \"enforce\"", op),
			"The platform refuses to move this scope to `enforce` until every active device in it runs "+
				"an agent build that enforces call-time MCP rules. Check `hookRuleEnforcementCoverage` "+
				"on the posture fleet status in the admin console, update or retire the devices it "+
				"lists, then re-apply. Until then, keep `hook_rule_enforcement = \"shadow\"` to count "+
				"what the rules would deny.\n\nDetails: "+err.Error(),
		)
		return
	}
	addSDKError(diags, op, err)
}

// noZeroPrefix refuses a /0 CIDR in unnamed_flow_exempt_cidrs. The platform
// refuses it (it would exempt the whole address space, leaving the block
// reporting enabled while blocking nothing); this moves that to plan time.
type noZeroPrefix struct{}

func (noZeroPrefix) Description(context.Context) string {
	return "must not be a /0 prefix"
}

func (v noZeroPrefix) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (noZeroPrefix) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if strings.HasSuffix(strings.TrimSpace(req.ConfigValue.ValueString()), "/0") {
		resp.Diagnostics.AddAttributeError(req.Path, "A /0 prefix is refused",
			fmt.Sprintf("%q would exempt the whole address space, leaving the unnamed-flow block "+
				"reporting enabled while blocking nothing. Turn unnamed_flow_block_enabled off instead.",
				req.ConfigValue.ValueString()))
	}
}

// The platform normalizes three posture sets on write: DoH hosts are trimmed,
// lowercased and de-duplicated; DoH IPs and unnamed-flow CIDRs are
// canonicalized (CIDR host bits masked). State keeps the configured
// values, so normalization is not drift, and the platform's own forms from the
// last write or refresh live in private state under platformFormsKey.
//
// The normalization is not reimplemented: it rests on Java and Netty address
// formatting that Go's netip does not match in every case (an IPv4-mapped IPv6
// address, for one). Comparing the platform's value with the one it returned
// last time is exact.
const platformFormsKey = "platform_forms"

// platformForms is what the platform stored, as of the last write or refresh.
type platformForms struct {
	// Recorded is false for a zero value read from absent private state (an
	// import, or state from before this was recorded).
	Recorded bool `json:"recorded"`
	// Lists holds each normalized set, sorted; a nil entry is an absent set.
	Lists map[string][]string `json:"lists,omitempty"`
}

type privateStateGetter interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

type privateStateSetter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

func loadPlatformForms(ctx context.Context, p privateStateGetter) (platformForms, diag.Diagnostics) {
	if p == nil {
		return platformForms{}, nil
	}
	raw, diags := p.GetKey(ctx, platformFormsKey)
	if diags.HasError() || len(raw) == 0 {
		return platformForms{}, diags
	}
	var out platformForms
	if err := json.Unmarshal(raw, &out); err != nil {
		// Unreadable private state is not fatal: treat it as nothing recorded,
		// which takes the platform's values as-is, as an import does.
		return platformForms{}, diags
	}
	return out, diags
}

func storePlatformForms(ctx context.Context, p privateStateSetter, forms platformForms) diag.Diagnostics {
	if p == nil {
		return nil
	}
	raw, err := json.Marshal(forms)
	if err != nil {
		var d diag.Diagnostics
		d.AddError("Failed to record the platform's posture values", err.Error())
		return d
	}
	return p.SetKey(ctx, platformFormsKey, raw)
}

// reconcilePlatformForms decides each normalized value in m, which holds the
// platform's response, and returns what the platform stored.
//
//   - Sent in this write: the response must show it was stored. A non-empty
//     value that comes back empty, or a set that comes back larger than sent
//     (de-duplication only shrinks it), is an error: a platform build without
//     the field ignores it and would otherwise report success indefinitely.
//     Otherwise state takes the planned (configured) value.
//   - Not sent, and the platform's value equals the one recorded last time:
//     nothing changed out of band, so the prior (configured) value is kept.
//   - Otherwise (changed elsewhere, or nothing recorded, as after an import):
//     the platform's value, so a console edit shows as drift.
func reconcilePlatformForms(m *EndpointPolicySettingsResourceModel, prior EndpointPolicySettingsResourceModel,
	sent adminapi.EndpointPolicySettingsWrite, recorded platformForms) (platformForms, diag.Diagnostics) {
	var diags diag.Diagnostics
	forms := platformForms{Recorded: true, Lists: map[string][]string{}}
	sets := []struct {
		name  string
		got   *types.Set
		prior types.Set
		sent  *[]string
	}{
		{"unnamed_flow_exempt_cidrs", &m.UnnamedFlowExemptCIDRs, prior.UnnamedFlowExemptCIDRs, sent.UnnamedFlowExemptCidrs},
		{"doh_resolver_hosts", &m.DohResolverHosts, prior.DohResolverHosts, sent.DohResolverHosts},
		{"doh_resolver_ips", &m.DohResolverIPs, prior.DohResolverIPs, sent.DohResolverIps},
	}
	for _, f := range sets {
		server := setStrings(*f.got)
		forms.Lists[f.name] = server
		priorKnown := !f.prior.IsNull() && !f.prior.IsUnknown()
		switch {
		case f.sent != nil:
			// The input comes from a set and is already distinct; counted
			// through distinct anyway so the bound holds for any caller.
			sentCount := len(distinct(*f.sent))
			if sentCount > 0 && len(server) == 0 {
				diags.AddAttributeError(path.Root(f.name), fmt.Sprintf("The platform did not store %s", f.name),
					"The request sent it and the platform's response has none. A platform build that "+
						"predates this field ignores it; check the admin-api version.\n\n"+
						notStoredRecovery)
				continue
			}
			if len(server) > sentCount {
				diags.AddAttributeError(path.Root(f.name), fmt.Sprintf("The platform stored more %s than were sent", f.name),
					fmt.Sprintf("Sent %d, stored %d. Normalization only removes duplicates, so this is "+
						"unexpected; the stored value is %v.", sentCount, len(server), server))
				continue
			}
			if priorKnown {
				*f.got = f.prior
			}
		case priorKnown && recorded.Recorded:
			if last, ok := recorded.Lists[f.name]; ok && slices.Equal(last, server) {
				*f.got = f.prior
			}
		}
	}
	return forms, diags
}

// notStoredRecovery is appended to a "did not store" error. On Create the error
// leaves the resource tainted, and replacing a device-group row deletes the
// override (the group falls back to the tenant default) before recreating it,
// so re-applying unchanged would loop.
const notStoredRecovery = "The rest of the row was written. Before re-applying, remove the attribute " +
	"from the config or upgrade the platform: on a new device-group row Terraform now marks the " +
	"resource tainted, and replacing it deletes the override, so the group briefly falls back to " +
	"the tenant default."

// distinct returns vals de-duplicated, order kept.
func distinct(vals []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// setStrings returns a set's elements sorted, nil for a null, unknown or
// empty set.
func setStrings(s types.Set) []string {
	if s.IsNull() || s.IsUnknown() || len(s.Elements()) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.Elements()))
	for _, e := range s.Elements() {
		if v, ok := e.(types.String); ok {
			out = append(out, v.ValueString())
		}
	}
	slices.Sort(out)
	return out
}

// setToSDK converts a known set for the request: nil when null or unknown
// (not sent), and [] for a known empty set (the platform's clear).
func setToSDK(ctx context.Context, s types.Set) *[]string {
	if s.IsNull() || s.IsUnknown() {
		return nil
	}
	out := []string{}
	_ = s.ElementsAs(ctx, &out, false)
	return &out
}

// stringSliceToSetOrNull renders the platform's list as a set, null when it
// is absent or empty (the platform returns an empty list as absent).
func stringSliceToSetOrNull(p *[]string) types.Set {
	if p == nil || len(*p) == 0 {
		return types.SetNull(types.StringType)
	}
	elems := make([]attr.Value, 0, len(*p))
	for _, v := range *p {
		elems = append(elems, types.StringValue(v))
	}
	return types.SetValueMust(types.StringType, elems)
}
