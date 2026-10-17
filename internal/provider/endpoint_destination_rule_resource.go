package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// EndpointDestinationRuleResource is the `ferentin_endpoint_destination_rule`
// Terraform resource — allow / block / steer AI traffic on managed devices
// (platform#2010, IaC-ready per platform#2038).
//
// Rules are evaluated on-device, first match wins by ascending `priority`, so
// `priority` IS the policy semantics rather than cosmetic ordering. See the
// note on that attribute before splitting ownership with the console.
type EndpointDestinationRuleResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type EndpointDestinationRuleResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Priority    types.Int64  `tfsdk:"priority"`
	Enabled     types.Bool   `tfsdk:"enabled"`

	DestinationKind  types.String `tfsdk:"destination_kind"`
	CatalogSlug      types.String `tfsdk:"catalog_slug"`
	DestinationHosts types.List   `tfsdk:"destination_hosts"`

	Action     types.String `tfsdk:"action"`
	SteerToURL types.String `tfsdk:"steer_to_url"`

	AppBundleIDs  types.List `tfsdk:"app_bundle_ids"`
	AppSigningIDs types.List `tfsdk:"app_signing_ids"`
	AppTeamIDs    types.List `tfsdk:"app_team_ids"`

	DeviceGroupIDs     types.List            `tfsdk:"device_group_ids"`
	CriteriaCombinator types.String          `tfsdk:"criteria_combinator"`
	Criteria           []PolicyCriteriaModel `tfsdk:"criteria"`

	PosturePredicate *EndpointPosturePredicateModel `tfsdk:"posture_predicate"`

	RuleID    types.String `tfsdk:"rule_id"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`

	// IaC readiness (platform#2038).
	Version           types.Int64  `tfsdk:"version"` // for If-Match
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

// EndpointPosturePredicateModel is the rule's device security-posture
// condition (#447 WS1). A pointer on the model: nil is "no condition".
type EndpointPosturePredicateModel struct {
	RequireCompliant types.Bool `tfsdk:"require_compliant"`
}

func NewEndpointDestinationRuleResource() resource.Resource {
	return &EndpointDestinationRuleResource{}
}

var (
	_ resource.Resource                   = (*EndpointDestinationRuleResource)(nil)
	_ resource.ResourceWithConfigure      = (*EndpointDestinationRuleResource)(nil)
	_ resource.ResourceWithImportState    = (*EndpointDestinationRuleResource)(nil)
	_ resource.ResourceWithValidateConfig = (*EndpointDestinationRuleResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*EndpointDestinationRuleResource)(nil)
)

func (r *EndpointDestinationRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_endpoint_destination_rule"
}

func (r *EndpointDestinationRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig moves the platform's three cross-field rules to PLAN time.
//
// EndpointPolicyAdminService enforces them server-side and DB CHECK constraints
// back it up, but a 400 at `apply` — after Terraform has already created the
// device groups and other rules in the same run — is a much worse experience
// than a `plan` failure. Kept in lockstep with that service's validate():
// if the server relaxes a rule, relax it here rather than leaving the provider
// stricter than the API.
//
// Unknown values (interpolations not yet resolved at plan time) are skipped —
// validating them would produce spurious failures for perfectly valid configs.
//
// The five attributes are read INDIVIDUALLY rather than through a whole-model
// Get. `criteria` is backed by a Go slice, and the framework cannot put an
// unknown into one: a config whose criteria are computed (a for-expression over
// a resource that does not exist yet) would fail the entire plan with "Value
// Conversion Error … always an error in the provider" before reaching any of
// the checks below. types.String / types.List targets carry unknown natively.
func (r *EndpointDestinationRuleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var destinationKind, catalogSlug, action, steerToURL types.String
	var destinationHosts types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("destination_kind"), &destinationKind)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("catalog_slug"), &catalogSlug)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("destination_hosts"), &destinationHosts)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("action"), &action)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("steer_to_url"), &steerToURL)...)
	if resp.Diagnostics.HasError() {
		return
	}

	kind := "ai_provider"
	if !destinationKind.IsNull() && !destinationKind.IsUnknown() {
		kind = destinationKind.ValueString()
	}

	if kind == "ai_provider" && isUnsetString(catalogSlug) {
		resp.Diagnostics.AddAttributeError(path.Root("catalog_slug"),
			"catalog_slug is required when destination_kind is \"ai_provider\"",
			"Set catalog_slug (e.g. \"openai\"), or switch destination_kind to \"host\" and set destination_hosts.")
	}
	if kind == "host" && isUnsetList(destinationHosts) {
		resp.Diagnostics.AddAttributeError(path.Root("destination_hosts"),
			"destination_hosts is required when destination_kind is \"host\"",
			"List at least one host or suffix, or switch destination_kind to \"ai_provider\" and set catalog_slug.")
	}
	if !action.IsUnknown() && action.ValueString() == "steer" && isUnsetString(steerToURL) {
		resp.Diagnostics.AddAttributeError(path.Root("steer_to_url"),
			"steer_to_url is required when action is \"steer\"",
			"Point it at the https:// base URL of the service-edge that should receive the steered traffic.")
	}
}

