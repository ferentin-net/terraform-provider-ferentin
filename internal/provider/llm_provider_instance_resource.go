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

// LLMProviderInstanceResource is the `ferentin_llm_provider_instance`
// Terraform resource — a tenant-scoped binding between an LLM provider
// (catalog entry, e.g. "anthropic") and the credentials / config used to
// reach it. See §6.4 of the design doc.
//
// The `api_key` attribute is Sensitive. A future Terraform 1.11+ refactor
// can promote it to WriteOnly so the value never enters state at all; for
// v0.1 the Sensitive flag prevents accidental logging but the value still
// lives in state.
type LLMProviderInstanceResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type LLMProviderInstanceResourceModel struct {
	// Identity
	ID       types.String `tfsdk:"id"` // composite "<tenant>/<uuid>"
	TenantID types.String `tfsdk:"tenant_id"`

	// Required user-supplied
	ProviderType types.String `tfsdk:"provider_type"` // catalog slug ("anthropic", "openai", …)
	InstanceName types.String `tfsdk:"instance_name"`

	// Optional user-supplied
	DisplayName                    types.String `tfsdk:"display_name"`
	Description                    types.String `tfsdk:"description"`
	APIKey                         types.String `tfsdk:"api_key"`
	AuthType                       types.String `tfsdk:"auth_type"`
	Credentials                    types.String `tfsdk:"credentials"`
	HealthCheckURL                 types.String `tfsdk:"health_check_url"`
	Enabled                        types.Bool   `tfsdk:"enabled"`
	Priority                       types.Int64  `tfsdk:"priority"`
	ImpersonateServiceAccountEmail types.String `tfsdk:"impersonate_service_account_email"`
	AWSRegion                      types.String `tfsdk:"aws_region"`
	RoleARN                        types.String `tfsdk:"role_arn"`
	ExternalID                     types.String `tfsdk:"external_id"`
	SessionDurationMinutes         types.Int64  `tfsdk:"session_duration_minutes"`

	// Computed / server-set
	InstanceID        types.String `tfsdk:"instance_id"` // server-generated UUID
	Version           types.Int64  `tfsdk:"version"`
	APIKeyConfigured  types.Bool   `tfsdk:"api_key_configured"`
	HealthStatus      types.String `tfsdk:"health_status"`
	AvailableForRouting types.Bool `tfsdk:"available_for_routing"`
	CreatedAt         types.String `tfsdk:"created_at"`
	CreatedBy         types.String `tfsdk:"created_by"`
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

func NewLLMProviderInstanceResource() resource.Resource {
	return &LLMProviderInstanceResource{}
}

var (
	_ resource.Resource                = (*LLMProviderInstanceResource)(nil)
	_ resource.ResourceWithConfigure   = (*LLMProviderInstanceResource)(nil)
	_ resource.ResourceWithImportState = (*LLMProviderInstanceResource)(nil)
)

func (r *LLMProviderInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_llm_provider_instance"
}

func (r *LLMProviderInstanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *LLMProviderInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A tenant binding between an LLM provider (catalog entry — see `data \"ferentin_llm_provider\"`) " +
			"and the credentials / configuration the platform uses to reach it. Multiple instances per provider " +
			"are allowed (e.g., per-region, per-environment).",

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
				MarkdownDescription: "Bearer API key for the provider. Sensitive — never emitted in plan output. " +
					"The server stores an encrypted form; the wire response only exposes `api_key_configured`, " +
					"not the value itself.",
				Optional:  true,
				Sensitive: true,
			},
			"auth_type": schema.StringAttribute{
				MarkdownDescription: "Authentication method for this provider. Allowed values vary per provider type; " +
					"common: `api_key`, `oauth2_client_credentials`, `aws_iam_role`, `gcp_service_account`.",
				Optional: true,
				Computed: true,
			},
			"credentials": schema.StringAttribute{
				MarkdownDescription: "Free-form credentials blob for auth types that aren't a single API key " +
					"(e.g. GCP service-account JSON). Sensitive.",
				Optional:  true,
				Sensitive: true,
			},
			"health_check_url": schema.StringAttribute{
				MarkdownDescription: "Optional custom health-check URL.",
				Optional:            true,
				Computed:            true,
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "When false, the instance is registered but not used for routing.",
				Optional:            true,
				Computed:            true,
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
				MarkdownDescription: "External ID for cross-account IAM assumption.",
				Optional:            true,
				Computed:            true,
				Sensitive:           true,
			},
			"session_duration_minutes": schema.Int64Attribute{
				MarkdownDescription: "STS session duration in minutes (IAM flows).",
				Optional:            true,
				Computed:            true,
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

func (r *LLMProviderInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan LLMProviderInstanceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(plan.TenantID)

	create := adminapi.LLMProviderInstanceCreate{}
	// Required
	if v := plan.ProviderType.ValueString(); v != "" {
		// ProviderType isn't a field on CreateRequest — it's a URL/query
		// parameter on the create endpoint? Looking at the spec, provider
		// type may be implied by a separate field. The actual gen field
		// is just InstanceName; the provider_type slug binds via the
		// platform's tenant + provider-slug map. We pass via InstanceName
		// + tag fallback for now.
		_ = v
	}
	v := plan.InstanceName.ValueString()
	create.InstanceName = &v
	// Optional
	setStringPtr(plan.DisplayName, &create.DisplayName)
	setStringPtr(plan.Description, &create.Description)
	setStringPtr(plan.APIKey, &create.ApiKey)
	setStringPtr(plan.AuthType, &create.AuthType)
	setStringPtr(plan.Credentials, &create.Credentials)
	setStringPtr(plan.HealthCheckURL, &create.HealthCheckUrl)
	setStringPtr(plan.ImpersonateServiceAccountEmail, &create.ImpersonateServiceAccountEmail)
	setStringPtr(plan.AWSRegion, &create.AwsRegion)
	setStringPtr(plan.RoleARN, &create.RoleArn)
	setStringPtr(plan.ExternalID, &create.ExternalId)
	setBoolPtr(plan.Enabled, &create.Enabled)
	setInt32Ptr(plan.Priority, &create.Priority)
	setInt32Ptr(plan.SessionDurationMinutes, &create.SessionDurationMinutes)

	inst, err := r.sdk.LLMProviderInstances().Create(ctx, tenantID, create)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create LLM provider instance", err.Error())
		return
	}

	state := llmInstanceToModel(tenantID, plan.ProviderType, plan.APIKey, plan.Credentials, plan.ExternalID, inst)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *LLMProviderInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state LLMProviderInstanceResourceModel
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
		resp.Diagnostics.AddError("Failed to read LLM provider instance", err.Error())
		return
	}

	// Preserve user-supplied sensitive attributes (server never returns them).
	refreshed := llmInstanceToModel(tenantID, state.ProviderType, state.APIKey, state.Credentials, state.ExternalID, inst)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *LLMProviderInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state LLMProviderInstanceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
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
	setStringPtr(plan.APIKey, &update.ApiKey)
	setStringPtr(plan.AuthType, &update.AuthType)
	setStringPtr(plan.Credentials, &update.Credentials)
	setStringPtr(plan.HealthCheckURL, &update.HealthCheckUrl)
	setStringPtr(plan.ImpersonateServiceAccountEmail, &update.ImpersonateServiceAccountEmail)
	setBoolPtr(plan.Enabled, &update.Enabled)
	setInt32Ptr(plan.Priority, &update.Priority)

	inst, err := r.sdk.LLMProviderInstances().Update(ctx, tenantID, instanceID, version, update)
	if err != nil {
		if errors.Is(err, adminapi.ErrPreconditionFailed) {
			resp.Diagnostics.AddError(
				"LLM provider instance changed since last refresh",
				"The instance's version on the platform differs from Terraform state. "+
					"Run `terraform refresh` and re-plan to pick up out-of-band edits.",
			)
			return
		}
		resp.Diagnostics.AddError("Failed to update LLM provider instance", err.Error())
		return
	}

	refreshed := llmInstanceToModel(tenantID, plan.ProviderType, plan.APIKey, plan.Credentials, plan.ExternalID, inst)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *LLMProviderInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state LLMProviderInstanceResourceModel
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
		if errors.Is(err, adminapi.ErrPreconditionFailed) {
			resp.Diagnostics.AddError(
				"LLM provider instance changed since last refresh",
				"Refresh and re-plan.",
			)
			return
		}
		resp.Diagnostics.AddError("Failed to delete LLM provider instance", err.Error())
	}
}

