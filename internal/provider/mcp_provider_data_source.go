package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// MCPProviderDataSource looks up an entry in the global MCP provider
// catalog by UUID. Mirrors the `ferentin_llm_provider` shape; the SDK's
// MCPProviderCatalog type (gen.McpProvider) is the richer catalog DTO
// distinct from the per-tenant view.
type MCPProviderDataSource struct {
	sdk *adminapi.SDKClient
}

type MCPProviderDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	ProviderID  types.String `tfsdk:"provider_id"`
	Name        types.String `tfsdk:"name"`
	Slug        types.String `tfsdk:"slug"`
	DisplayName types.String `tfsdk:"display_name"`
	Description types.String `tfsdk:"description"`
}

func NewMCPProviderDataSource() datasource.DataSource { return &MCPProviderDataSource{} }

var (
	_ datasource.DataSource              = (*MCPProviderDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*MCPProviderDataSource)(nil)
)

func (d *MCPProviderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_provider"
}

func (d *MCPProviderDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected ProviderData, got %T", req.ProviderData))
		return
	}
	d.sdk = pd.SDK
}

func (d *MCPProviderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a global MCP provider by UUID. Read-only.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `provider_id` — surfaced as `id` per Terraform convention.",
				Computed:            true,
			},
			"provider_id": schema.StringAttribute{
				MarkdownDescription: "UUID of the catalog provider entry.",
				Required:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Catalog name.",
				Computed:            true,
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "URL slug for the provider (e.g. `salesforce`, `box`).",
				Computed:            true,
			},
			"display_name": schema.StringAttribute{
				MarkdownDescription: "Display name shown in the admin console.",
				Computed:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Catalog description.",
				Computed:            true,
			},
		},
	}
}

func (d *MCPProviderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data MCPProviderDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	prov, err := d.sdk.Catalogs().MCPProviderByID(ctx, data.ProviderID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.Diagnostics.AddError("MCP provider not found in catalog",
				fmt.Sprintf("No global provider with id %q. List available providers via Catalogs.MCPProviders().",
					data.ProviderID.ValueString()))
			return
		}
		resp.Diagnostics.AddError("Failed to read MCP provider catalog", err.Error())
		return
	}

	data.ID = data.ProviderID
	// MCPProviderCatalog has no Name field — surface DisplayName as the
	// effective name; Slug is McpSlug.
	data.Name = strPtrToTF(prov.DisplayName)
	data.Slug = strPtrToTF(prov.McpSlug)
	data.DisplayName = strPtrToTF(prov.DisplayName)
	data.Description = strPtrToTF(prov.Description)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
