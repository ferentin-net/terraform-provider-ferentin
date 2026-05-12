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

// OtelSinkProviderDataSource looks up a global OTEL sink provider by slug.
type OtelSinkProviderDataSource struct {
	sdk *adminapi.SDKClient
}

type OtelSinkProviderDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Slug        types.String `tfsdk:"slug"`
	Name        types.String `tfsdk:"name"`
	DisplayName types.String `tfsdk:"display_name"`
	Description types.String `tfsdk:"description"`
}

func NewOtelSinkProviderDataSource() datasource.DataSource { return &OtelSinkProviderDataSource{} }

var (
	_ datasource.DataSource              = (*OtelSinkProviderDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*OtelSinkProviderDataSource)(nil)
)

func (d *OtelSinkProviderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_otel_sink_provider"
}

func (d *OtelSinkProviderDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *OtelSinkProviderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an OTEL sink provider in the global catalog by slug. Read-only.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `slug`.",
				Computed:            true,
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "Catalog slug (`datadog`, `honeycomb`, `otlp`, …).",
				Required:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Catalog name.",
				Computed:            true,
			},
			"display_name": schema.StringAttribute{
				MarkdownDescription: "Display name.",
				Computed:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Catalog description.",
				Computed:            true,
			},
		},
	}
}

func (d *OtelSinkProviderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data OtelSinkProviderDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	prov, err := d.sdk.Catalogs().OtelSinkProviderBySlug(ctx, data.Slug.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.Diagnostics.AddError("OTEL sink provider not found",
				fmt.Sprintf("No provider with slug %q in the catalog.", data.Slug.ValueString()))
			return
		}
		resp.Diagnostics.AddError("Failed to read OTEL sink provider", err.Error())
		return
	}

	data.ID = data.Slug
	data.Name = strPtrToTF(prov.Name)
	data.DisplayName = strPtrToTF(prov.DisplayName)
	data.Description = strPtrToTF(prov.Description)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
