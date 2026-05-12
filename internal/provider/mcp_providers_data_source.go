package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// MCPProvidersDataSource lists every entry in the global MCP provider
// catalog (plural — for `for_each` patterns).
type MCPProvidersDataSource struct {
	sdk *adminapi.SDKClient
}

type MCPProvidersDataSourceModel struct {
	Providers []MCPProviderListItem `tfsdk:"providers"`
}

type MCPProviderListItem struct {
	ProviderID  types.String `tfsdk:"provider_id"`
	Slug        types.String `tfsdk:"slug"`
	DisplayName types.String `tfsdk:"display_name"`
	Description types.String `tfsdk:"description"`
}

func NewMCPProvidersDataSource() datasource.DataSource { return &MCPProvidersDataSource{} }

var (
	_ datasource.DataSource              = (*MCPProvidersDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*MCPProvidersDataSource)(nil)
)

func (d *MCPProvidersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_providers"
}

func (d *MCPProvidersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *MCPProvidersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List every entry in the global MCP provider catalog. Returns one row per provider " +
			"with the minimum fields useful for `for_each` patterns.",

		Attributes: map[string]schema.Attribute{
			"providers": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"provider_id":  schema.StringAttribute{Computed: true},
						"slug":         schema.StringAttribute{Computed: true},
						"display_name": schema.StringAttribute{Computed: true},
						"description":  schema.StringAttribute{Computed: true},
					},
				},
			},
		},
	}
}

func (d *MCPProvidersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	provs, err := d.sdk.Catalogs().MCPProviders(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list MCP providers", err.Error())
		return
	}
	out := make([]MCPProviderListItem, 0, len(provs))
	for _, p := range provs {
		item := MCPProviderListItem{
			Slug:        strPtrToTF(p.McpSlug),
			DisplayName: strPtrToTF(p.DisplayName),
			Description: strPtrToTF(p.Description),
		}
		if p.Id != nil {
			item.ProviderID = types.StringValue(p.Id.String())
		} else {
			item.ProviderID = types.StringNull()
		}
		out = append(out, item)
	}
	state := MCPProvidersDataSourceModel{Providers: out}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