func (r *EndpointDestinationRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Endpoint destination rule — allow / block / steer AI traffic on managed " +
			"devices (the macOS endpoint agent). Rules are evaluated on-device, **first match wins** by " +
			"ascending `priority`.\n\n" +
			"`steer` points the device at a service-edge base URL; the device never holds an upstream " +
			"provider key — service-edge presents it.\n\n" +
			"Targeting `device_group_ids` bumps only those groups' policy-bundle versions, so devices in " +
			"other groups do not re-pull.\n\n" +
			"## Import\n\n" +
			"```\n" +
			"terraform import ferentin_endpoint_destination_rule.example <tenant_id>/<rule_id>\n" +
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
				// Mirrors @NotBlank + @Size(max = 255) on the platform DTO so an
				// over-long name fails at plan instead of as a 400 at apply.
				Validators: []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			// description, destination_hosts, app_*_ids and device_group_ids are
			// Optional, NOT Computed. The PUT replaces them, so removing one from
			// config must plan a change; Optional+Computed would plan the old state
			// value instead, and the rule would stay as it was until an unrelated
			// edit cleared it. endpointRuleToModel reads the platform's empty forms
			// as null.
			"description": schema.StringAttribute{
				Optional: true,
				// Mirrors @Size(max = 2000).
				Validators: []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"priority": schema.Int64Attribute{
				MarkdownDescription: "Evaluation order — 1 is highest, first match wins. Default `100`.\n\n" +
					"~> **Ownership warning.** Because ordering *is* the policy semantics here, do not " +
					"split ownership of `priority` between Terraform and the console. The console's " +
					"reorder arrows swap two rows' priorities directly; if Terraform also declares " +
					"`priority`, the next `plan` shows drift and the two fight over evaluation order.",
				Optional: true, Computed: true,
				Default: int64default.StaticInt64(100),
				// Mirrors @Min(value = 1) on the platform DTO.
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the rule is evaluated. Default `true`.",
				Optional:            true, Computed: true,
				Default: booldefault.StaticBool(true),
			},

			"destination_kind": schema.StringAttribute{
				MarkdownDescription: "How the destination is identified:\n\n" +
					"* `ai_provider` — matches a provider from the platform's AI catalog via " +
					"`catalog_slug`. The agent resolves hosts from the discovery data it already has, " +
					"so a new provider host does **not** require a rule rewrite. Preferred.\n" +
					"* `host` — matches the explicit host/suffix list in `destination_hosts`.\n\n" +
					"Default `ai_provider`.",
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("ai_provider"),
				Validators: []validator.String{stringvalidator.OneOf("ai_provider", "host")},
			},
			"catalog_slug": schema.StringAttribute{
				MarkdownDescription: "AI-catalog slug (e.g. `openai`, `anthropic`). **Required when " +
					"`destination_kind = \"ai_provider\"`.**",
				Optional: true,
			},
			"destination_hosts": schema.ListAttribute{
				MarkdownDescription: "Explicit host / suffix list. **Required when " +
					"`destination_kind = \"host\"`.**",
				Optional:    true,
				ElementType: types.StringType,
			},

			"action": schema.StringAttribute{
				MarkdownDescription: "`allow`, `block`, or `steer`. `steer` requires `steer_to_url`.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf("allow", "block", "steer")},
			},
			"steer_to_url": schema.StringAttribute{
				MarkdownDescription: "service-edge base URL the agent rewrites the destination to. " +
					"**Required when `action = \"steer\"`, and must be `https://`** — the platform " +
					"rejects `http://` because it would silently downgrade every steered flow on the " +
					"fleet to cleartext.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(httpsOnlyURL,
						"must be an https:// URL — http:// would downgrade steered fleet traffic to cleartext"),
				},
			},

			"app_bundle_ids": schema.ListAttribute{
				MarkdownDescription: "macOS bundle identifiers the rule applies to (e.g. " +
					"`com.anthropic.claudefordesktop`). Empty or unset = **any app**.",
				Optional: true, ElementType: types.StringType,
			},
			"app_signing_ids": schema.ListAttribute{
				MarkdownDescription: "macOS code-signing identifiers. Empty or unset = any app.",
				Optional:            true, ElementType: types.StringType,
			},
			"app_team_ids": schema.ListAttribute{
				MarkdownDescription: "Apple Developer Team IDs. Empty or unset = any app.",
				Optional:            true, ElementType: types.StringType,
			},

			"device_group_ids": schema.ListAttribute{
				MarkdownDescription: "Device groups this rule applies to — reference " +
					"`ferentin_device_group.<name>.group_id`. Empty or unset = **every device in the " +
					"tenant**, including ungrouped ones.\n\n" +
					"~> A group id belonging to another tenant is rejected with 404 (the platform " +
					"deliberately does not distinguish \"not yours\" from \"does not exist\").",
				Optional: true, ElementType: types.StringType,
			},
			"criteria_combinator": schema.StringAttribute{
				MarkdownDescription: "How `criteria` GROUPS combine: `AND` or `OR`. Default `AND`. " +
					"Conditions *within* a group combine via that group's own `operator`, so this only " +
					"matters once a rule has two or more groups.",
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("AND"),
				Validators: []validator.String{stringvalidator.OneOf("AND", "OR")},
			},

			"criteria": criteriaSchemaAttribute(criteriaSchemaOptions{
				Description: "User/department ABAC criteria that **narrow** the rule to a population. " +
					"Groups combine via `criteria_combinator`; omitting criteria entirely means the rule " +
					"applies to **every user** it otherwise targets.\n\n" +
					"~> **Removing criteria widens the rule.** Because criteria narrow rather than " +
					"extend, dropping this block from a config that had one is a fail-open change — an " +
					"`allow` rule scoped to one department becomes an allow for everyone in the targeted " +
					"device groups. `terraform plan` shows the removal, and the provider adds a warning " +
					"on top of it; read both before approving.\n\n" +
					"~> **Not yet enforced on-device.** The endpoint agent ships criteria in the policy " +
					"bundle but refuses to match a rule that has them until the on-device user principal " +
					"lands (platform#2014), so a criteria-scoped rule is inert on the fleet today. That " +
					"is fail-closed for `allow` and `steer`, and it means a `block` rule scoped by " +
					"criteria does not block.",
				TypeDescription: "Criteria type. `claims` (the default) matches the signed-in user's " +
					"identity claims. Unlike the policy resources, this column is stored opaquely by " +
					"the platform, so nothing is defaulted server-side.",
				TypeDefault:      "claims",
				TypeOneOf:        []string{"claims", "context", "request", "time"},
				FieldDescription: "Field path to evaluate (e.g. `department`, `email`, `groups`).",
				ValueExample:     "`jsonencode(\"legal\")`",
			}),

			// Optional, NOT Computed, for the same reason as device_group_ids: the
			// PUT replaces the predicate, so a config without one must plan the
			// clear rather than hide it. A predicate set in the console reads into
			// state, and the plan then shows (and warns about) its removal.
			"posture_predicate": schema.SingleNestedAttribute{
				MarkdownDescription: "Device security-posture condition that **narrows** the rule to " +
					"devices meeting it. Unset = the rule matches regardless of the device's posture.\n\n" +
					"Evaluated on the device. A device whose posture is unknown counts as not compliant " +
					"for an `allow` rule, and does not escape a `block` rule.\n\n" +
					"~> **Removing the condition widens the rule.** An `allow` or `steer` rule then " +
					"applies to non-compliant devices too. A condition set in the admin console shows up " +
					"in the plan as a removal unless the config declares it; the provider warns about it " +
					"at plan time.\n\n" +
					"~> **Agent version.** An enabled rule with a posture condition raises the endpoint " +
					"policy section of every device that receives it to version `1.4.0`. Agents older than that " +
					"refuse the section and keep enforcing their last-known-good policy, so they stop " +
					"picking up endpoint policy changes until upgraded.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"require_compliant": schema.BoolAttribute{
						MarkdownDescription: "Match only devices currently reporting compliant (SIP " +
							"enabled, full boot security, content filter attached). `false` is the same " +
							"as no condition: the platform stores it as none.",
						Required: true,
					},
				},
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

			// ===== IaC-readiness attributes (platform#2038) =====
			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version. Threaded as `If-Match` on " +
					"Update/Delete so a concurrent console edit is rejected with 412 rather than " +
					"silently clobbered. Read-only.",
				Computed: true,
			},
			"managed_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the original creator (`iac` for this provider). " +
					"Immutable after create — a console edit does not steal ownership.",
				Computed: true,
			},
			"managed_by_client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth2 `client_id` of the creator, taken from the authenticated " +
					"principal (never a header).",
				Computed: true,
			},
			"managed_by_module": schema.StringAttribute{
				MarkdownDescription: "Module label this provider sent via `X-Ferentin-Managed-By-Module`.",
				Computed:            true,
			},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent writer. **Divergence from " +
					"`managed_by` is the drift signal** — `managed_by = \"iac\"` with " +
					"`last_modified_by = \"console\"` means somebody edited a Terraform-managed rule " +
					"in the admin console.",
				Computed: true,
			},
		},
	}
}

