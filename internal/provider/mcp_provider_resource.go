package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// MCPProviderResource is the `ferentin_mcp_provider` Terraform resource —
// a tenant-custom MCP provider definition. Distinct from
// `data "ferentin_mcp_provider"` which reads the global catalog. v0.1
// exposes the flat / simple fields; nested config (auth_config, setup_
// instructions, tool_view_bindings, available_scopes) is deferred.
type MCPProviderResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type MCPProviderResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	// Required
	DisplayName types.String `tfsdk:"display_name"`

	// Optional
	Slug                 types.String `tfsdk:"slug"` // input maps to mcp_slug; computed-output is also Slug
	Description          types.String `tfsdk:"description"`
	Icon                 types.String `tfsdk:"icon"`
	Owner                types.String `tfsdk:"owner"`
	Contact              types.String `tfsdk:"contact"`
	DefaultURL           types.String `tfsdk:"default_url"`
	Transport            types.String `tfsdk:"transport"`
	Category             types.String `tfsdk:"category"`
	AllowEndpointOverride types.Bool  `tfsdk:"allow_endpoint_override"`
	EnabledScopes        types.List   `tfsdk:"enabled_scopes"`

	// Computed
	ProviderID        types.String `tfsdk:"provider_id"`
	Name              types.String `tfsdk:"name"`
	Status            types.String `tfsdk:"status"`
	CreatedAt         types.String `tfsdk:"created_at"`
	UpdatedAt         types.String `tfsdk:"updated_at"`
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

func NewMCPProviderResource() resource.Resource { return &MCPProviderResource{} }

var (
	_ resource.Resource                = (*MCPProviderResource)(nil)
	_ resource.ResourceWithConfigure   = (*MCPProviderResource)(nil)
	_ resource.ResourceWithImportState = (*MCPProviderResource)(nil)
)

func (r *MCPProviderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_provider"
}

func (r *MCPProviderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *MCPProviderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A tenant-custom MCP provider definition. Use to publish your own MCP server " +
			"into the tenant catalog (vs `data \"ferentin_mcp_provider\"` which reads the global catalog). " +
			"v0.1 exposes flat fields; nested auth/scopes/setup configs are v0.2.",

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
			"display_name":             schema.StringAttribute{Required: true},
			"slug":                     schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "URL slug; if unset, derived from display_name."},
			"description":              schema.StringAttribute{Optional: true, Computed: true},
			"icon":                     schema.StringAttribute{Optional: true, Computed: true},
			"owner":                    schema.StringAttribute{Optional: true, Computed: true},
			"contact":                  schema.StringAttribute{Optional: true, Computed: true},
			"default_url":              schema.StringAttribute{Optional: true, Computed: true},
			"transport":                schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Transport (`stdio`, `sse`, `streamable_http`)."},
			"category":                 schema.StringAttribute{Optional: true, Computed: true},
			"allow_endpoint_override": schema.BoolAttribute{Optional: true, Computed: true},
			"enabled_scopes": schema.ListAttribute{
				Optional: true, Computed: true,
				ElementType: types.StringType,
			},
			"provider_id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":                 schema.StringAttribute{Computed: true},
			"status":               schema.StringAttribute{Computed: true},
			"created_at":           schema.StringAttribute{Computed: true},
			"updated_at":           schema.StringAttribute{Computed: true},
			"managed_by":           schema.StringAttribute{Computed: true},
			"managed_by_client_id": schema.StringAttribute{Computed: true},
			"managed_by_module":    schema.StringAttribute{Computed: true},
			"last_modified_by":     schema.StringAttribute{Computed: true},
		},
	}
}

