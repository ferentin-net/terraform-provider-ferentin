package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// WorkloadIdentityProviderResource is the `ferentin_workload_identity_provider`
// Terraform resource — inbound trust configuration for accepting workload
// identity tokens from a customer's cloud (AWS / GCP / Azure / OCI / generic
// OIDC) or GitHub Actions. Workloads in that cloud authenticate to Ferentin
// without a pre-provisioned client_secret by presenting their cloud-issued
// JWT, which Ferentin validates against the trust config defined here.
//
// Unlike most v1 entities, this resource doesn't have an optimistic-
// concurrency `version` field — Update / Delete are last-write-wins. The
// `aws`, `azure`, `gcp`, etc. boolean discriminators are computed from
// `cloud_provider`.
type WorkloadIdentityProviderResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type WorkloadIdentityProviderResourceModel struct {
	// Identity
	ID         types.String `tfsdk:"id"` // composite "<tenant>/<provider_uuid>"
	TenantID   types.String `tfsdk:"tenant_id"`
	ProviderID types.String `tfsdk:"provider_id"` // server-generated UUID

	// Required user-supplied
	Name              types.String `tfsdk:"name"`
	CloudProvider     types.String `tfsdk:"cloud_provider"`
	ProtocolType      types.String `tfsdk:"protocol_type"`
	JwksURI           types.String `tfsdk:"jwks_uri"`
	AllowedIssuers    types.List   `tfsdk:"allowed_issuers"`
	ExpectedAudiences types.List   `tfsdk:"expected_audiences"`

	// Optional
	Description           types.String `tfsdk:"description"`
	BaseURL               types.String `tfsdk:"base_url"`
	IdentityClaim         types.String `tfsdk:"identity_claim"`
	IdentityProviderID    types.String `tfsdk:"identity_provider_id"`
	ClaimMappings         types.String `tfsdk:"claim_mappings"` // JSON string
	CloudConfig           types.String `tfsdk:"cloud_config"`   // JSON string
	RequiredClaims        types.List   `tfsdk:"required_claims"`
	DomainNames           types.List   `tfsdk:"domain_names"`
	AllowClockSkewSeconds types.Int64  `tfsdk:"allow_clock_skew_seconds"`
	Active                types.Bool   `tfsdk:"active"`
	CatchAll              types.Bool   `tfsdk:"catch_all"`
	ValidateAudience      types.Bool   `tfsdk:"validate_audience"`
	ValidateIssuer        types.Bool   `tfsdk:"validate_issuer"`

	// Computed / server-derived
	RedirectURL              types.String `tfsdk:"redirect_url"`
	ValidConfiguration       types.Bool   `tfsdk:"valid_configuration"`
	PlatformManaged          types.Bool   `tfsdk:"platform_managed"`
	CloudProviderDisplayName types.String `tfsdk:"cloud_provider_display_name"`
	AWS                      types.Bool   `tfsdk:"aws"`
	Azure                    types.Bool   `tfsdk:"azure"`
	GCP                      types.Bool   `tfsdk:"gcp"`
	GenericOIDC              types.Bool   `tfsdk:"generic_oidc"`
	GitHub                   types.Bool   `tfsdk:"github"`
	OCI                      types.Bool   `tfsdk:"oci"`
	OIDC                     types.Bool   `tfsdk:"oidc"`
	SAML                     types.Bool   `tfsdk:"saml"`
	WorkloadIdentity         types.Bool   `tfsdk:"workload_identity"`
	CreatedAt                types.String `tfsdk:"created_at"`
	UpdatedAt                types.String `tfsdk:"updated_at"`
}

func NewWorkloadIdentityProviderResource() resource.Resource {
	return &WorkloadIdentityProviderResource{}
}

var (
	_ resource.Resource                = (*WorkloadIdentityProviderResource)(nil)
	_ resource.ResourceWithConfigure   = (*WorkloadIdentityProviderResource)(nil)
	_ resource.ResourceWithImportState = (*WorkloadIdentityProviderResource)(nil)
)

func (r *WorkloadIdentityProviderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workload_identity_provider"
}

