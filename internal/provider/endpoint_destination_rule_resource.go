package provider

import (
	"context"
	"encoding/json"
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

	DeviceGroupIDs     types.List   `tfsdk:"device_group_ids"`
	CriteriaCombinator types.String `tfsdk:"criteria_combinator"`
	// Read-only passthrough so a PUT does not destroy criteria this resource
	// cannot author. See the schema doc and toWrite.
	CriteriaJSON types.String `tfsdk:"criteria_json"`

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

func NewEndpointDestinationRuleResource() resource.Resource {
	return &EndpointDestinationRuleResource{}
}

var (
	_ resource.Resource                   = (*EndpointDestinationRuleResource)(nil)
	_ resource.ResourceWithConfigure      = (*EndpointDestinationRuleResource)(nil)
	_ resource.ResourceWithImportState    = (*EndpointDestinationRuleResource)(nil)
	_ resource.ResourceWithValidateConfig = (*EndpointDestinationRuleResource)(nil)
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
func (r *EndpointDestinationRuleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg EndpointDestinationRuleResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	kind := "ai_provider"
	if !cfg.DestinationKind.IsNull() && !cfg.DestinationKind.IsUnknown() {
		kind = cfg.DestinationKind.ValueString()
	}

	if kind == "ai_provider" && isUnsetString(cfg.CatalogSlug) {
		resp.Diagnostics.AddAttributeError(path.Root("catalog_slug"),
			"catalog_slug is required when destination_kind is \"ai_provider\"",
			"Set catalog_slug (e.g. \"openai\"), or switch destination_kind to \"host\" and set destination_hosts.")
	}
	if kind == "host" && isUnsetList(cfg.DestinationHosts) {
		resp.Diagnostics.AddAttributeError(path.Root("destination_hosts"),
			"destination_hosts is required when destination_kind is \"host\"",
			"List at least one host or suffix, or switch destination_kind to \"ai_provider\" and set catalog_slug.")
	}
	if !cfg.Action.IsUnknown() && cfg.Action.ValueString() == "steer" && isUnsetString(cfg.SteerToURL) {
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
			"description": schema.StringAttribute{
				Optional: true, Computed: true,
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
				Optional: true, Computed: true,
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
				Optional: true, Computed: true, ElementType: types.StringType,
			},
			"app_signing_ids": schema.ListAttribute{
				MarkdownDescription: "macOS code-signing identifiers. Empty or unset = any app.",
				Optional:            true, Computed: true, ElementType: types.StringType,
			},
			"app_team_ids": schema.ListAttribute{
				MarkdownDescription: "Apple Developer Team IDs. Empty or unset = any app.",
				Optional:            true, Computed: true, ElementType: types.StringType,
			},

			"device_group_ids": schema.ListAttribute{
				MarkdownDescription: "Device groups this rule applies to — reference " +
					"`ferentin_device_group.<name>.group_id`. Empty or unset = **every device in the " +
					"tenant**, including ungrouped ones.\n\n" +
					"~> A group id belonging to another tenant is rejected with 404 (the platform " +
					"deliberately does not distinguish \"not yours\" from \"does not exist\").",
				Optional: true, Computed: true, ElementType: types.StringType,
			},
			"criteria_combinator": schema.StringAttribute{
				MarkdownDescription: "How user/department ABAC criteria combine: `AND` or `OR`. " +
					"Default `AND`.\n\n" +
					"~> Criteria themselves cannot be *authored* here yet — see `criteria_json`.",
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("AND"),
				Validators: []validator.String{stringvalidator.OneOf("AND", "OR")},
			},

			"criteria_json": schema.StringAttribute{
				MarkdownDescription: "The rule's user/department ABAC criteria, as an opaque JSON string. " +
					"**Read-only, and preserved rather than managed.**\n\n" +
					"Criteria cannot be authored from Terraform yet. But the platform's update is a PUT " +
					"full replace that sets `criteria` from the request unconditionally, so a provider " +
					"that simply omitted the field would **silently delete** criteria authored in the " +
					"admin console on every `terraform apply`. That is a fail-open: criteria *narrow* a " +
					"rule to a population, and the endpoint agent currently refuses to match a rule that " +
					"has them — so wiping them flips a rule from inert to active for everyone it " +
					"targets.\n\n" +
					"This attribute therefore round-trips the server's value back unchanged. A concurrent " +
					"console edit to criteria bumps `version`, so the stale value is rejected by " +
					"`If-Match` (412) rather than re-applied. Authoring criteria from Terraform is a " +
					"follow-up.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
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

	body, invalidGroups := plan.toWrite(ctx)
	if len(invalidGroups) > 0 {
		addInvalidGroupDiag(&resp.Diagnostics, invalidGroups)
		return
	}
	rule, err := r.sdk.EndpointPolicies().CreateRule(ctx, tenantID, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create endpoint destination rule", err)
		return
	}
	state := endpointRuleToModel(tenantID, rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
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
	refreshed := endpointRuleToModel(tenantID, rule)
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
	body, invalidGroups := plan.toWrite(ctx)
	if len(invalidGroups) > 0 {
		addInvalidGroupDiag(&resp.Diagnostics, invalidGroups)
		return
	}
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	rule, err := r.sdk.EndpointPolicies().UpdateRule(ctx, tenantID, state.RuleID.ValueString(),
		version, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update endpoint destination rule", err)
		return
	}
	refreshed := endpointRuleToModel(tenantID, rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
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
func (m *EndpointDestinationRuleResourceModel) toWrite(ctx context.Context) (adminapi.EndpointDestinationRuleWrite, []string) {
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

	// Preserve criteria this resource cannot author. The platform's PUT sets
	// `criteria` from the request unconditionally, so omitting the field deletes
	// whatever the console authored — see the criteria_json schema doc for why
	// that is a fail-open and not merely lossy.
	if !m.CriteriaJSON.IsNull() && !m.CriteriaJSON.IsUnknown() && m.CriteriaJSON.ValueString() != "" {
		var criteria []map[string]interface{}
		if err := json.Unmarshal([]byte(m.CriteriaJSON.ValueString()), &criteria); err == nil && len(criteria) > 0 {
			body.Criteria = &criteria
		}
		// An unmarshal failure is deliberately not fatal: criteria_json is
		// server-provided, so a shape this provider cannot parse means the
		// platform grew the model. Dropping it here would delete it, so the
		// caller re-reads instead — endpointRuleToModel keeps the raw string in
		// state and the next Read repairs it.
	}
	return body, invalidGroups
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

func endpointRuleToModel(tenantID string, rule *adminapi.EndpointDestinationRule) EndpointDestinationRuleResourceModel {
	m := EndpointDestinationRuleResourceModel{TenantID: types.StringValue(tenantID)}
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
	m.DestinationKind = strPtrToTF(rule.DestinationKind)
	m.CatalogSlug = strPtrToTF(rule.CatalogSlug)
	m.DestinationHosts = stringSliceToList(rule.DestinationHosts)
	m.Action = strPtrToTF(rule.Action)
	m.SteerToURL = strPtrToTF(rule.SteerToUrl)
	m.AppBundleIDs = stringSliceToList(rule.AppBundleIds)
	m.AppSigningIDs = stringSliceToList(rule.AppSigningIds)
	m.AppTeamIDs = stringSliceToList(rule.AppTeamIds)
	m.DeviceGroupIDs = uuidSliceToList(rule.DeviceGroupIds)
	m.CriteriaCombinator = strPtrToTF(rule.CriteriaCombinator)
	m.CriteriaJSON = criteriaToJSONString(rule.Criteria)
	m.CreatedAt = timePtrToTF(rule.CreatedAt)
	m.UpdatedAt = timePtrToTF(rule.UpdatedAt)

	m.Version = int64PtrToTF(rule.Version)
	m.ManagedBy = enumPtrToTF(rule.ManagedBy)
	m.ManagedByClientID = strPtrToTF(rule.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(rule.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(rule.LastModifiedBy)
	return m
}

// criteriaToJSONString renders the server's opaque criteria array as a JSON
// string for state, or null when there are none. Null and "[]" are deliberately
// collapsed to null: an empty criteria array and an absent one both mean
// "matches everyone" on the platform, and keeping them distinct would show a
// permanent diff.
func criteriaToJSONString(criteria *[]map[string]interface{}) types.String {
	if criteria == nil || len(*criteria) == 0 {
		return types.StringNull()
	}
	raw, err := json.Marshal(*criteria)
	if err != nil {
		// Unreachable for decoded JSON, but returning null here would signal
		// "no criteria" and cause the very deletion this exists to prevent —
		// so surface something non-empty that a Read will correct.
		return types.StringValue("[]")
	}
	return types.StringValue(string(raw))
}
