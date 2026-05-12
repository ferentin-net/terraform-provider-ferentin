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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// LLMPolicyResource is the `ferentin_llm_policy` Terraform resource — an
// ABAC-style governance policy applied to LLM traffic. Exercises the
// nested-attribute patterns (criteria, limits) for the first time in the
// provider; see §6.4 of the design doc.
//
// Out of scope for v0.1: model_surface, enforcement, llm_logging_config —
// these are complex nested objects we'll expose in v0.2 once we have
// customer feedback on the shape. For now, callers needing them must edit
// in the console (and accept the resulting drift signal).
type LLMPolicyResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type LLMPolicyResourceModel struct {
	// Identity
	ID       types.String `tfsdk:"id"` // composite "<tenant>/<uuid>"
	TenantID types.String `tfsdk:"tenant_id"`

	// Required user-supplied
	Name types.String `tfsdk:"name"`

	// Optional user-supplied
	Description             types.String `tfsdk:"description"`
	Priority                types.Int64  `tfsdk:"priority"`
	Enabled                 types.Bool   `tfsdk:"enabled"`
	SystemPrompt            types.String `tfsdk:"system_prompt"`
	DeveloperPrompt         types.String `tfsdk:"developer_prompt"`
	Message                 types.String `tfsdk:"message"`
	DisallowClientDeveloper types.Bool   `tfsdk:"disallow_client_developer"`
	DisallowClientSystem    types.Bool   `tfsdk:"disallow_client_system"`
	PromptCacheEnabled      types.Bool   `tfsdk:"prompt_cache_enabled"`
	SummaryEnabled          types.Bool   `tfsdk:"summary_enabled"`
	UseGatewayPrompts       types.Bool   `tfsdk:"use_gateway_prompts"`

	ProviderInstances types.List `tfsdk:"provider_instances"` // []string

	Limits   *LLMPolicyLimitsModel   `tfsdk:"limits"`
	Criteria []LLMPolicyCriteriaModel `tfsdk:"criteria"`

	// Computed / server-set
	PolicyID          types.String `tfsdk:"policy_id"`
	Version           types.Int64  `tfsdk:"version"`
	CreatedAt         types.String `tfsdk:"created_at"`
	CreatedBy         types.String `tfsdk:"created_by"`
	UpdatedAt         types.String `tfsdk:"updated_at"`
	UpdatedBy         types.String `tfsdk:"updated_by"`
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

// LLMPolicyLimitsModel mirrors ModelSurfaceLimits. All fields optional; the
// server fills defaults on Create.
type LLMPolicyLimitsModel struct {
	MaxTokens             types.Int64 `tfsdk:"max_tokens"`
	MaxRequestKb          types.Int64 `tfsdk:"max_request_kb"`
	MaxFilesPerRequest    types.Int64 `tfsdk:"max_files_per_request"`
	MaxImagesPerRequest   types.Int64 `tfsdk:"max_images_per_request"`
	MaxImageBytes         types.Int64 `tfsdk:"max_image_bytes"`
	MaxAudioBytes         types.Int64 `tfsdk:"max_audio_bytes"`
	MaxAudioDurationSec   types.Int64 `tfsdk:"max_audio_duration_sec"`
	MaxToolArgumentsBytes types.Int64 `tfsdk:"max_tool_arguments_bytes"`
	RequestTimeoutMs      types.Int64 `tfsdk:"request_timeout_ms"`
	StreamTimeoutMs       types.Int64 `tfsdk:"stream_timeout_ms"`
	EnforceModelLimits    types.Bool  `tfsdk:"enforce_model_limits"`
}

// LLMPolicyCriteriaModel mirrors PolicyCriteria.
type LLMPolicyCriteriaModel struct {
	Operator    types.String                       `tfsdk:"operator"`
	Type        types.String                       `tfsdk:"type"`
	Description types.String                       `tfsdk:"description"`
	Conditions  []LLMPolicyCriteriaConditionModel `tfsdk:"conditions"`
}

// LLMPolicyCriteriaConditionModel mirrors CriteriaCondition. The `value`
// is a JSON-encoded string — the user writes `value = jsonencode("…")` or
// `value = jsonencode([...])`; the SDK marshals as `{"value": <decoded>}`
// to fit the platform's `map[string]interface{}` wire shape.
type LLMPolicyCriteriaConditionModel struct {
	Field         types.String `tfsdk:"field"`
	Operator      types.String `tfsdk:"operator"`
	Value         types.String `tfsdk:"value"`
	ValueType     types.String `tfsdk:"value_type"`
	CaseSensitive types.Bool   `tfsdk:"case_sensitive"`
	Description   types.String `tfsdk:"description"`
}

func NewLLMPolicyResource() resource.Resource {
	return &LLMPolicyResource{}
}

var (
	_ resource.Resource                = (*LLMPolicyResource)(nil)
	_ resource.ResourceWithConfigure   = (*LLMPolicyResource)(nil)
	_ resource.ResourceWithImportState = (*LLMPolicyResource)(nil)
)

func (r *LLMPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_llm_policy"
}

func (r *LLMPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *LLMPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An ABAC-style LLM governance policy. Combines targeting (criteria), enforcement " +
			"(limits, prompts), and routing (provider_instances). See §6.4 of the design doc for an example.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Composite Terraform resource ID `<tenant_id>/<policy_id>`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Tenant UUID. Defaults to the provider-level value; immutable per-policy.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Policy name. Unique within the tenant.",
				Required:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Optional description.",
				Optional:            true,
				Computed:            true,
			},
			"priority": schema.Int64Attribute{
				MarkdownDescription: "Evaluation priority. Lower number = higher priority. Conflicting policies " +
					"are resolved by priority.",
				Optional: true,
				Computed: true,
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the policy is enforced. Disable to stage changes without applying.",
				Optional:            true,
				Computed:            true,
			},
			"system_prompt": schema.StringAttribute{
				MarkdownDescription: "System prompt injected ahead of the user's prompt. Often loaded with `file()`.",
				Optional:            true,
				Computed:            true,
			},
			"developer_prompt": schema.StringAttribute{
				MarkdownDescription: "Developer prompt injected ahead of the user's prompt (separate channel from system).",
				Optional:            true,
				Computed:            true,
			},
			"message": schema.StringAttribute{
				MarkdownDescription: "Optional explanation surfaced to the agent when the policy applies.",
				Optional:            true,
				Computed:            true,
			},
			"disallow_client_developer": schema.BoolAttribute{
				MarkdownDescription: "When true, client-supplied developer prompts are stripped before forwarding.",
				Optional:            true,
				Computed:            true,
			},
			"disallow_client_system": schema.BoolAttribute{
				MarkdownDescription: "When true, client-supplied system prompts are stripped before forwarding.",
				Optional:            true,
				Computed:            true,
			},
			"prompt_cache_enabled": schema.BoolAttribute{
				MarkdownDescription: "Enable prompt-cache routing (provider-dependent).",
				Optional:            true,
				Computed:            true,
			},
			"summary_enabled": schema.BoolAttribute{
				MarkdownDescription: "Enable post-response summarization.",
				Optional:            true,
				Computed:            true,
			},
			"use_gateway_prompts": schema.BoolAttribute{
				MarkdownDescription: "When true, prepend the platform's gateway prompts to system/developer.",
				Optional:            true,
				Computed:            true,
			},
			"provider_instances": schema.ListAttribute{
				MarkdownDescription: "List of `instance_name`s (from `ferentin_llm_provider_instance.instance_name`) " +
					"this policy routes to. Order determines failover order at runtime.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
			},

			"limits": schema.SingleNestedAttribute{
				MarkdownDescription: "Per-request limits the platform enforces before forwarding to the provider.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"max_tokens": schema.Int64Attribute{
						MarkdownDescription: "Maximum output tokens per request.",
						Optional:            true,
					},
					"max_request_kb": schema.Int64Attribute{
						MarkdownDescription: "Maximum request payload size in kilobytes.",
						Optional:            true,
					},
					"max_files_per_request": schema.Int64Attribute{
						MarkdownDescription: "Maximum file attachments per request (0 = files disabled).",
						Optional:            true,
					},
					"max_images_per_request": schema.Int64Attribute{
						MarkdownDescription: "Maximum images per request (0 = images disabled).",
						Optional:            true,
					},
					"max_image_bytes": schema.Int64Attribute{
						MarkdownDescription: "Maximum total image bytes per request.",
						Optional:            true,
					},
					"max_audio_bytes": schema.Int64Attribute{
						MarkdownDescription: "Maximum total audio bytes per request (0 = audio disabled).",
						Optional:            true,
					},
					"max_audio_duration_sec": schema.Int64Attribute{
						MarkdownDescription: "Maximum audio duration in seconds (0 = audio disabled).",
						Optional:            true,
					},
					"max_tool_arguments_bytes": schema.Int64Attribute{
						MarkdownDescription: "Maximum total size of tool arguments in bytes.",
						Optional:            true,
					},
					"request_timeout_ms": schema.Int64Attribute{
						MarkdownDescription: "Request timeout in milliseconds.",
						Optional:            true,
					},
					"stream_timeout_ms": schema.Int64Attribute{
						MarkdownDescription: "Streaming-response timeout in milliseconds.",
						Optional:            true,
					},
					"enforce_model_limits": schema.BoolAttribute{
						MarkdownDescription: "Validate requests against the model's context-window limits from the catalog.",
						Optional:            true,
					},
				},
			},

			"criteria": schema.ListNestedAttribute{
				MarkdownDescription: "ABAC criteria for matching requests. Each entry combines `conditions` with " +
					"a logical operator (AND / OR). Multiple criteria entries are themselves ANDed.",
				Optional: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"operator": schema.StringAttribute{
							MarkdownDescription: "Logical operator joining the conditions: `AND` or `OR`.",
							Required:            true,
						},
						"type": schema.StringAttribute{
							MarkdownDescription: "Criteria type. Typically `user` or `request`. Server default applies if unset.",
							Optional:            true,
							Computed:            true,
						},
						"description": schema.StringAttribute{
							MarkdownDescription: "Optional human-readable description.",
							Optional:            true,
						},
						"conditions": schema.ListNestedAttribute{
							MarkdownDescription: "Conditions evaluated under the parent `operator`.",
							Required:            true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"field": schema.StringAttribute{
										MarkdownDescription: "Field path to evaluate (e.g. `user.department`).",
										Required:            true,
									},
									"operator": schema.StringAttribute{
										MarkdownDescription: "Comparison operator (`equals`, `in`, `lt`, `gt`, …).",
										Required:            true,
									},
									"value": schema.StringAttribute{
										MarkdownDescription: "JSON-encoded value to compare against. Examples: " +
											"`jsonencode(\"engineering\")`, `jsonencode([\"a\",\"b\"])`, `jsonencode(100)`.",
										Optional: true,
									},
									"value_type": schema.StringAttribute{
										MarkdownDescription: "Optional type hint for the value (`string`, `int`, `list`, …).",
										Optional:            true,
									},
									"case_sensitive": schema.BoolAttribute{
										MarkdownDescription: "For string operations.",
										Optional:            true,
									},
									"description": schema.StringAttribute{
										MarkdownDescription: "Optional description.",
										Optional:            true,
									},
								},
							},
						},
					},
				},
			},

			// Computed / server-set
			"policy_id": schema.StringAttribute{
				MarkdownDescription: "Server-generated UUID for this policy.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version (platform #649) for If-Match.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp of creation.",
				Computed:            true,
			},
			"created_by": schema.StringAttribute{
				MarkdownDescription: "Principal that created the policy.",
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

func (r *LLMPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan LLMPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(plan.TenantID)
	create, diags := plan.toCreateRequest(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	pol, err := r.sdk.LLMPolicies().Create(ctx, tenantID, create)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create LLM policy", err.Error())
		return
	}

	state := llmPolicyToModel(ctx, tenantID, pol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *LLMPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state LLMPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	policyID := state.PolicyID.ValueString()

	pol, err := r.sdk.LLMPolicies().Get(ctx, tenantID, policyID)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read LLM policy", err.Error())
		return
	}

	refreshed := llmPolicyToModel(ctx, tenantID, pol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *LLMPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state LLMPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	policyID := state.PolicyID.ValueString()
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)

	update, diags := plan.toUpdateRequest(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	pol, err := r.sdk.LLMPolicies().Update(ctx, tenantID, policyID, version, update)
	if err != nil {
		if errors.Is(err, adminapi.ErrPreconditionFailed) {
			resp.Diagnostics.AddError(
				"LLM policy changed since last refresh",
				"The policy's version on the platform differs from Terraform state. "+
					"Run `terraform refresh` and re-plan to pick up out-of-band edits.",
			)
			return
		}
		resp.Diagnostics.AddError("Failed to update LLM policy", err.Error())
		return
	}

	refreshed := llmPolicyToModel(ctx, tenantID, pol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *LLMPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state LLMPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	policyID := state.PolicyID.ValueString()
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)

	err := r.sdk.LLMPolicies().Delete(ctx, tenantID, policyID, version)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			return
		}
		if errors.Is(err, adminapi.ErrPreconditionFailed) {
			resp.Diagnostics.AddError("LLM policy changed since last refresh",
				"Refresh and re-plan.")
			return
		}
		resp.Diagnostics.AddError("Failed to delete LLM policy", err.Error())
	}
}

func (r *LLMPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, policyID string
	switch len(parts) {
	case 1:
		tenantID = r.tenantID
		policyID = parts[0]
	case 2:
		tenantID = parts[0]
		policyID = parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError(
			"Cannot determine tenant for import",
			"Pass `<tenant_id>/<policy_id>` or configure `tenant_id` on the provider block.",
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_id"), policyID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+policyID)...)
}

func (r *LLMPolicyResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}
