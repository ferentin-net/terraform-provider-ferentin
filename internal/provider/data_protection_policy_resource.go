package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// DataProtectionPolicyResource is the `ferentin_data_protection_policy`
// resource — the tenant's DLP / "Data & Content Protection" policy
// (ferentin-platform#1060). Detector selection is three-layered (profiles +
// individual enable + profile-exclude), effects are per-detector with a
// default, and four scope flags gate where the policy runs (LLM/MCP ×
// input/output).
//
// Naming tracks the backend (`data_protection_*`, `/data-protection-policies`),
// NOT the console's "Data & Content Protection" label — the #1060 rename is
// UX-only.
type DataProtectionPolicyResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type DataProtectionPolicyResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`
	PolicyID types.String `tfsdk:"policy_id"`

	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Priority    types.Int64  `tfsdk:"priority"`
	Enabled     types.Bool   `tfsdk:"enabled"`

	EnabledProfiles    types.List `tfsdk:"enabled_profiles"`    // []string
	EnabledDetectors   types.Map  `tfsdk:"enabled_detectors"`   // map[string]bool
	DisabledDetectors  types.Map  `tfsdk:"disabled_detectors"`  // map[string]bool
	DetectorThresholds types.Map  `tfsdk:"detector_thresholds"` // map[string]float64
	DetectorConfigs    types.Map  `tfsdk:"detector_configs"`    // map[string]json-string
	Effects            types.Map  `tfsdk:"effects"`             // map[string]string

	DefaultEffect  types.String `tfsdk:"default_effect"`
	BlockedMessage types.String `tfsdk:"blocked_message"`
	FpeKeyID       types.String `tfsdk:"fpe_key_id"`
	TweakScope     types.String `tfsdk:"tweak_scope"`

	ApplyToLlmInput  types.Bool `tfsdk:"apply_to_llm_input"`
	ApplyToLlmOutput types.Bool `tfsdk:"apply_to_llm_output"`
	ApplyToMcpInput  types.Bool `tfsdk:"apply_to_mcp_input"`
	ApplyToMcpOutput types.Bool `tfsdk:"apply_to_mcp_output"`

	// Endpoint scopes (ferentin-platform#2872): evaluated on enrolled devices.
	ApplyToToolInput          types.Bool   `tfsdk:"apply_to_tool_input"`
	ApplyToPromptInput        types.Bool   `tfsdk:"apply_to_prompt_input"`
	ApplyToToolOutput         types.Bool   `tfsdk:"apply_to_tool_output"`
	ToolOutputUnscannedAction types.String `tfsdk:"tool_output_unscanned_action"`
	DeviceGroupIDs            types.Set    `tfsdk:"device_group_ids"`        // []uuid-string
	EndpointEffectMapping     types.Map    `tfsdk:"endpoint_effect_mapping"` // scope -> authored -> applied

	Criteria []PolicyCriteriaModel `tfsdk:"criteria"`

	ProfileCount types.Int64  `tfsdk:"profile_count"`
	CreatedAt    types.String `tfsdk:"created_at"`
	CreatedBy    types.String `tfsdk:"created_by"`
	UpdatedAt    types.String `tfsdk:"updated_at"`
	UpdatedBy    types.String `tfsdk:"updated_by"`
}

func NewDataProtectionPolicyResource() resource.Resource { return &DataProtectionPolicyResource{} }

var (
	_ resource.Resource                = (*DataProtectionPolicyResource)(nil)
	_ resource.ResourceWithConfigure   = (*DataProtectionPolicyResource)(nil)
	_ resource.ResourceWithImportState = (*DataProtectionPolicyResource)(nil)
)

func (r *DataProtectionPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_data_protection_policy"
}

func (r *DataProtectionPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *DataProtectionPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	effectValues := []string{"tokenize", "redact", "block", "log"}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Tenant data-protection (DLP / \"Data & Content Protection\") policy. Selects " +
			"sensitive-data and security detectors (via profiles and/or individual detectors), assigns a " +
			"per-detector or default effect (`tokenize` / `redact` / `block` / `log`), and scopes enforcement " +
			"to LLM and/or MCP input/output. Optional `criteria` apply the policy conditionally (ABAC).\n\n" +
			"## Import\n\n" +
			"```\n" +
			"terraform import ferentin_data_protection_policy.example <tenant_id>/<policy_id>\n" +
			"```",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant_id": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"policy_id": schema.StringAttribute{
				Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},

			"name":        schema.StringAttribute{Required: true},
			"description": schema.StringAttribute{Optional: true, Computed: true},
			"priority": schema.Int64Attribute{
				MarkdownDescription: "Evaluation priority (1 = highest). Defaults to 100.",
				Optional:            true, Computed: true,
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the policy is enforced. Default `true`.",
				Optional:            true, Computed: true,
				Default: booldefault.StaticBool(true),
			},

			"enabled_profiles": schema.ListAttribute{
				MarkdownDescription: "Detector profiles to enable, e.g. `US_PII`, `EU_PII`, `SECRETS`, " +
					"`EXFILTRATION_DEFENSE`. Profile names are catalog data (see the " +
					"`ferentin_data_protection_profiles` data source) and validated server-side.",
				Optional: true, Computed: true,
				ElementType: types.StringType,
			},
			"enabled_detectors": schema.MapAttribute{
				MarkdownDescription: "Individual detectors to enable beyond the selected profiles, keyed by " +
					"detector ID (e.g. `{ \"DATABASE_URL\" = true }`).",
				Optional: true, Computed: true,
				ElementType: types.BoolType,
			},
			"disabled_detectors": schema.MapAttribute{
				MarkdownDescription: "Detectors to exclude even though a profile pulls them in, keyed by " +
					"detector ID (e.g. `{ \"EU_VAT\" = true }`).",
				Optional: true, Computed: true,
				ElementType: types.BoolType,
			},
			"detector_thresholds": schema.MapAttribute{
				MarkdownDescription: "Per-detector confidence threshold override (0.0–1.0), keyed by detector ID.",
				Optional:            true, Computed: true,
				ElementType: types.Float64Type,
			},
			"detector_configs": schema.MapAttribute{
				MarkdownDescription: "Per-detector validator-config overrides, shallow-merged over the catalog " +
					"default at scan time. Keyed by detector ID; each value is a JSON object produced with " +
					"`jsonencode(...)`, e.g. " +
					"`{ \"EXFILTRATION_URL\" = jsonencode({ minConfidenceScore = 0.5 }) }`.",
				Optional: true, Computed: true,
				ElementType: types.StringType,
			},
			"effects": schema.MapAttribute{
				MarkdownDescription: "Per-detector effect override, keyed by detector ID. One of " +
					"`tokenize` / `redact` / `block` / `log`.",
				Optional: true, Computed: true,
				ElementType: types.StringType,
				Validators: []validator.Map{
					mapvalidator.ValueStringsAre(stringvalidator.OneOf(effectValues...)),
				},
			},

			"default_effect": schema.StringAttribute{
				MarkdownDescription: "Effect applied when a matched detector has no per-detector override. " +
					"One of `tokenize` / `redact` / `block` / `log`. Defaults to `redact`.",
				Optional: true, Computed: true,
				Validators: []validator.String{stringvalidator.OneOf(effectValues...)},
			},
			"blocked_message": schema.StringAttribute{
				MarkdownDescription: "User-facing message returned when a request is blocked.",
				Optional:            true, Computed: true,
			},
			"fpe_key_id": schema.StringAttribute{
				MarkdownDescription: "Key identifier for format-preserving encryption. Required by the platform " +
					"when any effect is `tokenize`.",
				Optional: true, Computed: true,
			},
			"tweak_scope": schema.StringAttribute{
				MarkdownDescription: "FPE tweak scope: `conversation` / `request` / `tenant`. Defaults to " +
					"`conversation`.",
				Optional: true, Computed: true,
				Validators: []validator.String{stringvalidator.OneOf("conversation", "request", "tenant")},
			},

			"apply_to_llm_input":  schema.BoolAttribute{MarkdownDescription: "Scan LLM user input/prompts. Default `true`.", Optional: true, Computed: true},
			"apply_to_llm_output": schema.BoolAttribute{MarkdownDescription: "Scan LLM responses. Default `true`.", Optional: true, Computed: true},
			"apply_to_mcp_input":  schema.BoolAttribute{MarkdownDescription: "Scan MCP tool-call arguments. Default `false`.", Optional: true, Computed: true},
			"apply_to_mcp_output": schema.BoolAttribute{MarkdownDescription: "Scan MCP tool-call results. Default `false`.", Optional: true, Computed: true},

			// Endpoint scopes. Unlike the four above these default to false in the schema, so
			// removing one from config turns that scope off rather than leaving it as it was.
			"apply_to_tool_input": schema.BoolAttribute{
				MarkdownDescription: "Evaluate on enrolled endpoints, against AI agents' native tool calls: shell " +
					"commands, fetched URLs and bodies, file writes. `tokenize` is enforced there as `redact` " +
					"(as `block` on an agent that cannot rewrite tool input). Default `false`.",
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"apply_to_prompt_input": schema.BoolAttribute{
				MarkdownDescription: "Evaluate on enrolled endpoints, against prompts developers submit to AI " +
					"agents, before they reach the model. A prompt cannot be rewritten, so `redact` and " +
					"`tokenize` are enforced as `block`. Default `false`.",
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"apply_to_tool_output": schema.BoolAttribute{
				MarkdownDescription: "Evaluate on enrolled endpoints, against what a tool returns before the " +
					"agent's model sees it: `block` replaces the affected text with a notice and `redact` masks " +
					"the match. The tool has already run, so this protects the model's context, not the action. " +
					"Enforced only on agents whose hooks can replace tool output. Default `false`.",
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"tool_output_unscanned_action": schema.StringAttribute{
				MarkdownDescription: "With `apply_to_tool_output`: what a device does with tool output it knows it " +
					"could not scan (over its size cap, or out of scan time). `pass` lets it through and counts " +
					"it; `withhold` replaces it with a notice. Best effort: output the agent itself lets through " +
					"(its own hook timeout) reaches the model regardless. Default `pass`.",
				Optional: true, Computed: true, Default: stringdefault.StaticString("pass"),
				Validators: []validator.String{stringvalidator.OneOf("pass", "withhold")},
			},
			"device_group_ids": schema.SetAttribute{
				MarkdownDescription: "Device groups this policy targets on its endpoint scopes — reference " +
					"`ferentin_device_group.<name>.group_id`. Empty or unset = **every device in the tenant**. " +
					"Ignored by edge and cloud enforcement. At most 100.\n\n" +
					"~> A group id belonging to another tenant is rejected with 404 (the platform deliberately " +
					"does not distinguish \"not yours\" from \"does not exist\").",
				Optional: true, Computed: true, ElementType: types.StringType,
				// Unset means every device, so removing the attribute must clear the targeting rather
				// than keep the old, narrower list in state.
				// A set, not a list: the platform stores the ids de-duplicated and SORTED, so a list in
				// any other order would come back reordered and fail as an inconsistent result.
				Default:    setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})),
				Validators: []validator.Set{setvalidator.SizeAtMost(100)},
			},
			"endpoint_effect_mapping": schema.MapAttribute{
				MarkdownDescription: "Read-only. Per endpoint scope this policy opts into, the authored effects a " +
					"device enforces differently (authored → applied), e.g. " +
					"`{ tool_input = { tokenize = \"redact\" } }`. The format-preserving key never reaches a " +
					"device, so `tokenize` is always mapped. Empty when no endpoint scope is set.",
				Computed:    true,
				ElementType: types.MapType{ElemType: types.StringType},
			},

			"criteria": criteriaSchemaAttribute(criteriaSchemaOptions{
				Description: "ABAC criteria for conditional application. Each entry combines `conditions` " +
					"with a logical operator (`AND`/`OR`); multiple criteria entries are themselves ANDed.",
				TypeDescription:  "Criteria type. Allowed: `claims`, `context`, `request`, `time`. Server default applies if unset.",
				FieldDescription: "Field path to evaluate (e.g. `client_id`, `department`, `email`).",
				ValueExample:     "`jsonencode(\"legal\")`",
			}),

			"profile_count": schema.Int64Attribute{Computed: true},
			"created_at":    schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"created_by":    schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"updated_at":    schema.StringAttribute{Computed: true},
			"updated_by":    schema.StringAttribute{Computed: true},
		},
	}
}

