package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// Match kinds, grouped as the platform groups them (shared-core EndpointMcpRule).
// The groups drive the cross-field rules in ValidateConfig.
var (
	mcpRuleRemoteKinds   = []string{"host", "url_prefix"}
	mcpRuleLocalKinds    = []string{"package", "argv_contains"}
	mcpRuleCallTimeKinds = []string{"tool_name", "command_binary"}
	mcpRuleBehaviorKinds = []string{"tool_behavior"}
	mcpRuleAllKinds      = []string{"host", "url_prefix", "package", "argv_contains",
		"tool_name", "command_binary", "tool_behavior"}

	// mcpRuleToolBehaviors is the closed vocabulary of a tool_behavior rule's
	// match_values (EndpointToolBehavior.ALL).
	mcpRuleToolBehaviors = []string{"shell", "file_read", "file_write"}
)

const (
	// Platform limits (EndpointMcpRuleAdminService): the agent refuses a longer
	// list and pins the whole bundle to last-known-good.
	mcpRuleMaxMatchValues  = 64
	mcpRuleMaxAppBundleIDs = 64
)

// EndpointMCPRuleResource is the `ferentin_endpoint_mcp_rule` Terraform
// resource — allow / block / steer for local and remote MCP servers and tool
// calls on managed devices (platform#2166).
//
// Same shape as ferentin_endpoint_destination_rule: ordinary CRUD, first match
// wins by ascending priority, version + provenance for If-Match and drift.
// Call-time `block` rules are enforced or only counted according to the
// posture's hook_rule_enforcement (ferentin_endpoint_policy_settings).
//
// Two platform behaviours shape the model:
//
//   - The PUT replaces description, steer_to_url, app_bundle_ids,
//     device_group_ids and criteria, but KEEPS priority, enabled and
//     criteria_combinator when omitted. All three carry a schema Default, so
//     they are always sent and removing one from config converges.
//   - match_values on an allow or steer rule are canonicalized on write (block
//     values are stored verbatim). State keeps the configured spelling in
//     match_values and the server's in stored_match_values; see
//     reconcileMatchValues.
type EndpointMCPRuleResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type EndpointMCPRuleResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Priority    types.Int64  `tfsdk:"priority"`
	Enabled     types.Bool   `tfsdk:"enabled"`

	MatchKind         types.String `tfsdk:"match_kind"`
	MatchValues       types.List   `tfsdk:"match_values"`
	StoredMatchValues types.List   `tfsdk:"stored_match_values"`

	Action     types.String `tfsdk:"action"`
	SteerToURL types.String `tfsdk:"steer_to_url"`

	AppBundleIDs       types.List            `tfsdk:"app_bundle_ids"`
	DeviceGroupIDs     types.List            `tfsdk:"device_group_ids"`
	CriteriaCombinator types.String          `tfsdk:"criteria_combinator"`
	Criteria           []PolicyCriteriaModel `tfsdk:"criteria"`

	Enforcement       types.String `tfsdk:"enforcement"`
	EnforcementReason types.String `tfsdk:"enforcement_reason"`

	RuleID    types.String `tfsdk:"rule_id"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`

	Version           types.Int64  `tfsdk:"version"` // for If-Match
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

func NewEndpointMCPRuleResource() resource.Resource {
	return &EndpointMCPRuleResource{}
}

var (
	_ resource.Resource                   = (*EndpointMCPRuleResource)(nil)
	_ resource.ResourceWithConfigure      = (*EndpointMCPRuleResource)(nil)
	_ resource.ResourceWithImportState    = (*EndpointMCPRuleResource)(nil)
	_ resource.ResourceWithValidateConfig = (*EndpointMCPRuleResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*EndpointMCPRuleResource)(nil)
)

func (r *EndpointMCPRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_endpoint_mcp_rule"
}