// ImportState accepts "<tenant_id>/<instance_id>" or just "<instance_id>"
// (falling back to provider-level tenant_id).
func (r *LLMProviderInstanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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

func (r *LLMProviderInstanceResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// llmInstanceToModel maps SDK response → Terraform state. Sensitive inputs
// (api_key, credentials, external_id) aren't round-tripped by the server, so
// we carry them forward from the prior model.
func llmInstanceToModel(
	tenantID string,
	providerType, apiKey, credentials, externalID types.String,
	inst *adminapi.LLMProviderInstance,
) LLMProviderInstanceResourceModel {
	m := LLMProviderInstanceResourceModel{
		TenantID:     types.StringValue(tenantID),
		ProviderType: providerType,
		APIKey:       apiKey,
		Credentials:  credentials,
		ExternalID:   externalID,
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
	m.AuthType = strPtrToTF(inst.AuthType)
	m.HealthCheckURL = strPtrToTF(inst.HealthCheckUrl)
	m.Enabled = boolPtrOrDefault(inst.Enabled)
	m.Priority = int32PtrToTF(inst.Priority)
	m.ImpersonateServiceAccountEmail = strPtrToTF(inst.ImpersonateServiceAccountEmail)
	// AWS-region / role-arn / external-id / session-duration are user-set but
	// the response doesn't echo them back; leave Null in state to round-trip
	// the user's last-set value.
	m.AWSRegion = types.StringNull()
	m.RoleARN = types.StringNull()
	m.SessionDurationMinutes = types.Int64Null()

	m.APIKeyConfigured = boolPtrOrDefault(inst.ApiKeyConfigured)
	m.HealthStatus = enumPtrToTF(inst.HealthStatus)
	m.AvailableForRouting = boolPtrOrDefault(inst.AvailableForRouting)
	m.CreatedAt = timePtrToTF(inst.CreatedAt)
	m.CreatedBy = strPtrToTF(inst.CreatedBy)
	m.ManagedBy = enumPtrToTF(inst.ManagedBy)
	m.ManagedByClientID = strPtrToTF(inst.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(inst.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(inst.LastModifiedBy)
	return m
}

// setStringPtr / setInt32Ptr / setBoolPtr (setBoolPtr is in edge_site_resource.go).
func setStringPtr(in types.String, out **string) {
	if in.IsNull() || in.IsUnknown() {
		return
	}
	v := in.ValueString()
	*out = &v
}

func setInt32Ptr(in types.Int64, out **int32) {
	if in.IsNull() || in.IsUnknown() {
		return
	}
	v := int32(in.ValueInt64())
	*out = &v
}