func (r *WorkloadIdentityProviderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *WorkloadIdentityProviderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Inbound trust configuration for accepting workload identity tokens from a customer's " +
			"cloud (AWS / GCP / Azure / OCI / generic OIDC / GitHub). Workloads in that cloud authenticate to " +
			"Ferentin without a pre-provisioned client_secret by presenting their cloud-issued JWT, which Ferentin " +
			"validates against this trust config.\n\n" +
			"This resource doesn't use optimistic concurrency (no `version` field); Update / Delete are last-write-wins.\n\n" +
			"## Import\n\n" +
			"Existing providers can be imported using `<tenant_id>/<provider_id>`:\n\n" +
			"```\n" +
			"terraform import ferentin_workload_identity_provider.example <tenant_id>/<provider_id>\n" +
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
			"provider_id": schema.StringAttribute{
				MarkdownDescription: "Server-generated UUID for this trust config.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},

			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Human-readable name (e.g. `aws-prod-eks`)."},
			"cloud_provider": schema.StringAttribute{
				MarkdownDescription: "Cloud provider type. Allowed: `aws`, `azure`, `gcp`, `generic_oidc`, `github`, `oci`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.OneOf("aws", "azure", "gcp", "generic_oidc", "github", "oci"),
				},
			},
			"protocol_type": schema.StringAttribute{
				MarkdownDescription: "Identity protocol. Allowed: `OIDC`, `SAML`, `WORKLOAD_IDENTITY`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.OneOf("OIDC", "SAML", "WORKLOAD_IDENTITY"),
				},
			},
			"jwks_uri": schema.StringAttribute{
				MarkdownDescription: "JWKS endpoint URL the platform uses to verify cloud-issued token signatures.",
				Required:            true,
			},
			"allowed_issuers": schema.ListAttribute{
				MarkdownDescription: "Allowed issuer URLs (exact match). For cloud providers, use the standard issuer URL " +
					"(e.g. `https://sts.amazonaws.com` for AWS, `https://accounts.google.com` for GCP).",
				Required:    true,
				ElementType: types.StringType,
			},
			"expected_audiences": schema.ListAttribute{
				MarkdownDescription: "Expected `aud` claim values in cloud-issued tokens.",
				Required:            true,
				ElementType:         types.StringType,
			},

			"description":              schema.StringAttribute{Optional: true, Computed: true},
			"base_url":                 schema.StringAttribute{Optional: true, Computed: true},
			"identity_claim":           schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "JWT claim that identifies the workload (default: `sub`; GCP uses `email`)."},
			"identity_provider_id":     schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Optional FK to a `ferentin_identity_provider` for SSO inheritance."},
			"claim_mappings":           schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Map cloud claims to standard WIF claims, as a JSON string."},
			"cloud_config":             schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Cloud-specific configuration, as a JSON string."},
			"required_claims":          schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, MarkdownDescription: "Claims that must be present in cloud tokens for the trust to apply."},
			"domain_names":             schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType},
			"allow_clock_skew_seconds": schema.Int64Attribute{Optional: true, Computed: true, MarkdownDescription: "Allowed clock skew in seconds (0-300)."},
			"active": schema.BoolAttribute{
				MarkdownDescription: "Whether this provider accepts inbound tokens. Default `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			"catch_all":         schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "When true, this provider handles all authentication requests for the tenant (default `false`)."},
			"validate_audience": schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "When true (default), the `aud` claim is enforced against `expected_audiences`."},
			"validate_issuer":   schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "When true (default), the `iss` claim is enforced against `allowed_issuers`."},

			"redirect_url":                schema.StringAttribute{Computed: true},
			"valid_configuration":         schema.BoolAttribute{Computed: true},
			"platform_managed":            schema.BoolAttribute{Computed: true, MarkdownDescription: "True when Ferentin Platform SSO manages this trust config end-to-end."},
			"cloud_provider_display_name": schema.StringAttribute{Computed: true},
			"aws":                         schema.BoolAttribute{Computed: true},
			"azure":                       schema.BoolAttribute{Computed: true},
			"gcp":                         schema.BoolAttribute{Computed: true},
			"generic_oidc":                schema.BoolAttribute{Computed: true},
			"github":                      schema.BoolAttribute{Computed: true},
			"oci":                         schema.BoolAttribute{Computed: true},
			"oidc":                        schema.BoolAttribute{Computed: true},
			"saml":                        schema.BoolAttribute{Computed: true},
			"workload_identity":           schema.BoolAttribute{Computed: true},
			"created_at":                  schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"updated_at":                  schema.StringAttribute{Computed: true},
		},
	}
}