func (r *EndpointDestinationRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan EndpointDestinationRuleResourceModel
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
	rule, err := r.sdk.EndpointPolicies().CreateRule(ctx, tenantID, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create endpoint destination rule", err)
		return
	}
	state := endpointRuleToModel(tenantID, rule, &resp.Diagnostics)
	state.keepEmptyForms(plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	checkPosturePredicateStored(plan, state, &resp.Diagnostics)
}

func (r *EndpointDestinationRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state EndpointDestinationRuleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	rule, err := r.sdk.EndpointPolicies().GetRule(ctx, tenantID, state.RuleID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read endpoint destination rule", err)
		return
	}
	refreshed := endpointRuleToModel(tenantID, rule, &resp.Diagnostics)
	refreshed.keepEmptyForms(state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *EndpointDestinationRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state EndpointDestinationRuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	// Version comes from state — never a literal. See the platform#2038 note on
	// ferentin_mcp_policy, where a hardcoded 0 made every second update 412.
	body, invalidGroups, diags := plan.toWrite(ctx)
	resp.Diagnostics.Append(diags...)
	if len(invalidGroups) > 0 {
		addInvalidGroupDiag(&resp.Diagnostics, invalidGroups)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	rule, err := r.sdk.EndpointPolicies().UpdateRule(ctx, tenantID, state.RuleID.ValueString(),
		version, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update endpoint destination rule", err)
		return
	}
	refreshed := endpointRuleToModel(tenantID, rule, &resp.Diagnostics)
	refreshed.keepEmptyForms(plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
	checkPosturePredicateStored(plan, refreshed, &resp.Diagnostics)
}

func (r *EndpointDestinationRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state EndpointDestinationRuleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	err := r.sdk.EndpointPolicies().DeleteRule(ctx, tenantID, state.RuleID.ValueString(), version)
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete endpoint destination rule", err)
	}
}

func (r *EndpointDestinationRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	tenantID, ruleID, diags := parseTenantScopedImportID(req.ID, r.tenantID, "rule")
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("rule_id"), ruleID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+ruleID)...)
}

func (r *EndpointDestinationRuleResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// toWrite builds the PUT body. The second return value carries any
// device_group_ids entries that are not valid UUIDs — callers MUST fail on a
// non-empty result rather than proceed, because an empty target list means
// "every device in the tenant" and silently dropping a bad id would widen the
// rule's blast radius from one group to the whole fleet.
func (m *EndpointDestinationRuleResourceModel) toWrite(ctx context.Context) (adminapi.EndpointDestinationRuleWrite, []string, diag.Diagnostics) {
	var diags diag.Diagnostics
	// PUT is a full replace on this resource, so every optional field is sent —
	// including nil for the ones the config omits. Sending only changed fields
	// would leave stale values on the row.
	body := adminapi.EndpointDestinationRuleWrite{
		Name:   m.Name.ValueString(),
		Action: m.Action.ValueString(),
	}
	setStringPtr(m.Description, &body.Description)
	setInt32Ptr(m.Priority, &body.Priority)
	setBoolPtr(m.Enabled, &body.Enabled)
	setStringPtr(m.DestinationKind, &body.DestinationKind)
	setStringPtr(m.CatalogSlug, &body.CatalogSlug)
	setStringPtr(m.SteerToURL, &body.SteerToUrl)
	setStringPtr(m.CriteriaCombinator, &body.CriteriaCombinator)
	setStringListPtr(ctx, m.DestinationHosts, &body.DestinationHosts)
	setStringListPtr(ctx, m.AppBundleIDs, &body.AppBundleIds)
	setStringListPtr(ctx, m.AppSigningIDs, &body.AppSigningIds)
	setStringListPtr(ctx, m.AppTeamIDs, &body.AppTeamIds)
	invalidGroups := setUUIDListPtr(ctx, m.DeviceGroupIDs, &body.DeviceGroupIds)

	// The platform's PUT is a full replace that sets `criteria` from the request
	// unconditionally, so what is sent here IS the rule's whole subject scope.
	// An empty result therefore means "matches every user", which for an `allow`
	// rule is a widening — hence the plan-time warning in ModifyPlan when a
	// config drops criteria it previously had.
	crits, d := criteriaListToOpaque(m.Criteria)
	diags.Append(d...)
	if len(crits) > 0 {
		body.Criteria = &crits
	}

	// Sent on every PUT, for the same full-replace reason: the platform stores
	// an absent predicate as NULL, so leaving this out would erase a condition
	// the config declares.
	if p := m.PosturePredicate; p != nil && !p.RequireCompliant.IsNull() && !p.RequireCompliant.IsUnknown() {
		body.PosturePredicate = &gen.EndpointPosturePredicateDto{
			RequireCompliant: p.RequireCompliant.ValueBoolPointer(),
		}
	}
	return body, invalidGroups, diags
}

// addInvalidGroupDiag renders the fail-loud diagnostic for malformed
// device_group_ids entries.
func addInvalidGroupDiag(diags *diag.Diagnostics, invalid []string) {
	diags.AddAttributeError(path.Root("device_group_ids"),
		"device_group_ids contains a value that is not a UUID",
		fmt.Sprintf("Not valid UUIDs: %s.\n\nReference a managed group "+
			"(ferentin_device_group.<name>.group_id) rather than writing ids by hand. This is an "+
			"error rather than a skipped entry on purpose: an empty target list means the rule "+
			"applies to EVERY device in the tenant, so dropping a bad id would silently widen the "+
			"rule from one group to the whole fleet.", strings.Join(invalid, ", ")))
}

func endpointRuleToModel(tenantID string, rule *adminapi.EndpointDestinationRule, diags *diag.Diagnostics) EndpointDestinationRuleResourceModel {
	m := EndpointDestinationRuleResourceModel{TenantID: types.StringValue(tenantID)}
	if rule.Id != nil {
		m.RuleID = types.StringValue(rule.Id.String())
	} else {
		m.RuleID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.RuleID.ValueString())
	m.Name = strPtrToTF(rule.Name)
	m.Description = emptyStringAsNull(rule.Description)
	m.Priority = int32PtrToTF(rule.Priority)
	m.Enabled = boolPtrOrDefault(rule.Enabled)
	m.DestinationKind = strPtrToTF(rule.DestinationKind)
	m.CatalogSlug = strPtrToTF(rule.CatalogSlug)
	m.DestinationHosts = emptyListAsNull(stringSliceToList(rule.DestinationHosts))
	m.Action = strPtrToTF(rule.Action)
	m.SteerToURL = strPtrToTF(rule.SteerToUrl)
	m.AppBundleIDs = emptyListAsNull(stringSliceToList(rule.AppBundleIds))
	m.AppSigningIDs = emptyListAsNull(stringSliceToList(rule.AppSigningIds))
	m.AppTeamIDs = emptyListAsNull(stringSliceToList(rule.AppTeamIds))
	m.DeviceGroupIDs = emptyListAsNull(uuidSliceToList(rule.DeviceGroupIds))
	m.CriteriaCombinator = strPtrToTF(rule.CriteriaCombinator)
	m.Criteria = endpointRuleCriteria(rule.Criteria, diags)
	m.PosturePredicate = posturePredicateToModel(rule.PosturePredicate)
	m.CreatedAt = timePtrToTF(rule.CreatedAt)
	m.UpdatedAt = timePtrToTF(rule.UpdatedAt)

	m.Version = int64PtrToTF(rule.Version)
	m.ManagedBy = enumPtrToTF(rule.ManagedBy)
	m.ManagedByClientID = strPtrToTF(rule.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(rule.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(rule.LastModifiedBy)
	return m
}

// checkPosturePredicateStored fails the apply when the config requires a
// compliant device and the platform's response has no condition. A platform
// build without #447 WS1 ignores the field, so the rule it saved matches every
// device; the framework's generic "inconsistent result" error would not say so.
// State is already set, so Terraform keeps tracking (and can fix) the rule.
func checkPosturePredicateStored(plan, got EndpointDestinationRuleResourceModel, diags *diag.Diagnostics) {
	want := plan.PosturePredicate != nil && plan.PosturePredicate.RequireCompliant.ValueBool()
	if !want || got.PosturePredicate != nil {
		return
	}
	diags.AddAttributeError(path.Root("posture_predicate"),
		"The platform did not store the rule's device posture condition",
		fmt.Sprintf("Rule %q was saved, but the platform's response has no posture condition, so the "+
			"rule now matches devices whatever their posture. The platform is probably too old to "+
			"support `posture_predicate`. Disable or remove the rule until the platform is upgraded.",
			plan.Name.ValueString()))
}

// posturePredicateToModel reads a predicate that constrains nothing (absent, or
// requireCompliant false or missing) as nil. The platform stores those forms as
// NULL anyway; keepEmptyForms restores `require_compliant = false` when the
// config wrote it.
func posturePredicateToModel(p *gen.EndpointPosturePredicateDto) *EndpointPosturePredicateModel {
	if p == nil || p.RequireCompliant == nil || !*p.RequireCompliant {
		return nil
	}
	return &EndpointPosturePredicateModel{RequireCompliant: types.BoolValue(true)}
}

// endpointRuleCriteria maps the stored (opaque) criteria into the shared model,
// warning about anything on the wire this provider does not model.
//
// The warning matters because the column is untyped: the console — or a future
// platform version — can store keys the provider's schema has no attribute for
// (a condition's `stage`, say). Those keys are absent from state, so the next
// PUT, which is a full replace, drops them. Silently narrowing or widening a
// criteria group is exactly the failure this resource is careful about
// everywhere else, so say so out loud instead.
func endpointRuleCriteria(criteria *[]map[string]interface{}, diags *diag.Diagnostics) []PolicyCriteriaModel {
	models, unmodelled := criteriaListFromOpaque(criteria)
	if len(unmodelled) > 0 && diags != nil {
		diags.AddAttributeWarning(path.Root("criteria"),
			"Endpoint rule criteria contain fields this provider does not model",
			fmt.Sprintf("Not represented in Terraform state: %s.\n\nThese were authored outside "+
				"Terraform. The platform's update is a full replace built from state, so the next "+
				"`terraform apply` that touches this rule will DROP them and change which users the "+
				"rule matches. Re-author the criteria in the console after that apply, or upgrade the "+
				"provider if it has since grown the attribute.", strings.Join(unmodelled, ", ")))
	}
	return models
}

// ModifyPlan warns when an apply is about to delete a rule's criteria, device
// groups, app filters or posture condition; see warnOnCriteriaRemoval,
// warnOnTargetRemoval and warnOnPosturePredicateChange. Under an allowlist
// posture (`default_destination_action = "block"`) that warning is the
// difference between a scoped exception and a fleet-wide one.
func (r *EndpointDestinationRuleResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	warnOnCriteriaRemoval(ctx, req, resp,
		"\n\nIf you meant to keep them, add the `criteria` block back to the config (criteria "+
			"authored in the admin console are not preserved for you — Terraform owns this field now).")
	// Earlier versions planned no change when these were removed, so after an
	// upgrade the warning can be about a removal made long ago.
	const upgrade = " Earlier provider versions ignored the removal of this attribute, so after " +
		"an upgrade this can be a removal made long ago."
	warnOnTargetRemoval(ctx, req, resp, "device_group_ids",
		"every device in the tenant, including ungrouped ones", upgrade)
	for _, attr := range []string{"app_bundle_ids", "app_signing_ids", "app_team_ids"} {
		warnOnTargetRemoval(ctx, req, resp, attr, "any app, unless another app filter still narrows it", upgrade)
	}
	warnOnPosturePredicateChange(ctx, req, resp)
}

// warnOnPosturePredicateChange warns when an apply removes the rule's posture
// condition (a widening, like removing criteria), and when it puts a posture
// condition into force: added to an enabled rule, or a rule that has one being
// enabled. cp-server projects only enabled rules, so that is the moment the
// devices receiving the rule move to bundle section 1.4.0, which older agents
// refuse. Unknown plan values may resolve either way, so they never warn.
func warnOnPosturePredicateChange(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var planPredicate types.Object
	var planEnabled, stateEnabled types.Bool
	var name types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("posture_predicate"), &planPredicate)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("enabled"), &planEnabled)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("name"), &name)...)
	if resp.Diagnostics.HasError() {
		return
	}
	planRequires, known := requiresCompliant(planPredicate)
	if !known {
		return
	}
	stateRequires := false
	if !req.State.Raw.IsNull() {
		var statePredicate types.Object
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("posture_predicate"), &statePredicate)...)
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("enabled"), &stateEnabled)...)
		if resp.Diagnostics.HasError() {
			return
		}
		stateRequires, _ = requiresCompliant(statePredicate)
	}
	// enabled defaults to true, so null here is an old state, not a disabled rule.
	isOn := func(b types.Bool) bool { return b.IsNull() || (!b.IsUnknown() && b.ValueBool()) }
	inForce := !req.State.Raw.IsNull() && stateRequires && isOn(stateEnabled)

	switch {
	case stateRequires && !planRequires:
		// Warned on a disabled rule too: the condition is gone either way, and
		// enabling the rule later raises no warning of its own.
		resp.Diagnostics.AddAttributeWarning(path.Root("posture_predicate"),
			"This apply removes the rule's device posture condition",
			fmt.Sprintf("Rule %q currently matches only devices that report compliant, and the plan "+
				"removes that condition, so it will match devices whatever their posture. An `allow` "+
				"or `steer` rule then applies to non-compliant devices too.\n\n"+
				"If the condition was set in the admin console and you meant to keep it, add "+
				"`posture_predicate = { require_compliant = true }` to the config. Earlier provider "+
				"versions did not model this attribute and erased the condition on every apply that "+
				"touched the rule, without showing it in the plan.", name.ValueString()))
	case planRequires && !planEnabled.IsUnknown() && isOn(planEnabled) && !inForce:
		resp.Diagnostics.AddAttributeWarning(path.Root("posture_predicate"),
			"This apply puts a device posture condition into force",
			fmt.Sprintf("Rule %q will be enabled with a posture condition, so it matches only devices "+
				"that report compliant. An enabled rule with a posture condition raises the endpoint "+
				"policy section of every device that receives it to version 1.4.0. Agents older than "+
				"1.4.0 refuse that section and keep enforcing their last-known-good policy, so they "+
				"stop picking up endpoint policy changes until they are upgraded. Check the agent "+
				"versions in the targeted device groups before applying.", name.ValueString()))
	}
}