func (r *EndpointMCPRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *EndpointMCPRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Endpoint MCP rule — allow / block / steer for MCP servers and tool calls on " +
			"managed devices. Rules are evaluated on-device, **first match wins** by ascending `priority`.\n\n" +
			"What a rule matches depends on `match_kind`:\n\n" +
			"* **remote MCP servers** — `host`, `url_prefix`\n" +
			"* **local (stdio) MCP servers** — `package`, `argv_contains`\n" +
			"* **tool calls**, decided at call time — `tool_name`, `command_binary`\n" +
			"* **tool behaviours** — `tool_behavior` (`shell`, `file_read`, `file_write`), expanded into " +
			"call-time rules for every AI tool that can express them\n\n" +
			"Call-time and behaviour rules support `block` only. Whether a call-time `block` denies the " +
			"call or only counts it is set per scope by `hook_rule_enforcement` on " +
			"`ferentin_endpoint_policy_settings`.\n\n" +
			"## Import\n\n" +
			"```\n" +
			"terraform import ferentin_endpoint_mcp_rule.example <tenant_id>/<rule_id>\n" +
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
			"name": schema.StringAttribute{
				MarkdownDescription: "Rule name, unique within the tenant. 1-255 characters.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"description": schema.StringAttribute{
				Optional: true, Computed: true,
				Validators: []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"priority": schema.Int64Attribute{
				MarkdownDescription: "Evaluation order — 1 is highest, first match wins. Default `100`.\n\n" +
					"~> **Ownership warning.** Ordering is the policy here, so do not split ownership of " +
					"`priority` between Terraform and the console: the console's reorder arrows swap two " +
					"rules' priorities, and Terraform would then fight it on every plan.",
				// A Default, not Computed-only: the platform KEEPS priority when a PUT
				// omits it, so sending it every time is what makes removing it from
				// config return the rule to 100.
				Optional: true, Computed: true,
				Default:    int64default.StaticInt64(100),
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the rule is evaluated. Default `true`.",
				Optional:            true, Computed: true,
				Default: booldefault.StaticBool(true),
			},

			"match_kind": schema.StringAttribute{
				MarkdownDescription: "What `match_values` name: `host`, `url_prefix` (remote MCP servers), " +
					"`package`, `argv_contains` (local MCP servers), `tool_name`, `command_binary` (tool " +
					"calls), or `tool_behavior`.",
				Required:   true,
				Validators: []validator.String{stringvalidator.OneOf(mcpRuleAllKinds...)},
			},
			"match_values": schema.ListAttribute{
				MarkdownDescription: "Values to match, interpreted per `match_kind`. 1-64 entries.\n\n" +
					"* `host` — bare hostnames (no scheme, port or path)\n" +
					"* `package` — one registry-qualified id per entry\n" +
					"* `tool_behavior` — `shell`, `file_read` or `file_write`\n\n" +
					"On an `allow` or `steer` rule the platform canonicalizes each value; `block` values " +
					"are stored as written. Terraform keeps your spelling here and the platform's in " +
					"`stored_match_values`, so canonicalization does not show as drift.",
				Required:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					listvalidator.SizeBetween(1, mcpRuleMaxMatchValues),
					listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
				},
			},
			"stored_match_values": schema.ListAttribute{
				MarkdownDescription: "Read-only. `match_values` as the platform stores them (canonicalized " +
					"on an `allow` or `steer` rule). A console edit shows up as a change here and in " +
					"`match_values` on the next plan.",
				Computed:    true,
				ElementType: types.StringType,
			},

			"action": schema.StringAttribute{
				MarkdownDescription: "`allow`, `block` or `steer`. Call-time kinds (`tool_name`, " +
					"`command_binary`) and `tool_behavior` support `block` only.",
				Required:   true,
				Validators: []validator.String{stringvalidator.OneOf("allow", "block", "steer")},
			},
			"steer_to_url": schema.StringAttribute{
				MarkdownDescription: "Gateway route a remote `steer` rule sends the server to: this tenant's " +
					"MCP gateway URL ending in the route of an enabled MCP server. **Required on a `steer` " +
					"rule with `match_kind` `host` or `url_prefix`; refused on any other rule.** Must be " +
					"`https://`. A local steer is the shim wrap and has no target.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(httpsOnlyURL,
						"must be an https:// URL — http:// would downgrade steered MCP sessions to cleartext"),
				},
			},

			"app_bundle_ids": schema.ListAttribute{
				MarkdownDescription: "Which MCP **client** the rule governs (Claude Desktop, Cursor, …) by " +
					"macOS bundle identifier, e.g. `com.anthropic.claudefordesktop`. Never which server. " +
					"Empty or unset = any client. At most 64.",
				Optional: true, Computed: true, ElementType: types.StringType,
				Validators: []validator.List{listvalidator.SizeAtMost(mcpRuleMaxAppBundleIDs)},
			},
			"device_group_ids": schema.ListAttribute{
				MarkdownDescription: "Device groups this rule applies to — reference " +
					"`ferentin_device_group.<name>.group_id`. Empty or unset = **every device in the " +
					"tenant**.\n\n" +
					"~> Refused on a local `allow` rule (`package` / `argv_contains`): devices do not yet " +
					"carry an authenticated identity when a local server is admitted, so the narrowing " +
					"would be ignored and the allow would apply tenant-wide. Narrowing a `block` fails " +
					"safe and is accepted.",
				Optional: true, Computed: true, ElementType: types.StringType,
			},
			"criteria_combinator": schema.StringAttribute{
				MarkdownDescription: "How `criteria` groups combine: `AND` or `OR`. Default `AND`.",
				// A Default for the same reason as priority: the platform keeps the
				// stored value when a PUT omits it.
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("AND"),
				Validators: []validator.String{stringvalidator.OneOf("AND", "OR")},
			},
			"criteria": criteriaSchemaAttribute(criteriaSchemaOptions{
				Description: "User/department ABAC criteria that **narrow** the rule to a population. " +
					"Groups combine via `criteria_combinator`; no criteria means the rule applies to " +
					"every user it otherwise targets.\n\n" +
					"~> **Removing criteria widens the rule**, and the provider warns at plan when an " +
					"apply would. Criteria are refused on a local `allow` rule, for the same reason as " +
					"`device_group_ids`.",
				TypeDescription: "Criteria type. `claims` (the default) matches the signed-in user's " +
					"identity claims. Stored opaquely by the platform, so nothing is defaulted " +
					"server-side.",
				TypeDefault:      "claims",
				TypeOneOf:        []string{"claims", "context", "request", "time"},
				FieldDescription: "Field path to evaluate (e.g. `department`, `email`, `groups`).",
				ValueExample:     "`jsonencode(\"legal\")`",
			}),

			"enforcement": schema.StringAttribute{
				MarkdownDescription: "Read-only. Whether the fleet enforces this rule: `active`, `pending`, " +
					"`not_enforced` or `never_matched`. Computed by the platform from device reports, so " +
					"it can change without an apply.",
				Computed: true,
			},
			"enforcement_reason": schema.StringAttribute{
				MarkdownDescription: "Read-only. Why `enforcement` is what it is, when it is not `active`.",
				Computed:            true,
			},

			"rule_id": schema.StringAttribute{
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

// ValidateConfig moves the platform's cross-field rules to plan time, kept in
// lockstep with EndpointMcpRuleAdminService's validation: if the server relaxes
// a rule, relax it here. Unknown values are skipped.
//
// Attributes are read individually, not through the model, because criteria is
// a Go slice there and cannot hold an unknown (see the destination-rule note).
func (r *EndpointMCPRuleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var matchKind, action, steerToURL types.String
	var matchValues, deviceGroupIDs, criteria types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("match_kind"), &matchKind)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("action"), &action)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("steer_to_url"), &steerToURL)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("match_values"), &matchValues)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("device_group_ids"), &deviceGroupIDs)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("criteria"), &criteria)...)
	if resp.Diagnostics.HasError() || matchKind.IsUnknown() || matchKind.IsNull() {
		return
	}
	kind := matchKind.ValueString()
	act := action.ValueString()
	actionKnown := !action.IsUnknown() && !action.IsNull()

	if actionKnown && act != "block" && (contains(mcpRuleCallTimeKinds, kind) || contains(mcpRuleBehaviorKinds, kind)) {
		resp.Diagnostics.AddAttributeError(path.Root("action"),
			fmt.Sprintf("A %q rule supports action \"block\" only", kind),
			"Call-time and behaviour rules can only block: `allow` on a call-time kind is pending "+
				"review on the platform, and `steer` has no call-time meaning.")
	}

	if kind == "tool_behavior" && !matchValues.IsUnknown() && !matchValues.IsNull() {
		for _, v := range matchValues.Elements() {
			s, ok := v.(types.String)
			if !ok || s.IsUnknown() || s.IsNull() {
				continue
			}
			if !contains(mcpRuleToolBehaviors, s.ValueString()) {
				resp.Diagnostics.AddAttributeError(path.Root("match_values"),
					fmt.Sprintf("%q is not a known tool behaviour", s.ValueString()),
					"A tool_behavior rule matches one of: "+strings.Join(mcpRuleToolBehaviors, ", ")+".")
			}
		}
	}

	hasSteerTarget := !steerToURL.IsUnknown() && !isUnsetString(steerToURL)
	remote := contains(mcpRuleRemoteKinds, kind)
	if hasSteerTarget && !remote {
		resp.Diagnostics.AddAttributeError(path.Root("steer_to_url"),
			fmt.Sprintf("steer_to_url is remote-only; omit it on a %q rule", kind),
			"Only host and url_prefix rules steer to a gateway route. A local steer is the shim wrap "+
				"and has no target to name.")
	}
	if actionKnown && hasSteerTarget && act != "steer" {
		resp.Diagnostics.AddAttributeError(path.Root("steer_to_url"),
			fmt.Sprintf("steer_to_url is only meaningful on a \"steer\" rule; omit it on an %q rule", act),
			"A steer target on an allow or block rule contradicts the rule's own action.")
	}
	if actionKnown && act == "steer" && remote && !steerToURL.IsUnknown() && isUnsetString(steerToURL) {
		resp.Diagnostics.AddAttributeError(path.Root("steer_to_url"),
			"steer_to_url is required on a remote \"steer\" rule",
			"Point it at this tenant's MCP gateway URL ending in the route of an enabled MCP server.")
	}

	if actionKnown && act == "allow" && contains(mcpRuleLocalKinds, kind) {
		if !deviceGroupIDs.IsUnknown() && len(deviceGroupIDs.Elements()) > 0 {
			resp.Diagnostics.AddAttributeError(path.Root("device_group_ids"),
				"A local \"allow\" rule cannot be narrowed by device_group_ids yet",
				localAllowNarrowingDetail)
		}
		if !criteria.IsUnknown() && len(criteria.Elements()) > 0 {
			resp.Diagnostics.AddAttributeError(path.Root("criteria"),
				"A local \"allow\" rule cannot be narrowed by criteria yet",
				localAllowNarrowingDetail)
		}
	}
}