// toBody builds the WorkloadIdentityConfig payload from the model. Used for
// both Create and Update — the platform exposes the same DTO for both verbs,
// matching the response shape; server-side fields like `id` and `createdAt`
// are echoed back as the response and ignored on input.
func (m *WorkloadIdentityProviderResourceModel) toBody(ctx context.Context, tenantUUID openapi_types.UUID) (*adminapi.WorkloadIdentityProvider, error) {
	body := &adminapi.WorkloadIdentityProvider{
		Name:              m.Name.ValueString(),
		JwksUri:           m.JwksURI.ValueString(),
		CloudProvider:     gen.WorkloadIdentityConfigCloudProvider(m.CloudProvider.ValueString()),
		ProtocolType:      gen.WorkloadIdentityConfigProtocolType(m.ProtocolType.ValueString()),
		TenantId:          tenantUUID,
		AllowedIssuers:    stringListToSDK(ctx, m.AllowedIssuers),
		ExpectedAudiences: stringListToSDK(ctx, m.ExpectedAudiences),
	}

	setStringPtr(m.Description, &body.Description)
	setStringPtr(m.BaseURL, &body.BaseUrl)
	setStringPtr(m.IdentityClaim, &body.IdentityClaim)
	setStringPtr(m.ClaimMappings, &body.ClaimMappings)
	setStringPtr(m.CloudConfig, &body.CloudConfig)
	setBoolPtr(m.Active, &body.Active)
	setBoolPtr(m.CatchAll, &body.CatchAll)
	setBoolPtr(m.ValidateAudience, &body.ValidateAudience)
	setBoolPtr(m.ValidateIssuer, &body.ValidateIssuer)
	setInt32Ptr(m.AllowClockSkewSeconds, &body.AllowClockSkewSeconds)
	if !m.IdentityProviderID.IsNull() && !m.IdentityProviderID.IsUnknown() && m.IdentityProviderID.ValueString() != "" {
		id, err := parseUUID(m.IdentityProviderID.ValueString())
		if err != nil {
			return nil, fmt.Errorf("identity_provider_id: %w", err)
		}
		body.IdentityProviderId = &id
	}
	if !m.RequiredClaims.IsNull() && !m.RequiredClaims.IsUnknown() {
		v := stringListToSDK(ctx, m.RequiredClaims)
		body.RequiredClaims = &v
	}
	if !m.DomainNames.IsNull() && !m.DomainNames.IsUnknown() {
		v := stringListToSDK(ctx, m.DomainNames)
		body.DomainNames = &v
	}
	return body, nil
}

func (r *WorkloadIdentityProviderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan WorkloadIdentityProviderResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tenantID := r.resolveTenant(plan.TenantID)
	tenantUUID, err := parseUUID(tenantID)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("tenant_id"), "Invalid tenant UUID", err.Error())
		return
	}
	body, err := plan.toBody(ctx, tenantUUID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid workload identity provider config", err.Error())
		return
	}

	out, err := r.sdk.WorkloadIdentityProviders().Create(ctx, tenantID, *body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create workload identity provider", err)
		return
	}

	state := workloadIdentityProviderToModel(tenantID, out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *WorkloadIdentityProviderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state WorkloadIdentityProviderResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	out, err := r.sdk.WorkloadIdentityProviders().Get(ctx, tenantID, state.ProviderID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read workload identity provider", err)
		return
	}
	refreshed := workloadIdentityProviderToModel(tenantID, out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *WorkloadIdentityProviderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state WorkloadIdentityProviderResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	tenantUUID, err := parseUUID(tenantID)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("tenant_id"), "Invalid tenant UUID", err.Error())
		return
	}
	body, err := plan.toBody(ctx, tenantUUID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid workload identity provider config", err.Error())
		return
	}
	out, err := r.sdk.WorkloadIdentityProviders().Update(ctx, tenantID, state.ProviderID.ValueString(), *body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update workload identity provider", err)
		return
	}
	refreshed := workloadIdentityProviderToModel(tenantID, out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *WorkloadIdentityProviderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state WorkloadIdentityProviderResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	err := r.sdk.WorkloadIdentityProviders().Delete(ctx, tenantID, state.ProviderID.ValueString())
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete workload identity provider", err)
	}
}

