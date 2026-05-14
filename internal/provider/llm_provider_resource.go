package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// LLMProviderResource is the `ferentin_llm_provider`
// Terraform resource — a tenant-scoped binding between an LLM provider
// (catalog entry, e.g. "anthropic") and the credentials / config used to
// reach it. See §6.4 of the design doc.
//
// The secret attributes (`api_key`, `credentials`, `external_id`) are
// WriteOnly — the value flows from config to the provider during apply but
// is never written to state. To rotate a secret, bump the companion
// `*_wo_version` integer.
type LLMProviderResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type LLMProviderResourceModel struct {
	// Identity
	ID       types.String `tfsdk:"id"` // composite "<tenant>/<uuid>"
	TenantID types.String `tfsdk:"tenant_id"`

	// Required user-supplied
	ProviderType types.String `tfsdk:"provider_type"` // catalog slug ("anthropic", "openai", …)
	InstanceName types.String `tfsdk:"instance_name"`

	// Optional user-supplied
	DisplayName                    types.String              `tfsdk:"display_name"`
	Description                    types.String              `tfsdk:"description"`
	APIKey                         types.String              `tfsdk:"api_key"`            // WriteOnly
	APIKeyWOVersion                types.Int64               `tfsdk:"api_key_wo_version"` // companion to api_key
	AuthType                       types.String              `tfsdk:"auth_type"`
	Credentials                    types.String              `tfsdk:"credentials"` // WriteOnly
	CredentialsWOVersion           types.Int64               `tfsdk:"credentials_wo_version"`
	HealthCheckURL                 types.String              `tfsdk:"health_check_url"`
	Enabled                        types.Bool                `tfsdk:"enabled"`
	Priority                       types.Int64               `tfsdk:"priority"`
	ImpersonateServiceAccountEmail types.String              `tfsdk:"impersonate_service_account_email"`
	AWSRegion                      types.String              `tfsdk:"aws_region"`
	RoleARN                        types.String              `tfsdk:"role_arn"`
	ExternalID                     types.String              `tfsdk:"external_id"` // WriteOnly
	ExternalIDWOVersion            types.Int64               `tfsdk:"external_id_wo_version"`
	SessionDurationMinutes         types.Int64               `tfsdk:"session_duration_minutes"`
	ModelConstraints               *LLMModelConstraintsModel `tfsdk:"model_constraints"`

	// Computed / server-set
	InstanceID          types.String `tfsdk:"instance_id"` // server-generated UUID
	Version             types.Int64  `tfsdk:"version"`
	APIKeyConfigured    types.Bool   `tfsdk:"api_key_configured"`
	HealthStatus        types.String `tfsdk:"health_status"`
	AvailableForRouting types.Bool   `tfsdk:"available_for_routing"`
	CreatedAt           types.String `tfsdk:"created_at"`
	CreatedBy           types.String `tfsdk:"created_by"`
	ManagedBy           types.String `tfsdk:"managed_by"`
	ManagedByClientID   types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule     types.String `tfsdk:"managed_by_module"`
	LastModifiedBy      types.String `tfsdk:"last_modified_by"`
}

// LLMModelConstraintsModel maps to provider_config.model_constraints on the
// wire (a JSONB nested object). The platform reads
// provider_config->model_constraints->models when filtering instances by
// model availability at runtime.
type LLMModelConstraintsModel struct {
	Mode   types.String `tfsdk:"mode"`   // all | allowlist | blocklist
	Models types.List   `tfsdk:"models"` // []string
}

func NewLLMProviderResource() resource.Resource {
	return &LLMProviderResource{}
}

var (
	_ resource.Resource                     = (*LLMProviderResource)(nil)
	_ resource.ResourceWithConfigure        = (*LLMProviderResource)(nil)
	_ resource.ResourceWithImportState      = (*LLMProviderResource)(nil)
	_ resource.ResourceWithConfigValidators = (*LLMProviderResource)(nil)
)

func (r *LLMProviderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_llm_provider"
}