const localAllowNarrowingDetail = "Devices do not carry an authenticated identity when a local MCP " +
	"server is admitted, so the narrowing would be ignored and the allow would apply to every device " +
	"in the tenant. Author the allow tenant-wide, or use \"block\": narrowing a deny fails safe."

func (r *EndpointMCPRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan EndpointMCPRuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	body, invalidGroups, diags := plan.toWrite(ctx)
	resp.Diagnostics.Append(diags...)
	if len(invalidGroups) > 0 {
		addInvalidGroupDiag(&resp.Diagnostics, invalidGroups)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.sdk.EndpointMCPRules().Create(ctx, tenantID, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create endpoint MCP rule", err)
		return
	}
	state := endpointMCPRuleToModel(tenantID, rule, &resp.Diagnostics)
	// match_values is Required, so state must hold the configured spelling; the
	// platform's canonical form lives in stored_match_values.
	state.MatchValues = plan.MatchValues
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *EndpointMCPRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state EndpointMCPRuleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	rule, err := r.sdk.EndpointMCPRules().Get(ctx, tenantID, state.RuleID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read endpoint MCP rule", err)
		return
	}
	refreshed := endpointMCPRuleToModel(tenantID, rule, &resp.Diagnostics)
	refreshed.MatchValues = reconcileMatchValues(state, refreshed)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *EndpointMCPRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state EndpointMCPRuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	body, invalidGroups, diags := plan.toWrite(ctx)
	resp.Diagnostics.Append(diags...)
	if len(invalidGroups) > 0 {
		addInvalidGroupDiag(&resp.Diagnostics, invalidGroups)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	rule, err := r.sdk.EndpointMCPRules().Update(ctx, tenantID, state.RuleID.ValueString(), version, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update endpoint MCP rule", err)
		return
	}
	refreshed := endpointMCPRuleToModel(tenantID, rule, &resp.Diagnostics)
	refreshed.MatchValues = plan.MatchValues
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *EndpointMCPRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state EndpointMCPRuleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	err := r.sdk.EndpointMCPRules().Delete(ctx, tenantID, state.RuleID.ValueString(), version)
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete endpoint MCP rule", err)
	}
}

func (r *EndpointMCPRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, ruleID string
	switch len(parts) {
	case 1:
		tenantID, ruleID = r.tenantID, parts[0]
	case 2:
		tenantID, ruleID = parts[0], parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>/<rule_id>` or configure `tenant_id` on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("rule_id"), ruleID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+ruleID)...)
}

func (r *EndpointMCPRuleResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// toWrite builds the request. The PUT replaces every field except priority,
// enabled and criteria_combinator, which carry schema Defaults and are therefore
// always known, so the whole rule is restated on every write. The second return
// value carries device_group_ids entries that are not UUIDs; callers must fail
// on it, because an empty target list means every device in the tenant.
func (m *EndpointMCPRuleResourceModel) toWrite(ctx context.Context) (adminapi.EndpointMCPRuleWrite, []string, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := adminapi.EndpointMCPRuleWrite{
		Name:      m.Name.ValueString(),
		Action:    m.Action.ValueString(),
		MatchKind: gen.EndpointMcpRuleRequestMatchKind(m.MatchKind.ValueString()),
	}
	var values *[]string
	setStringListPtr(ctx, m.MatchValues, &values)
	if values != nil {
		body.MatchValues = *values
	}
	setStringPtr(m.Description, &body.Description)
	setInt32Ptr(m.Priority, &body.Priority)
	setBoolPtr(m.Enabled, &body.Enabled)
	setStringPtr(m.SteerToURL, &body.SteerToUrl)
	setStringPtr(m.CriteriaCombinator, &body.CriteriaCombinator)
	setStringListPtr(ctx, m.AppBundleIDs, &body.AppBundleIds)
	invalidGroups := setUUIDListPtr(ctx, m.DeviceGroupIDs, &body.DeviceGroupIds)

	crits, d := criteriaListToOpaque(m.Criteria)
	diags.Append(d...)
	if len(crits) > 0 {
		body.Criteria = &crits
	}
	return body, invalidGroups, diags
}

// reconcileMatchValues decides what match_values holds after a refresh.
//
// The platform canonicalizes allow and steer values, so the stored list can
// differ from the configured one with nothing having changed. When the server's
// list still equals stored_match_values from the last apply, nothing changed
// out of band and the configured spelling is kept. When it differs, someone
// edited the rule elsewhere: match_values takes the server's list so the plan
// shows the drift. On import there is no prior stored list, so the server's
// list is taken as is.
func reconcileMatchValues(prior, refreshed EndpointMCPRuleResourceModel) types.List {
	if prior.StoredMatchValues.IsNull() || prior.StoredMatchValues.IsUnknown() ||
		prior.MatchValues.IsNull() || prior.MatchValues.IsUnknown() {
		return refreshed.MatchValues
	}
	if refreshed.StoredMatchValues.Equal(prior.StoredMatchValues) {
		return prior.MatchValues
	}
	return refreshed.MatchValues
}

func endpointMCPRuleToModel(tenantID string, rule *adminapi.EndpointMCPRule, diags *diag.Diagnostics) EndpointMCPRuleResourceModel {
	m := EndpointMCPRuleResourceModel{TenantID: types.StringValue(tenantID)}
	if rule.Id != nil {
		m.RuleID = types.StringValue(rule.Id.String())
	} else {
		m.RuleID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.RuleID.ValueString())
	m.Name = strPtrToTF(rule.Name)
	m.Description = strPtrToTF(rule.Description)
	m.Priority = int32PtrToTF(rule.Priority)
	m.Enabled = boolPtrOrDefault(rule.Enabled)
	m.MatchKind = strPtrToTF(rule.MatchKind)
	m.StoredMatchValues = stringSliceToList(rule.MatchValues)
	m.MatchValues = m.StoredMatchValues
	m.Action = strPtrToTF(rule.Action)
	m.SteerToURL = strPtrToTF(rule.SteerToUrl)
	m.AppBundleIDs = stringSliceToList(rule.AppBundleIds)
	m.DeviceGroupIDs = uuidSliceToList(rule.DeviceGroupIds)
	m.CriteriaCombinator = strPtrToTF(rule.CriteriaCombinator)
	m.Criteria = endpointRuleCriteria(rule.Criteria, diags)
	m.Enforcement = enumPtrToTF(rule.Enforcement)
	m.EnforcementReason = strPtrToTF(rule.EnforcementReason)
	m.CreatedAt = timePtrToTF(rule.CreatedAt)
	m.UpdatedAt = timePtrToTF(rule.UpdatedAt)

	m.Version = int64PtrToTF(rule.Version)
	m.ManagedBy = enumPtrToTF(rule.ManagedBy)
	m.ManagedByClientID = strPtrToTF(rule.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(rule.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(rule.LastModifiedBy)
	return m
}

// ModifyPlan warns when an apply is about to delete a rule's criteria: criteria
// narrow a rule, so removing them widens it to every user in the targeted
// groups. Same reasoning and same unknown handling as the destination rule.
func (r *EndpointMCPRuleResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var stateCriteria, planCriteria types.List
	var name types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("criteria"), &stateCriteria)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("criteria"), &planCriteria)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("name"), &name)...)
	if resp.Diagnostics.HasError() || planCriteria.IsUnknown() {
		return
	}
	if len(stateCriteria.Elements()) > 0 && len(planCriteria.Elements()) == 0 {
		resp.Diagnostics.AddAttributeWarning(path.Root("criteria"),
			"This apply removes the rule's subject criteria",
			fmt.Sprintf("Rule %q currently has %d criteria group(s) and the plan has none. Criteria "+
				"NARROW a rule to a population, so removing them widens it to every user in the "+
				"targeted device groups.\n\nIf you meant to keep them, add the `criteria` block back "+
				"to the config.", name.ValueString(), len(stateCriteria.Elements())))
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