func (r *WorkloadIdentityProviderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, id string
	switch len(parts) {
	case 1:
		tenantID = r.tenantID
		id = parts[0]
	case 2:
		tenantID = parts[0]
		id = parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>/<provider_id>` or configure `tenant_id` on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("provider_id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+id)...)
}

func (r *WorkloadIdentityProviderResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

func workloadIdentityProviderToModel(tenantID string, p *adminapi.WorkloadIdentityProvider) WorkloadIdentityProviderResourceModel {
	m := WorkloadIdentityProviderResourceModel{
		TenantID: types.StringValue(tenantID),
	}
	if p.Id != nil {
		m.ProviderID = types.StringValue(p.Id.String())
	} else {
		m.ProviderID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.ProviderID.ValueString())

	m.Name = types.StringValue(p.Name)
	m.JwksURI = types.StringValue(p.JwksUri)
	m.CloudProvider = types.StringValue(string(p.CloudProvider))
	m.ProtocolType = types.StringValue(string(p.ProtocolType))
	m.AllowedIssuers = stringSliceToList(&p.AllowedIssuers)
	m.ExpectedAudiences = stringSliceToList(&p.ExpectedAudiences)

	m.Description = strPtrToTF(p.Description)
	m.BaseURL = strPtrToTF(p.BaseUrl)
	m.IdentityClaim = strPtrToTF(p.IdentityClaim)
	if p.IdentityProviderId != nil {
		m.IdentityProviderID = types.StringValue(p.IdentityProviderId.String())
	} else {
		m.IdentityProviderID = types.StringNull()
	}
	m.ClaimMappings = strPtrToTF(p.ClaimMappings)
	m.CloudConfig = strPtrToTF(p.CloudConfig)
	m.RequiredClaims = stringSliceToList(p.RequiredClaims)
	m.DomainNames = stringSliceToList(p.DomainNames)
	m.AllowClockSkewSeconds = int32PtrToTF(p.AllowClockSkewSeconds)
	m.Active = boolPtrOrDefault(p.Active)
	m.CatchAll = boolPtrOrDefault(p.CatchAll)
	m.ValidateAudience = boolPtrOrDefault(p.ValidateAudience)
	m.ValidateIssuer = boolPtrOrDefault(p.ValidateIssuer)

	m.RedirectURL = strPtrToTF(p.RedirectUrl)
	m.ValidConfiguration = boolPtrOrDefault(p.ValidConfiguration)
	m.PlatformManaged = boolPtrOrDefault(p.PlatformManaged)
	m.CloudProviderDisplayName = strPtrToTF(p.CloudProviderDisplayName)
	m.AWS = boolPtrOrDefault(p.Aws)
	m.Azure = boolPtrOrDefault(p.Azure)
	m.GCP = boolPtrOrDefault(p.Gcp)
	m.GenericOIDC = boolPtrOrDefault(p.GenericOidc)
	m.GitHub = boolPtrOrDefault(p.GitHub)
	m.OCI = boolPtrOrDefault(p.Oci)
	m.OIDC = boolPtrOrDefault(p.Oidc)
	m.SAML = boolPtrOrDefault(p.Saml)
	m.WorkloadIdentity = boolPtrOrDefault(p.WorkloadIdentity)
	m.CreatedAt = timePtrToTF(p.CreatedAt)
	m.UpdatedAt = timePtrToTF(p.UpdatedAt)
	return m
}