func (r *MCPProviderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MCPProviderResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	body := adminapi.MCPProviderCreate{
		// DisplayName is the only required field (non-pointer string).
		DisplayName: plan.DisplayName.ValueString(),
	}
	r.fillBody(ctx, &plan, &body)

	prov, err := r.sdk.MCPProviders().Create(ctx, tenantID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create MCP provider", err.Error())
		return
	}
	state := mcpProviderToModel(tenantID, prov)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *MCPProviderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MCPProviderResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	prov, err := r.sdk.MCPProviders().Get(ctx, tenantID, state.ProviderID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read MCP provider", err.Error())
		return
	}
	refreshed := mcpProviderToModel(tenantID, prov)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPProviderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state MCPProviderResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	// MCPProviderUpdate = gen.McpProvider (the catalog DTO type). DisplayName
	// is *string here (vs the Create body where it's required); send via
	// pointer.
	body := adminapi.MCPProviderUpdate{}
	v := plan.DisplayName.ValueString()
	body.DisplayName = &v
	if !plan.Slug.IsNull() && !plan.Slug.IsUnknown() {
		body.McpSlug = plan.Slug.ValueStringPointer()
	}
	setStringPtr(plan.Description, &body.Description)
	setStringPtr(plan.Icon, &body.Icon)
	setStringPtr(plan.Owner, &body.Owner)
	setStringPtr(plan.Contact, &body.Contact)
	setStringPtr(plan.DefaultURL, &body.DefaultUrl)
	setBoolPtr(plan.AllowEndpointOverride, &body.AllowEndpointOverride)
	if !plan.Transport.IsNull() && !plan.Transport.IsUnknown() {
		v := gen.McpProviderTransport(plan.Transport.ValueString())
		body.Transport = &v
	}
	if !plan.Category.IsNull() && !plan.Category.IsUnknown() {
		v := gen.McpProviderCategory(plan.Category.ValueString())
		body.Category = &v
	}
	if !plan.EnabledScopes.IsNull() && !plan.EnabledScopes.IsUnknown() {
		s := stringListToSDK(ctx, plan.EnabledScopes)
		body.EnabledScopes = &s
	}

	// MCPProviderResponseDto doesn't expose Version; send empty If-Match.
	prov, err := r.sdk.MCPProviders().Update(ctx, tenantID, state.ProviderID.ValueString(), "", body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update MCP provider", err.Error())
		return
	}
	refreshed := mcpProviderToModel(tenantID, prov)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPProviderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MCPProviderResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	err := r.sdk.MCPProviders().Delete(ctx, tenantID, state.ProviderID.ValueString(), "")
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		resp.Diagnostics.AddError("Failed to delete MCP provider", err.Error())
	}
}

func (r *MCPProviderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, providerID string
	switch len(parts) {
	case 1:
		tenantID, providerID = r.tenantID, parts[0]
	case 2:
		tenantID, providerID = parts[0], parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>/<provider_id>` or configure `tenant_id` on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("provider_id"), providerID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+providerID)...)
}

func (r *MCPProviderResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

func (r *MCPProviderResource) fillBody(ctx context.Context, plan *MCPProviderResourceModel, body *adminapi.MCPProviderCreate) {
	if !plan.Slug.IsNull() && !plan.Slug.IsUnknown() {
		body.McpSlug = plan.Slug.ValueStringPointer()
	}
	setStringPtr(plan.Description, &body.Description)
	setStringPtr(plan.Icon, &body.Icon)
	setStringPtr(plan.Owner, &body.Owner)
	setStringPtr(plan.Contact, &body.Contact)
	setStringPtr(plan.DefaultURL, &body.DefaultUrl)
	setBoolPtr(plan.AllowEndpointOverride, &body.AllowEndpointOverride)
	if !plan.Transport.IsNull() && !plan.Transport.IsUnknown() {
		v := gen.McpProviderRequestDtoTransport(plan.Transport.ValueString())
		body.Transport = &v
	}
	if !plan.Category.IsNull() && !plan.Category.IsUnknown() {
		v := gen.McpProviderRequestDtoCategory(plan.Category.ValueString())
		body.Category = &v
	}
	if !plan.EnabledScopes.IsNull() && !plan.EnabledScopes.IsUnknown() {
		s := stringListToSDK(ctx, plan.EnabledScopes)
		body.EnabledScopes = &s
	}
}

func mcpProviderToModel(tenantID string, prov *adminapi.MCPProvider) MCPProviderResourceModel {
	m := MCPProviderResourceModel{TenantID: types.StringValue(tenantID)}
	if prov.Id != nil {
		m.ProviderID = types.StringValue(prov.Id.String())
	} else {
		m.ProviderID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.ProviderID.ValueString())
	// MCPProvider (= gen.McpProviderResponseDto) has no Name field;
	// surface DisplayName as effective name and McpSlug as Slug.
	m.Name = strPtrToTF(prov.DisplayName)
	m.DisplayName = strPtrToTF(prov.DisplayName)
	m.Slug = strPtrToTF(prov.McpSlug)
	m.Description = strPtrToTF(prov.Description)
	m.Icon = strPtrToTF(prov.Icon)
	m.Owner = strPtrToTF(prov.Owner)
	m.Contact = strPtrToTF(prov.Contact)
	m.DefaultURL = strPtrToTF(prov.DefaultUrl)
	m.Transport = enumPtrToTF(prov.Transport)
	m.Category = enumPtrToTF(prov.Category)
	m.AllowEndpointOverride = boolPtrOrDefault(prov.AllowEndpointOverride)
	m.EnabledScopes = stringSliceToList(prov.EnabledScopes)
	m.Status = enumPtrToTF(prov.Status)
	m.CreatedAt = timePtrToTF(prov.CreatedAt)
	m.UpdatedAt = timePtrToTF(prov.UpdatedAt)
	m.ManagedBy = enumPtrToTF(prov.ManagedBy)
	m.ManagedByClientID = strPtrToTF(prov.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(prov.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(prov.LastModifiedBy)
	return m
}