// requiresCompliant reports whether a posture_predicate value requires a
// compliant device, and whether that is known yet. Null means no condition.
func requiresCompliant(p types.Object) (requires, known bool) {
	if p.IsUnknown() {
		return false, false
	}
	if p.IsNull() {
		return false, true
	}
	v, ok := p.Attributes()["require_compliant"].(types.Bool)
	if !ok || v.IsUnknown() {
		return false, false
	}
	return v.ValueBool(), true
}

// keepEmptyForms restores an empty form the config wrote explicitly for the
// Optional, non-Computed attributes, which endpointRuleToModel reads as null.
func (m *EndpointDestinationRuleResourceModel) keepEmptyForms(prior EndpointDestinationRuleResourceModel) {
	m.Description = keepEmptyString(prior.Description, m.Description)
	m.DestinationHosts = keepEmptyList(prior.DestinationHosts, m.DestinationHosts)
	m.AppBundleIDs = keepEmptyList(prior.AppBundleIDs, m.AppBundleIDs)
	m.AppSigningIDs = keepEmptyList(prior.AppSigningIDs, m.AppSigningIDs)
	m.AppTeamIDs = keepEmptyList(prior.AppTeamIDs, m.AppTeamIDs)
	m.DeviceGroupIDs = keepEmptyList(prior.DeviceGroupIDs, m.DeviceGroupIDs)
	// `posture_predicate = { require_compliant = false }` is the predicate's
	// empty form: the platform stores it as no condition.
	if p := prior.PosturePredicate; m.PosturePredicate == nil && p != nil &&
		!p.RequireCompliant.IsNull() && !p.RequireCompliant.IsUnknown() && !p.RequireCompliant.ValueBool() {
		m.PosturePredicate = p
	}
}