// ConfigValidators surfaces auth-shape mistakes at plan time.
//   - `aws_region` + `role_arn` must come as a pair (AWS IAM-role auth);
//     one without the other is meaningless and would surface as an opaque
//     platform 400 on apply.
//   - `external_id` is only meaningful with `role_arn` (cross-account STS).
func (r *LLMProviderResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.RequiredTogether(
			path.MatchRoot("aws_region"),
			path.MatchRoot("role_arn"),
		),
	}
}

func (r *LLMProviderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(ProviderData)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected ProviderData, got %T", req.ProviderData),
		)
		return
	}
	r.sdk = pd.SDK
	r.tenantID = pd.TenantID
}

func (r *LLMProviderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A tenant binding between an LLM provider (catalog entry — see `data \"ferentin_llm_provider\"`) " +
			"and the credentials / configuration the platform uses to reach it. Multiple instances per provider " +
			"are allowed (e.g., per-region, per-environment).\n\n" +
			"## Import\n\n" +
			"Existing instances can be imported using `<tenant_id>/<instance_id>` " +
			"(or `<instance_id>` alone when the provider's default `tenant_id` matches):\n\n" +
			"```\n" +
			"terraform import ferentin_llm_provider.example <tenant_id>/<instance_id>\n" +
			"```\n\n" +
			"After import, set `api_key` (and any other WriteOnly attrs) and bump the `*_wo_version` " +
			"companions to push the secret on the next apply.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Composite Terraform resource ID `<tenant_id>/<instance_id>`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Tenant UUID. Defaults to provider-level `tenant_id`; immutable per-instance.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"provider_type": schema.StringAttribute{
				MarkdownDescription: "Catalog slug of the LLM provider (`anthropic`, `openai`, `google-vertex`, …). " +
					"Pull from `data \"ferentin_llm_provider\"`. Immutable post-create.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"instance_name": schema.StringAttribute{
				MarkdownDescription: "Human-readable per-instance name (e.g. `anthropic-prod-us`).",
				Required:            true,
			},
			"display_name": schema.StringAttribute{
				MarkdownDescription: "Display name shown in the admin console.",
				Optional:            true,
				Computed:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Optional description.",
				Optional:            true,
				Computed:            true,
			},
			"api_key": schema.StringAttribute{
				MarkdownDescription: "Bearer API key for the provider. **WriteOnly** (Terraform 1.11+) — the " +
					"value flows through to the provider during apply but never enters Terraform state. " +
					"The server stores an encrypted form; the wire response only exposes `api_key_configured`. " +
					"Bump `api_key_wo_version` to force a re-send when the upstream secret rotates.",
				Optional:  true,
				WriteOnly: true,
				Sensitive: true,
			},
			"api_key_wo_version": schema.Int64Attribute{
				MarkdownDescription: "Companion to write-only `api_key`. Bump this integer to force the " +
					"provider to re-send the (potentially changed) `api_key` on the next apply.",
				Optional: true,
			},
			"auth_type": schema.StringAttribute{
				MarkdownDescription: "Authentication method for this provider. Allowed values vary per provider type; " +
					"common: `api_key`, `oauth2_client_credentials`, `aws_iam_role`, `gcp_service_account`.",
				Optional: true,
				Computed: true,
			},
			"credentials": schema.StringAttribute{
				MarkdownDescription: "Free-form credentials blob for auth types that aren't a single API key " +
					"(e.g. GCP service-account JSON). **WriteOnly** — flows through but never enters state. " +
					"Bump `credentials_wo_version` to force a re-send.",
				Optional:  true,
				WriteOnly: true,
				Sensitive: true,
			},
			"credentials_wo_version": schema.Int64Attribute{
				MarkdownDescription: "Companion to write-only `credentials`. Bump to force re-send.",
				Optional:            true,
			},
			"health_check_url": schema.StringAttribute{
				MarkdownDescription: "Optional custom health-check URL.",
				Optional:            true,
				Computed:            true,
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "When false, the instance is registered but not used for routing. Default `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			"priority": schema.Int64Attribute{
				MarkdownDescription: "Routing priority (lower is higher-priority).",
				Optional:            true,
				Computed:            true,
			},
			"impersonate_service_account_email": schema.StringAttribute{
				MarkdownDescription: "GCP service-account email for impersonation (Vertex AI flows).",
				Optional:            true,
				Computed:            true,
			},
			"aws_region": schema.StringAttribute{
				MarkdownDescription: "AWS region (Bedrock / IAM flows).",
				Optional:            true,
				Computed:            true,
			},
			"role_arn": schema.StringAttribute{
				MarkdownDescription: "AWS IAM role ARN to assume.",
				Optional:            true,
				Computed:            true,
			},
			"external_id": schema.StringAttribute{
				MarkdownDescription: "External ID for cross-account IAM assumption. **WriteOnly** — flows " +
					"through but never enters state. Bump `external_id_wo_version` to force a re-send.",
				Optional:  true,
				WriteOnly: true,
				Sensitive: true,
			},
			"external_id_wo_version": schema.Int64Attribute{
				MarkdownDescription: "Companion to write-only `external_id`. Bump to force re-send.",
				Optional:            true,
			},
			"session_duration_minutes": schema.Int64Attribute{
				MarkdownDescription: "STS session duration in minutes (IAM flows).",
				Optional:            true,
				Computed:            true,
			},
			"model_constraints": schema.SingleNestedAttribute{
				MarkdownDescription: "Restrict which catalog models this instance exposes to runtime requests. " +
					"Pair `mode = \"allowlist\"` with an explicit `models` list to deny requests for any model " +
					"not in the set — the most common pattern when an instance should pin to a single OpenAI / " +
					"Anthropic model (e.g. `gpt-5.5`, `claude-sonnet-4`). Persisted on the platform as " +
					"`provider_config.model_constraints` (JSONB); echoed back on Read so drift detection works.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"mode": schema.StringAttribute{
						MarkdownDescription: "Set to `\"allowlist\"` and provide `models`. The platform " +
							"accepts other modes for advanced use cases but `allowlist` is the supported shape " +
							"for the Terraform provider.",
						Required: true,
						Validators: []validator.String{
							stringvalidator.OneOf("all", "allowlist", "blocklist"),
						},
					},
					"models": schema.ListAttribute{
						MarkdownDescription: "Model IDs that this instance is permitted to serve. " +
							"E.g. `[\"gpt-5.5\"]` to pin a single model, or `[\"gpt-5.5\", \"gpt-4o\"]` to " +
							"allow a small set.",
						Optional:    true,
						ElementType: types.StringType,
					},
				},
			},

			// Computed / server-set
			"instance_id": schema.StringAttribute{
				MarkdownDescription: "Server-generated UUID for this instance.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version (platform #649) for If-Match.",
				Computed:            true,
			},
			"api_key_configured": schema.BoolAttribute{
				MarkdownDescription: "True when an API key has been set on this instance. The value itself is never returned.",
				Computed:            true,
			},
			"health_status": schema.StringAttribute{
				MarkdownDescription: "Runtime health status from the most recent check.",
				Computed:            true,
			},
			"available_for_routing": schema.BoolAttribute{
				MarkdownDescription: "Whether the instance is currently eligible for routing.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp of creation.",
				Computed:            true,
			},
			"created_by": schema.StringAttribute{
				MarkdownDescription: "Principal that created the instance.",
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
				MarkdownDescription: "Module label the creator passed via `X-Ferentin-Managed-By-Module`.",
				Computed:            true,
			},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent writer; diverges from `managed_by` when an " +
					"out-of-band writer edits the resource — the drift signal per platform #651.",
				Computed: true,
			},
		},
	}
}

