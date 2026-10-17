package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
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
// to rows that already exist. GH #2127 moved `unapproved_mcp_action` server-side, so
// that attribute dropped its Default and now defers to the server via
// Computed + UseStateForUnknown (see the schema block). Prefer that shape for
// anything new; keep a mirrored Default only where an omitted value genuinely must
// resolve client-side.
//
// NOTE: this is NOT what Delete writes. Destroy deliberately leaves the
// tenant-default row enforcing whatever was last applied; see Delete.
const (
	postureDefaultDestinationAction = "allow"
)

// localMcpRiskActions is what an unvetted local MCP tool at one risk rating gets.
var localMcpRiskActions = []string{"allow", "ask_once", "ask_every_call", "deny"}

var localMcpRiskPolicyAttrTypes = map[string]attr.Type{
	"low":    types.StringType,
	"medium": types.StringType,
	"high":   types.StringType,
}

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

	UnapprovedMcpAction      types.String `tfsdk:"unapproved_mcp_action"`
	McpGatewayURL            types.String `tfsdk:"mcp_gateway_url"`
	DefaultDestinationAction types.String `tfsdk:"default_destination_action"`
	EchStripEnabled          types.Bool   `tfsdk:"ech_strip_enabled"`
	DohBlockEnabled          types.Bool   `tfsdk:"doh_block_enabled"`
	QuicBlockEnabled         types.Bool   `tfsdk:"quic_block_enabled"`

	// Server-defaulted and console-editable (platform#3011, GH #2986): Optional +
	// Computed + UseStateForUnknown, no Default, so omitting either keeps what the
	// tenant has. The upsert keeps any field the request leaves out.
	HookRuleEnforcement types.String `tfsdk:"hook_rule_enforcement"`
	LocalMcpRiskPolicy  types.Object `tfsdk:"local_mcp_risk_policy"`

	// More server-defaulted, console-editable posture fields, same shape as the
	// two above: Optional + Computed + UseStateForUnknown, no Default, sent only
	// when configured. The three lists are normalized by the platform; see
	// reconcileNormalizedList.
	UnapprovedAgentAction   types.String `tfsdk:"unapproved_agent_action"`
	LocalMcpBlockEnabled    types.Bool   `tfsdk:"local_mcp_block_enabled"`
	UnnamedFlowBlockEnabled types.Bool   `tfsdk:"unnamed_flow_block_enabled"`
	UnnamedFlowExemptCIDRs  types.List   `tfsdk:"unnamed_flow_exempt_cidrs"`
	DohResolverHosts        types.List   `tfsdk:"doh_resolver_hosts"`
	DohResolverIPs          types.List   `tfsdk:"doh_resolver_ips"`

	// JSON (jsonencode), not a typed attribute: the platform stores the whole
	// ApprovalConfig as sent, so a typed subset would erase whatever it did not
	// model. Reconciled through private state like the normalized lists.
	LocalMcpApprovalConfig types.String `tfsdk:"local_mcp_approval_config"`

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
		MarkdownDescription: "Endpoint posture for managed devices — the unapproved-MCP action, the MCP " +
			"gateway target, the DNS/QUIC network flags, the default action for AI destinations with " +
			"no matching `ferentin_endpoint_destination_rule`, call-time MCP rule enforcement, and the " +
			"tenant's policy for unvetted local MCP tools.\n\n" +
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

			"unapproved_mcp_action": schema.StringAttribute{
				MarkdownDescription: "What the agent does with an MCP server config it finds on a device " +
					"that is not approved:\n\n" +
					"* `report_only` — report it in telemetry, change nothing on the machine\n" +
					"* `quarantine` — move the offending config aside so the client cannot load it " +
					"(default). The entry is preserved verbatim in a root-owned sidecar before " +
					"removal, and is not removed if it cannot be preserved\n" +
					"* `block` — block the server's traffic outright (requires the NetworkExtension " +
					"content filter; without it the agent can report but not enforce)",
				// NO schema Default, deliberately — unlike its sibling below.
				//
				// A `Default` on an Optional+Computed attribute is a silent data
				// migration: a config that omits the field plans to the constant and
				// APPLIES it. When platform#2127 moved the server default to
				// "quarantine", that turned every IaC-managed tenant with an existing
				// row into a plan of `report_only -> quarantine`, applied on the next
				// run — precisely the migration changeset 1248 refused to perform,
				// because an implicit `report_only` and a deliberate one are the same
				// bytes and there is no way to tell them apart.
				//
				// Computed + UseStateForUnknown instead: omitting the attribute keeps
				// whatever the tenant already has, and a brand-new row gets whatever
				// default the SERVER picks. That is also the better shape generally —
				// mirroring a server-side default into a provider constant is what
				// created the drift in the first place, and this attribute no longer
				// has a second definition to fall out of step with.
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.OneOf("report_only", "quarantine", "block")},
			},
			"mcp_gateway_url": schema.StringAttribute{
				MarkdownDescription: "Tenant MCP gateway base URL that approved server configs are " +
					"rewritten to point at. Unset = the agent reports what it finds and rewrites nothing.\n\n" +
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
				// Computed + UseStateForUnknown, no Default — see unapproved_mcp_action.
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
			"unnamed_flow_exempt_cidrs": schema.ListAttribute{
				MarkdownDescription: "Addresses or CIDRs exempt from the unnamed-flow block, for infrastructure " +
					"reached by bare IP on 443 (VPN concentrators, monitoring). At most 64; a `/0` prefix is " +
					"refused. The platform canonicalizes them (host bits masked, duplicates collapsed); your " +
					"spelling is kept here. Omit to keep the current list; `[]` clears it.",
				Optional:      true,
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
				Validators: []validator.List{
					listvalidator.SizeAtMost(64),
					listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1), noZeroPrefix{}),
				},
			},
			"doh_resolver_hosts": schema.ListAttribute{
				MarkdownDescription: "DoH resolver hostnames the DoH block matches; `*.` and leading-`.` suffix " +
					"patterns are allowed. At most 256. Lowercased by the platform; your spelling is kept " +
					"here. Omit to keep the current list; `[]` clears it, returning the agent to its built-in " +
					"list.\n\n" +
					"~> Authoring either DoH list requires the fleet on agent 0.9 or later: older agents " +
					"refuse a posture section with unknown keys and keep their last-known-good policy.",
				Optional:      true,
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
				Validators: []validator.List{
					listvalidator.SizeAtMost(256),
					listvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 253)),
				},
			},
			"doh_resolver_ips": schema.ListAttribute{
				MarkdownDescription: "DoH bootstrap IP addresses the DoH block matches, on port 443 only. At most " +
					"256. Canonicalized by the platform; your spelling is kept here. Omit to keep the " +
					"current list; `[]` clears it. Same agent requirement as `doh_resolver_hosts`.",
				Optional:      true,
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
				Validators: []validator.List{
					listvalidator.SizeAtMost(256),
					listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
				},
			},
			"local_mcp_approval_config": schema.StringAttribute{
				MarkdownDescription: "Approval routing for step-up approvals raised by unvetted local MCP " +
					"tools, as JSON — write it with `jsonencode({...})`. The object is the platform's " +
					"approval config: `step_up_mode` (`reauth` / `approve`), `channels` (at least one; " +
					"`type` is `invocation_context`, `slack`, `email`, `webhook` or `approver_dm`), and " +
					"optionally `approver_role`, `timeout_seconds`, `escalation`, `context_fields`, " +
					"`redacted_fields` and the risk bounds. Any `ask_*` tier in `local_mcp_risk_policy` " +
					"needs this set.\n\n" +
					"A key this provider version does not know fails at plan rather than being dropped " +
					"(the platform ignores unknown keys). The platform stores the whole object as sent, " +
					"so everything it should hold belongs in this value. Omit to keep the current " +
					"config. Clearing it is done in the admin console: `{}` is refused here, as is " +
					"removing it while an `ask_*` tier still needs it (the platform refuses that too).",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{approvalConfigJSON{}},
			},
			// An ObjectAttribute, not a SingleNestedAttribute. An Optional+Computed
			// NESTED attribute left out of config does not propose its prior value,
			// so the framework sees a change and marks every computed attribute
			// (version, updated_at, provenance) unknown on every plan: a permanent
			// no-op update, found running the acceptance suite. A plain object
			// attribute keeps its prior value like any Optional+Computed attribute.
			// The tier rules the nested schema expressed live in
			// localMcpRiskPolicyTiers instead.
			"local_mcp_risk_policy": schema.ObjectAttribute{
				MarkdownDescription: "What an unvetted local MCP tool gets at each risk rating: an object " +
					"with `low`, `medium` and `high`, each one of `allow`, `ask_once`, `ask_every_call` or " +
					"`deny`. All three are required. Unset on the platform means the legacy behaviour (step " +
					"up when approval routing exists, deny otherwise).\n\n" +
					"**Tenant default only**: the decision is read from the tenant-default row alone, so the " +
					"platform refuses it on a device-group override, and this fails at plan when " +
					"`device_group_id` is set.\n\n" +
					"~> Any `ask_*` tier needs approval routing (`localMcpApprovalConfig` with at least one " +
					"channel), which this resource does not manage; set it up in the admin console first or " +
					"the platform rejects the write.\n\n" +
					"Removing this attribute from config leaves the stored policy in place. Clearing it is " +
					"done in the admin console.",
				Optional:       true,
				Computed:       true,
				AttributeTypes: localMcpRiskPolicyAttrTypes,
				PlanModifiers:  []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
				Validators: []validator.Object{
					objectvalidator.ConflictsWith(path.MatchRoot("device_group_id")),
					localMcpRiskPolicyTiers{},
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
				Computed: true,
			},
			"managed_by_client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth2 `client_id` of the creator, from the authenticated principal.",
				Computed:            true,
			},
			"managed_by_module": schema.StringAttribute{
				MarkdownDescription: "Module label this provider sent via `X-Ferentin-Managed-By-Module`.",
				Computed:            true,
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
	body := plan.toWrite()
	row, err := r.upsert(ctx, tenantID, plan.DeviceGroupID, "", body)
	if err != nil {
		addEndpointSettingsWriteError(&resp.Diagnostics, "Failed to write endpoint policy settings", err,
			body, types.StringNull())
		return
	}
	state := endpointSettingsToModel(tenantID, row)
	stored := reconcileNormalizedLists(&state, plan, body, nil)
	resp.Diagnostics.Append(storeNormalizedLists(ctx, resp.Private, stored)...)
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
	prior, d := loadNormalizedLists(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	stored := reconcileNormalizedLists(&refreshed, state, adminapi.EndpointPolicySettingsWrite{}, prior)
	resp.Diagnostics.Append(storeNormalizedLists(ctx, resp.Private, stored)...)
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

	body := plan.toWrite()
	omitUnconfigured(&body, config)
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	row, err := r.upsert(ctx, tenantID, state.DeviceGroupID, version, body)
	if err != nil {
		addEndpointSettingsWriteError(&resp.Diagnostics, "Failed to update endpoint policy settings", err,
			body, state.HookRuleEnforcement)
		return
	}
	refreshed := endpointSettingsToModel(tenantID, row)
	prior, d := loadNormalizedLists(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	// A list the request sent takes the planned (configured) value; one it did
	// not send reconciles against state like a refresh. plan carries state for
	// an omitted attribute (UseStateForUnknown), so it serves as the prior too.
	stored := reconcileNormalizedLists(&refreshed, plan, body, prior)
	resp.Diagnostics.Append(storeNormalizedLists(ctx, resp.Private, stored)...)
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
		version := strconv.FormatInt(state.Version.ValueInt64(), 10)
		err := r.sdk.EndpointPolicies().DeleteGroupSettings(ctx, tenantID,
			state.DeviceGroupID.ValueString(), version)
		if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
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
			"(unapproved_mcp_action = %q, default_destination_action = %q).\n\n"+
			"This is deliberate: resetting to the permissive defaults on destroy would silently "+
			"de-enforce the whole fleet, and removing a resource block or dropping a module reaches "+
			"this path just as `terraform destroy` does.\n\n"+
			"To actually stand enforcement down, do it explicitly and then destroy:\n"+
			"  1. Set unapproved_mcp_action = \"report_only\", default_destination_action = \"allow\", "+
			"hook_rule_enforcement = \"shadow\", clear mcp_gateway_url, and turn the network flags, "+
			"local_mcp_block_enabled and unnamed_flow_block_enabled off.\n"+
			"  2. terraform apply\n"+
			"  3. terraform destroy (or remove the resource block)",
			state.UnapprovedMcpAction.ValueString(), state.DefaultDestinationAction.ValueString()))
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

func (m *EndpointPolicySettingsResourceModel) toWrite() adminapi.EndpointPolicySettingsWrite {
	body := adminapi.EndpointPolicySettingsWrite{}
	setStringPtr(m.UnapprovedMcpAction, &body.UnapprovedMcpAction)
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
	body.LocalMcpRiskPolicy = localMcpRiskPolicyToSDK(m.LocalMcpRiskPolicy)
	setStringPtr(m.UnapprovedAgentAction, &body.UnapprovedAgentAction)
	setBoolPtr(m.LocalMcpBlockEnabled, &body.LocalMcpBlockEnabled)
	setBoolPtr(m.UnnamedFlowBlockEnabled, &body.UnnamedFlowBlockEnabled)
	// setStringListPtr sends a known empty list as [] (the platform's clear).
	setStringListPtr(context.Background(), m.UnnamedFlowExemptCIDRs, &body.UnnamedFlowExemptCidrs)
	setStringListPtr(context.Background(), m.DohResolverHosts, &body.DohResolverHosts)
	setStringListPtr(context.Background(), m.DohResolverIPs, &body.DohResolverIps)
	if !m.LocalMcpApprovalConfig.IsNull() && !m.LocalMcpApprovalConfig.IsUnknown() {
		// The validator already proved it decodes; a failure here sends nothing
		// rather than a half-read config the platform would store whole.
		if cfg, err := decodeApprovalConfig(m.LocalMcpApprovalConfig.ValueString()); err == nil {
			body.LocalMcpApprovalConfig = cfg
		}
	}

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
	return body
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
	m.UnapprovedMcpAction = strPtrToTF(row.UnapprovedMcpAction)
	m.McpGatewayURL = strPtrToTF(row.McpGatewayUrl)
	m.DefaultDestinationAction = strPtrToTF(row.DefaultDestinationAction)
	m.EchStripEnabled = boolPtrOrDefault(row.EchStripEnabled)
	m.DohBlockEnabled = boolPtrOrDefault(row.DohBlockEnabled)
	m.QuicBlockEnabled = boolPtrOrDefault(row.QuicBlockEnabled)
	m.HookRuleEnforcement = enumPtrToTF(row.HookRuleEnforcement)
	m.LocalMcpRiskPolicy = localMcpRiskPolicyToTF(row.LocalMcpRiskPolicy)
	m.UnapprovedAgentAction = strPtrToTF(row.UnapprovedAgentAction)
	m.LocalMcpBlockEnabled = boolPtrOrDefault(row.LocalMcpBlockEnabled)
	m.UnnamedFlowBlockEnabled = boolPtrOrDefault(row.UnnamedFlowBlockEnabled)
	m.UnnamedFlowExemptCIDRs = stringSliceToList(row.UnnamedFlowExemptCidrs)
	m.DohResolverHosts = stringSliceToList(row.DohResolverHosts)
	m.DohResolverIPs = stringSliceToList(row.DohResolverIps)
	m.LocalMcpApprovalConfig = approvalConfigToTF(row.LocalMcpApprovalConfig)
	m.CreatedAt = timePtrToTF(row.CreatedAt)
	m.UpdatedAt = timePtrToTF(row.UpdatedAt)

	m.Version = int64PtrToTF(row.Version)
	m.ManagedBy = enumPtrToTF(row.ManagedBy)
	m.ManagedByClientID = strPtrToTF(row.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(row.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(row.LastModifiedBy)
	return m
}

// localMcpRiskPolicyToSDK sends the policy only when it is configured. All three
// tiers are Required in the schema, so a known object always carries all three.
func localMcpRiskPolicyToSDK(o types.Object) *gen.LocalMcpRiskPolicy {
	if o.IsNull() || o.IsUnknown() {
		return nil
	}
	tier := func(name string) string {
		v, ok := o.Attributes()[name].(types.String)
		if !ok {
			return ""
		}
		return v.ValueString()
	}
	low := gen.LocalMcpRiskPolicyLow(tier("low"))
	medium := gen.LocalMcpRiskPolicyMedium(tier("medium"))
	high := gen.LocalMcpRiskPolicyHigh(tier("high"))
	return &gen.LocalMcpRiskPolicy{Low: &low, Medium: &medium, High: &high}
}

// localMcpRiskPolicyToTF reads an absent policy, or one with no tier set, as null:
// that is what the platform stores for "no policy" (it maps {} to null). A stored
// policy always has all three tiers, because the platform refuses a write that
// sends fewer, so a partially set object does not arise.
func localMcpRiskPolicyToTF(p *gen.LocalMcpRiskPolicy) types.Object {
	if p == nil || (p.Low == nil && p.Medium == nil && p.High == nil) {
		return types.ObjectNull(localMcpRiskPolicyAttrTypes)
	}
	return types.ObjectValueMust(localMcpRiskPolicyAttrTypes, map[string]attr.Value{
		"low":    enumPtrToTF(p.Low),
		"medium": enumPtrToTF(p.Medium),
		"high":   enumPtrToTF(p.High),
	})
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
	if config.LocalMcpRiskPolicy.IsNull() {
		body.LocalMcpRiskPolicy = nil
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
	if config.LocalMcpApprovalConfig.IsNull() {
		body.LocalMcpApprovalConfig = nil
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

// localMcpRiskPolicyTiers requires all three tiers of local_mcp_risk_policy and
// checks each against localMcpRiskActions — what Required + OneOf expressed when
// the attribute was a nested schema. The platform refuses a policy with fewer
// tiers (400), so this moves that to plan time.
type localMcpRiskPolicyTiers struct{}

func (localMcpRiskPolicyTiers) Description(context.Context) string {
	return "low, medium and high are each required and one of " + strings.Join(localMcpRiskActions, ", ")
}

func (v localMcpRiskPolicyTiers) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (localMcpRiskPolicyTiers) ValidateObject(_ context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	attrs := req.ConfigValue.Attributes()
	for _, tier := range []string{"low", "medium", "high"} {
		v, ok := attrs[tier].(types.String)
		if !ok || v.IsUnknown() {
			continue
		}
		if v.IsNull() {
			resp.Diagnostics.AddAttributeError(req.Path.AtName(tier),
				fmt.Sprintf("local_mcp_risk_policy.%s is required", tier),
				"Set all three tiers (low, medium, high); the platform refuses a policy with fewer.")
			continue
		}
		if !slices.Contains(localMcpRiskActions, v.ValueString()) {
			resp.Diagnostics.AddAttributeError(req.Path.AtName(tier),
				fmt.Sprintf("local_mcp_risk_policy.%s must be one of %s", tier, strings.Join(localMcpRiskActions, ", ")),
				fmt.Sprintf("Got %q.", v.ValueString()))
		}
	}
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

// The platform normalizes three posture lists on write: DoH hosts are
// trimmed, lowercased and de-duplicated; DoH IPs and unnamed-flow CIDRs are
// canonicalized (CIDR host bits masked). State keeps the configured spelling,
// so normalization is not drift, and the platform's normalized form from the
// last write or refresh lives in private state under normalizedListsKey.
//
// The normalization itself is not reimplemented: it rests on Java and Netty
// address formatting that Go's netip does not match in every case (an
// IPv4-mapped IPv6 address, for one). Comparing the platform's list with the
// one it returned last time is exact.
const normalizedListsKey = "normalized_posture_lists"

// normalizedLists maps attribute name to the platform's normalized list. A nil
// entry is an absent (empty) list.
type normalizedLists map[string][]string

type privateStateGetter interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

type privateStateSetter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

func loadNormalizedLists(ctx context.Context, p privateStateGetter) (normalizedLists, diag.Diagnostics) {
	if p == nil {
		return nil, nil
	}
	raw, diags := p.GetKey(ctx, normalizedListsKey)
	if diags.HasError() || len(raw) == 0 {
		return nil, diags
	}
	var out normalizedLists
	if err := json.Unmarshal(raw, &out); err != nil {
		// Unreadable private state is not fatal: treat it as absent, which
		// takes the platform's lists as-is, as an import does.
		return nil, diags
	}
	return out, diags
}

func storeNormalizedLists(ctx context.Context, p privateStateSetter, stored normalizedLists) diag.Diagnostics {
	if p == nil {
		return nil
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		var d diag.Diagnostics
		d.AddError("Failed to record normalized posture lists", err.Error())
		return d
	}
	return p.SetKey(ctx, normalizedListsKey, raw)
}

// reconcileNormalizedLists decides each normalized list in m, which holds the
// platform's response, and returns the normalized forms to store.
//
//   - Sent in this write: the platform accepted it, so state takes the planned
//     (configured) value.
//   - Not sent, and the platform's list equals the one recorded last time:
//     nothing changed out of band, so the prior (configured) value is kept.
//   - Otherwise (changed elsewhere, or nothing recorded, as after an import):
//     the platform's list, so a console edit shows as drift.
func reconcileNormalizedLists(m *EndpointPolicySettingsResourceModel, prior EndpointPolicySettingsResourceModel,
	sent adminapi.EndpointPolicySettingsWrite, recorded normalizedLists) normalizedLists {
	stored := normalizedLists{}
	fields := []struct {
		name  string
		got   *types.List
		prior types.List
		sent  bool
	}{
		{"unnamed_flow_exempt_cidrs", &m.UnnamedFlowExemptCIDRs, prior.UnnamedFlowExemptCIDRs, sent.UnnamedFlowExemptCidrs != nil},
		{"doh_resolver_hosts", &m.DohResolverHosts, prior.DohResolverHosts, sent.DohResolverHosts != nil},
		{"doh_resolver_ips", &m.DohResolverIPs, prior.DohResolverIPs, sent.DohResolverIps != nil},
	}
	for _, f := range fields {
		server := listStrings(*f.got)
		stored[f.name] = server
		priorKnown := !f.prior.IsNull() && !f.prior.IsUnknown()
		switch {
		case f.sent && priorKnown:
			*f.got = f.prior
		case !f.sent && priorKnown && recorded != nil:
			if last, ok := recorded[f.name]; ok && slices.Equal(last, server) {
				*f.got = f.prior
			}
		}
	}
	reconcileApprovalConfig(m, prior, sent, recorded, stored)
	return stored
}

// reconcileApprovalConfig applies the same rule as the lists to
// local_mcp_approval_config, recording the platform's normalized JSON under
// approvalConfigKey: the configured JSON is kept when it was sent, or when the
// platform's stored config is unchanged since last time.
func reconcileApprovalConfig(m *EndpointPolicySettingsResourceModel, prior EndpointPolicySettingsResourceModel,
	sent adminapi.EndpointPolicySettingsWrite, recorded, stored normalizedLists) {
	server := m.LocalMcpApprovalConfig
	if !server.IsNull() {
		stored[approvalConfigKey] = []string{server.ValueString()}
	} else {
		stored[approvalConfigKey] = nil
	}
	priorKnown := !prior.LocalMcpApprovalConfig.IsNull() && !prior.LocalMcpApprovalConfig.IsUnknown()
	switch {
	case sent.LocalMcpApprovalConfig != nil && priorKnown:
		m.LocalMcpApprovalConfig = prior.LocalMcpApprovalConfig
	case sent.LocalMcpApprovalConfig == nil && priorKnown && recorded != nil:
		if last, ok := recorded[approvalConfigKey]; ok && slices.Equal(last, stored[approvalConfigKey]) {
			m.LocalMcpApprovalConfig = prior.LocalMcpApprovalConfig
		}
	}
}

// approvalConfigKey is local_mcp_approval_config's entry in normalizedLists:
// a one-element list holding the platform's config as normalized JSON.
const approvalConfigKey = "local_mcp_approval_config"

// decodeApprovalConfig parses the attribute's JSON strictly into the SDK's
// ApprovalConfig: an unknown key (a typo, or a field newer than this
// provider's SDK) is an error, because the platform would silently drop it.
func decodeApprovalConfig(raw string) (*gen.ApprovalConfig, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg gen.ApprovalConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("unexpected data after the JSON object")
	}
	return &cfg, nil
}

// approvalConfigToTF renders the platform's config as normalized JSON: keys
// sorted, compact, as jsonencode writes it, so a config written with
// jsonencode and read back (an import) compares equal when nothing differs.
func approvalConfigToTF(cfg *gen.ApprovalConfig) types.String {
	if cfg == nil {
		return types.StringNull()
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return types.StringNull()
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return types.StringNull()
	}
	normalized, err := json.Marshal(generic)
	if err != nil {
		return types.StringNull()
	}
	return types.StringValue(string(normalized))
}

// approvalConfigJSON validates local_mcp_approval_config at plan time: a JSON
// object that decodes strictly into the SDK's ApprovalConfig, with a
// step_up_mode and at least one channel (the platform's rules), and not {}
// (the platform's clear, which the SDK cannot send; clearing is a console
// action).
type approvalConfigJSON struct{}

func (approvalConfigJSON) Description(context.Context) string {
	return "a JSON approval config with step_up_mode and at least one channel"
}

func (v approvalConfigJSON) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (approvalConfigJSON) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	raw := strings.TrimSpace(req.ConfigValue.ValueString())
	var generic map[string]any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "local_mcp_approval_config is not a JSON object",
			"Write it with jsonencode({...}). "+err.Error())
		return
	}
	if len(generic) == 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "An empty approval config is refused",
			"{} is the platform's way to clear approval routing, which this provider cannot send. "+
				"Clear it in the admin console, or leave the attribute out to keep the current config.")
		return
	}
	cfg, err := decodeApprovalConfig(raw)
	if err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "local_mcp_approval_config does not match the approval config",
			err.Error()+"\n\nAn unknown key fails here rather than being dropped: the platform ignores "+
				"keys it does not know.")
		return
	}
	if cfg.StepUpMode == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "local_mcp_approval_config.step_up_mode is required",
			"Set step_up_mode to \"approve\" or \"reauth\".")
	} else if cfg.StepUpMode != "approve" && cfg.StepUpMode != "reauth" {
		resp.Diagnostics.AddAttributeError(req.Path, "local_mcp_approval_config.step_up_mode is invalid",
			fmt.Sprintf("Got %q; use \"approve\" or \"reauth\".", cfg.StepUpMode))
	}
	if cfg.Channels == nil || len(*cfg.Channels) == 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "local_mcp_approval_config needs at least one channel",
			"The platform refuses an approval config without channels: it would read as set up while "+
				"every unvetted local tool was denied.")
	}
}

// listStrings returns a list's elements, nil for a null, unknown or empty list.
func listStrings(l types.List) []string {
	if l.IsNull() || l.IsUnknown() || len(l.Elements()) == 0 {
		return nil
	}
	out := make([]string, 0, len(l.Elements()))
	for _, e := range l.Elements() {
		if s, ok := e.(types.String); ok {
			out = append(out, s.ValueString())
		}
	}
	return out
}