func (r *DataProtectionPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DataProtectionPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	body := adminapi.DataProtectionPolicyCreate{Name: plan.Name.ValueString()}

	// Priority and DefaultEffect are required (non-pointer) on the create DTO;
	// fall back to the platform defaults when the (computed) attr is unset.
	if !plan.Priority.IsNull() && !plan.Priority.IsUnknown() {
		body.Priority = int32(plan.Priority.ValueInt64())
	} else {
		body.Priority = 100
	}
	defEffect := "redact"
	if !plan.DefaultEffect.IsNull() && !plan.DefaultEffect.IsUnknown() && plan.DefaultEffect.ValueString() != "" {
		defEffect = plan.DefaultEffect.ValueString()
	}
	body.DefaultEffect = gen.DataProtectionPolicyCreateRequestDefaultEffect(defEffect)

	setStringPtr(plan.Description, &body.Description)
	setBoolPtr(plan.Enabled, &body.Enabled)
	if !plan.EnabledProfiles.IsNull() && !plan.EnabledProfiles.IsUnknown() {
		s := stringListToSDK(ctx, plan.EnabledProfiles)
		body.EnabledProfiles = &s
	}
	body.EnabledDetectors = boolMapToSDK(ctx, plan.EnabledDetectors)
	body.DisabledDetectors = boolMapToSDK(ctx, plan.DisabledDetectors)
	body.DetectorThresholds = float64MapToSDK(ctx, plan.DetectorThresholds)
	body.DetectorConfigs = detectorConfigsToSDK(ctx, plan.DetectorConfigs, &resp.Diagnostics)
	body.Effects = stringMapToSDK(ctx, plan.Effects)
	setStringPtr(plan.BlockedMessage, &body.BlockedMessage)
	setStringPtr(plan.FpeKeyID, &body.FpeKeyId)
	if !plan.TweakScope.IsNull() && !plan.TweakScope.IsUnknown() && plan.TweakScope.ValueString() != "" {
		ts := gen.DataProtectionPolicyCreateRequestTweakScope(plan.TweakScope.ValueString())
		body.TweakScope = &ts
	}
	setBoolPtr(plan.ApplyToLlmInput, &body.ApplyToLlmInput)
	setBoolPtr(plan.ApplyToLlmOutput, &body.ApplyToLlmOutput)
	setBoolPtr(plan.ApplyToMcpInput, &body.ApplyToMcpInput)
	setBoolPtr(plan.ApplyToMcpOutput, &body.ApplyToMcpOutput)
	setBoolPtr(plan.ApplyToToolInput, &body.ApplyToToolInput)
	setBoolPtr(plan.ApplyToPromptInput, &body.ApplyToPromptInput)
	setBoolPtr(plan.ApplyToToolOutput, &body.ApplyToToolOutput)
	if !plan.ToolOutputUnscannedAction.IsNull() && !plan.ToolOutputUnscannedAction.IsUnknown() {
		a := gen.DataProtectionPolicyCreateRequestToolOutputUnscannedAction(plan.ToolOutputUnscannedAction.ValueString())
		body.ToolOutputUnscannedAction = &a
	}
	addInvalidDeviceGroups(&resp.Diagnostics, uuidSetToSDK(ctx, plan.DeviceGroupIDs, &body.DeviceGroupIds))
	crits, critDiags := criteriaListToSDK(plan.Criteria)
	resp.Diagnostics.Append(critDiags...)
	if len(crits) > 0 {
		body.Criteria = &crits
	}
	if resp.Diagnostics.HasError() {
		return
	}

	pol, err := r.sdk.DataProtectionPolicies().Create(ctx, tenantID, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create data protection policy", err)
		return
	}
	state := dataProtectionPolicyToModel(tenantID, pol, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DataProtectionPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DataProtectionPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	pol, err := r.sdk.DataProtectionPolicies().Get(ctx, tenantID, state.PolicyID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read data protection policy", err)
		return
	}
	refreshed := dataProtectionPolicyToModel(tenantID, pol, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *DataProtectionPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DataProtectionPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	body := adminapi.DataProtectionPolicyUpdate{}
	setStringPtr(plan.Name, &body.Name)
	setStringPtr(plan.Description, &body.Description)
	setInt32Ptr(plan.Priority, &body.Priority)
	setBoolPtr(plan.Enabled, &body.Enabled)
	if !plan.EnabledProfiles.IsNull() && !plan.EnabledProfiles.IsUnknown() {
		s := stringListToSDK(ctx, plan.EnabledProfiles)
		body.EnabledProfiles = &s
	}
	body.EnabledDetectors = boolMapToSDK(ctx, plan.EnabledDetectors)
	body.DisabledDetectors = boolMapToSDK(ctx, plan.DisabledDetectors)
	body.DetectorThresholds = float64MapToSDK(ctx, plan.DetectorThresholds)
	body.DetectorConfigs = detectorConfigsToSDK(ctx, plan.DetectorConfigs, &resp.Diagnostics)
	body.Effects = stringMapToSDK(ctx, plan.Effects)
	setStringPtr(plan.BlockedMessage, &body.BlockedMessage)
	setStringPtr(plan.FpeKeyID, &body.FpeKeyId)
	if !plan.DefaultEffect.IsNull() && !plan.DefaultEffect.IsUnknown() {
		de := gen.DataProtectionPolicyUpdateRequestDefaultEffect(plan.DefaultEffect.ValueString())
		body.DefaultEffect = &de
	}
	setStringPtr(plan.TweakScope, &body.TweakScope)
	setBoolPtr(plan.ApplyToLlmInput, &body.ApplyToLlmInput)
	setBoolPtr(plan.ApplyToLlmOutput, &body.ApplyToLlmOutput)
	setBoolPtr(plan.ApplyToMcpInput, &body.ApplyToMcpInput)
	setBoolPtr(plan.ApplyToMcpOutput, &body.ApplyToMcpOutput)
	setBoolPtr(plan.ApplyToToolInput, &body.ApplyToToolInput)
	setBoolPtr(plan.ApplyToPromptInput, &body.ApplyToPromptInput)
	setBoolPtr(plan.ApplyToToolOutput, &body.ApplyToToolOutput)
	if !plan.ToolOutputUnscannedAction.IsNull() && !plan.ToolOutputUnscannedAction.IsUnknown() {
		a := gen.DataProtectionPolicyUpdateRequestToolOutputUnscannedAction(plan.ToolOutputUnscannedAction.ValueString())
		body.ToolOutputUnscannedAction = &a
	}
	// Always sent when known: an empty list is how an update clears the targeting (the API
	// treats an omitted field as "unchanged").
	addInvalidDeviceGroups(&resp.Diagnostics, uuidSetToSDK(ctx, plan.DeviceGroupIDs, &body.DeviceGroupIds))
	crits, critDiags := criteriaListToSDK(plan.Criteria)
	resp.Diagnostics.Append(critDiags...)
	if len(crits) > 0 {
		body.Criteria = &crits
	}
	if resp.Diagnostics.HasError() {
		return
	}

	// Data protection policies carry no optimistic-concurrency token; the SDK
	// accepts an empty version (no If-Match) for symmetry with the other
	// policy sub-clients.
	pol, err := r.sdk.DataProtectionPolicies().Update(ctx, tenantID, state.PolicyID.ValueString(), "", body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update data protection policy", err)
		return
	}
	refreshed := dataProtectionPolicyToModel(tenantID, pol, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *DataProtectionPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DataProtectionPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	err := r.sdk.DataProtectionPolicies().Delete(ctx, tenantID, state.PolicyID.ValueString(), "")
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete data protection policy", err)
	}
}

func (r *DataProtectionPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, policyID string
	switch len(parts) {
	case 1:
		tenantID, policyID = r.tenantID, parts[0]
	case 2:
		tenantID, policyID = parts[0], parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>/<policy_id>` or configure `tenant_id` on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_id"), policyID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+policyID)...)
}

// addInvalidDeviceGroups reports malformed device_group_ids as an attribute error. Never
// skipped: an empty target list means every device, so dropping a bad id would widen the
// policy instead of narrowing it.
func addInvalidDeviceGroups(diags *diag.Diagnostics, invalid []string) {
	if len(invalid) == 0 {
		return
	}
	diags.AddAttributeError(path.Root("device_group_ids"), "Invalid device group id",
		fmt.Sprintf("device_group_ids must be UUIDs (ferentin_device_group.<name>.group_id); got %q.", invalid))
}

func (r *DataProtectionPolicyResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}