func (r *LLMProviderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan LLMProviderResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// WriteOnly attrs are stripped from Plan; read them from Config.
	var config LLMProviderResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(plan.TenantID)

	create := adminapi.LLMProviderInstanceCreate{
		// provider_type is a REQUIRED field on the wire (gen.ProviderInstanceCreateRequest.ProviderType
		// is a plain string, not a pointer). Earlier comment claimed it
		// wasn't on the create DTO — that was wrong; the platform 400s with
		// `fieldErrors:{providerType:"Provider type is required"}` without it.
		ProviderType: plan.ProviderType.ValueString(),
	}
	v := plan.InstanceName.ValueString()
	create.InstanceName = &v
	setStringPtr(plan.DisplayName, &create.DisplayName)
	setStringPtr(plan.Description, &create.Description)
	// WriteOnly sources — read from Config.
	setStringPtr(config.APIKey, &create.ApiKey)
	setStringPtr(config.Credentials, &create.Credentials)
	setStringPtr(config.ExternalID, &create.ExternalId)
	setStringPtr(plan.AuthType, &create.AuthType)
	setStringPtr(plan.HealthCheckURL, &create.HealthCheckUrl)
	setStringPtr(plan.ImpersonateServiceAccountEmail, &create.ImpersonateServiceAccountEmail)
	setStringPtr(plan.AWSRegion, &create.AwsRegion)
	setStringPtr(plan.RoleARN, &create.RoleArn)
	setBoolPtr(plan.Enabled, &create.Enabled)
	setInt32Ptr(plan.Priority, &create.Priority)
	setInt32Ptr(plan.SessionDurationMinutes, &create.SessionDurationMinutes)
	if pc, err := buildProviderConfig(ctx, plan.ModelConstraints); err != nil {
		resp.Diagnostics.AddError("Invalid model_constraints", err.Error())
		return
	} else if pc != nil {
		create.ProviderConfig = pc
	}

	inst, err := r.sdk.LLMProviderInstances().Create(ctx, tenantID, create)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create LLM provider instance", err)
		return
	}

	state := llmInstanceToModel(tenantID, plan, inst)
	// Plan-only attrs that the wire doesn't echo: carry forward the user's
	// last values so a no-op plan stays no-op. WriteOnly attrs (api_key,
	// credentials, external_id) are NOT carried — they're config-only.
	state.APIKeyWOVersion = plan.APIKeyWOVersion
	state.CredentialsWOVersion = plan.CredentialsWOVersion
	state.ExternalIDWOVersion = plan.ExternalIDWOVersion
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *LLMProviderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state LLMProviderResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	instanceID := state.InstanceID.ValueString()

	inst, err := r.sdk.LLMProviderInstances().Get(ctx, tenantID, instanceID)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read LLM provider instance", err)
		return
	}

	refreshed := llmInstanceToModel(tenantID, state, inst)
	// Carry forward the WO version companions — they live in state but the
	// server doesn't echo them; preserving keeps the plan a no-op.
	refreshed.APIKeyWOVersion = state.APIKeyWOVersion
	refreshed.CredentialsWOVersion = state.CredentialsWOVersion
	refreshed.ExternalIDWOVersion = state.ExternalIDWOVersion
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *LLMProviderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, config LLMProviderResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	instanceID := state.InstanceID.ValueString()
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)

	update := adminapi.LLMProviderInstanceUpdate{}
	setStringPtr(plan.InstanceName, &update.InstanceName)
	setStringPtr(plan.DisplayName, &update.DisplayName)
	setStringPtr(plan.Description, &update.Description)
	setStringPtr(plan.AuthType, &update.AuthType)
	setStringPtr(plan.HealthCheckURL, &update.HealthCheckUrl)
	setStringPtr(plan.ImpersonateServiceAccountEmail, &update.ImpersonateServiceAccountEmail)
	setBoolPtr(plan.Enabled, &update.Enabled)
	setInt32Ptr(plan.Priority, &update.Priority)
	if pc, err := buildProviderConfig(ctx, plan.ModelConstraints); err != nil {
		resp.Diagnostics.AddError("Invalid model_constraints", err.Error())
		return
	} else if pc != nil {
		update.ProviderConfig = pc
	}

	// WriteOnly secrets are only sent when the user bumped the companion
	// *_wo_version. Otherwise we leave them nil and the server preserves
	// the existing encrypted value. Detecting "version bumped" by comparing
	// plan-time vs prior state.
	if !plan.APIKeyWOVersion.Equal(state.APIKeyWOVersion) {
		setStringPtr(config.APIKey, &update.ApiKey)
	}
	if !plan.CredentialsWOVersion.Equal(state.CredentialsWOVersion) {
		setStringPtr(config.Credentials, &update.Credentials)
	}

	inst, err := r.sdk.LLMProviderInstances().Update(ctx, tenantID, instanceID, version, update)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update LLM provider instance", err)
		return
	}

	refreshed := llmInstanceToModel(tenantID, plan, inst)
	refreshed.APIKeyWOVersion = plan.APIKeyWOVersion
	refreshed.CredentialsWOVersion = plan.CredentialsWOVersion
	refreshed.ExternalIDWOVersion = plan.ExternalIDWOVersion
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *LLMProviderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state LLMProviderResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(state.TenantID)
	instanceID := state.InstanceID.ValueString()
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)

	err := r.sdk.LLMProviderInstances().Delete(ctx, tenantID, instanceID, version)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to delete LLM provider instance", err)
	}
}

// ImportState accepts "<tenant_id>/<instance_id>" or just "<instance_id>"
// (falling back to provider-level tenant_id).
func (r *LLMProviderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, instanceID string
	switch len(parts) {
	case 1:
		tenantID = r.tenantID
		instanceID = parts[0]
	case 2:
		tenantID = parts[0]
		instanceID = parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError(
			"Cannot determine tenant for import",
			"Pass `<tenant_id>/<instance_id>` or configure `tenant_id` on the provider block.",
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), instanceID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+instanceID)...)
}

func (r *LLMProviderResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// llmInstanceToModel maps SDK response → Terraform state. WriteOnly inputs
// (api_key, credentials, external_id) are not written to state by design —
// state holds Null for them; their *_wo_version companions track rotation
// intent and are carried over by the caller.
//
// `prior` is the plan (on Create/Update) or state (on Read). The mapper
// carries forward any Optional+Computed field the platform doesn't echo
// back (auth_type, aws_region, role_arn, session_duration_minutes,
// impersonate_service_account_email). Without this, Terraform's framework
// rejects the apply with "Provider produced inconsistent result after
// apply: was X, but now null" the moment the user sets any of these and
// the platform omits them from its response.
func llmInstanceToModel(
	tenantID string,
	prior LLMProviderResourceModel,
	inst *adminapi.LLMProviderInstance,
) LLMProviderResourceModel {
	// fallbackStr returns the server value when non-nil, else carries the
	// prior model value (plan on Create/Update, state on Read). Unknown
	// is demoted to Null because Computed fields the user didn't set show
	// up as Unknown in the plan and the framework rejects Unknown in
	// post-apply state with "all values must be known after apply".
	fallbackStr := func(server *string, fallback types.String) types.String {
		if server != nil {
			return types.StringValue(*server)
		}
		if fallback.IsUnknown() {
			return types.StringNull()
		}
		return fallback
	}
	fallbackInt64 := func(server *int32, fallback types.Int64) types.Int64 {
		if server != nil {
			return types.Int64Value(int64(*server))
		}
		if fallback.IsUnknown() {
			return types.Int64Null()
		}
		return fallback
	}

	m := LLMProviderResourceModel{
		TenantID:     types.StringValue(tenantID),
		ProviderType: prior.ProviderType,
		APIKey:       types.StringNull(),
		Credentials:  types.StringNull(),
		ExternalID:   types.StringNull(),
	}

	if inst.Id != nil {
		m.InstanceID = types.StringValue(inst.Id.String())
	} else {
		m.InstanceID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.InstanceID.ValueString())

	// LLMProviderInstance doesn't have a Version field in the response
	// (PartialUpdate uses path semantics, not optimistic-concurrency).
	// Default to 0 — the SDK's formatIfMatch accepts it as a weak-etag literal.
	m.Version = types.Int64Value(0)

	m.InstanceName = strPtrToTF(inst.InstanceName)
	m.DisplayName = strPtrToTF(inst.DisplayName)
	m.Description = strPtrToTF(inst.Description)
	m.AuthType = fallbackStr(inst.AuthType, prior.AuthType)
	m.HealthCheckURL = strPtrToTF(inst.HealthCheckUrl)
	m.Enabled = boolPtrOrDefault(inst.Enabled)
	m.Priority = int32PtrToTF(inst.Priority)
	m.ImpersonateServiceAccountEmail = fallbackStr(inst.ImpersonateServiceAccountEmail, prior.ImpersonateServiceAccountEmail)
	// AWS-region / role-arn / session-duration are user-set fields the
	// response doesn't echo back. Use the helpers so unset (Unknown) values
	// land as Null rather than Unknown.
	m.AWSRegion = fallbackStr(nil, prior.AWSRegion)
	m.RoleARN = fallbackStr(nil, prior.RoleARN)
	m.SessionDurationMinutes = fallbackInt64(nil, prior.SessionDurationMinutes)

	m.APIKeyConfigured = boolPtrOrDefault(inst.ApiKeyConfigured)
	m.HealthStatus = enumPtrToTF(inst.HealthStatus)
	m.AvailableForRouting = boolPtrOrDefault(inst.AvailableForRouting)
	m.CreatedAt = timePtrToTF(inst.CreatedAt)
	m.CreatedBy = strPtrToTF(inst.CreatedBy)
	m.ManagedBy = enumPtrToTF(inst.ManagedBy)
	m.ManagedByClientID = strPtrToTF(inst.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(inst.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(inst.LastModifiedBy)

	// model_constraints lives under provider_config.model_constraints on the
	// wire. Reverse-decode if the platform echoed it; otherwise carry the
	// prior model's value forward.
	m.ModelConstraints = readModelConstraintsFromProviderConfig(inst.ProviderConfig, prior.ModelConstraints)

	return m
}

// buildProviderConfig converts the user-facing model_constraints attribute
// into the platform's provider_config JSONB shape. Returns nil when the
// user didn't set the attribute — callers leave provider_config unset so
// the platform retains existing config on Update.
func buildProviderConfig(ctx context.Context, mc *LLMModelConstraintsModel) (*map[string]interface{}, error) {
	if mc == nil {
		return nil, nil
	}
	inner := map[string]interface{}{
		"mode": mc.Mode.ValueString(),
	}
	if !mc.Models.IsNull() && !mc.Models.IsUnknown() {
		var models []string
		diags := mc.Models.ElementsAs(ctx, &models, false)
		if diags.HasError() {
			return nil, fmt.Errorf("models: %s", diags.Errors()[0].Detail())
		}
		inner["models"] = models
	}
	pc := map[string]interface{}{
		"model_constraints": inner,
	}
	return &pc, nil
}

// readModelConstraintsFromProviderConfig pulls the user-facing nested model
// out of the platform's provider_config JSONB. JSON unmarshaling lands the
// inner map as map[string]interface{} and the `models` array as
// []interface{}; we type-assert and coerce element-by-element back to
// strings. When the server omits the section, carry forward the prior
// model so Optional+Computed semantics hold.
func readModelConstraintsFromProviderConfig(
	pc *map[string]interface{},
	prior *LLMModelConstraintsModel,
) *LLMModelConstraintsModel {
	if pc == nil {
		return prior
	}
	rawMC, ok := (*pc)["model_constraints"]
	if !ok {
		return prior
	}
	mc, ok := rawMC.(map[string]interface{})
	if !ok {
		return prior
	}
	out := &LLMModelConstraintsModel{
		Mode:   types.StringNull(),
		Models: types.ListNull(types.StringType),
	}
	if v, ok := mc["mode"].(string); ok {
		out.Mode = types.StringValue(v)
	}
	if raw, ok := mc["models"].([]interface{}); ok {
		elems := make([]attr.Value, 0, len(raw))
		for _, x := range raw {
			if s, ok := x.(string); ok {
				elems = append(elems, types.StringValue(s))
			}
		}
		lv, _ := types.ListValue(types.StringType, elems)
		out.Models = lv
	}
	return out
}
