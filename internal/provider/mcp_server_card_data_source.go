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

// MCPServerCardDataSource fetches one published MCP server-card manifest
// by slug. v0.1 surfaces only the identity / provenance fields; the
// manifest body (gen.McpServerCard.CardContent) is a *Json blob that
// callers needing the tool/prompt/resource list can decode themselves via
// a follow-up `card_content_json` attribute (deferred). The §6.5 design
// doc's for_each-over-tools pattern is Phase 4 / v1.1+.
type MCPServerCardDataSource struct {
	sdk *adminapi.SDKClient
}

type MCPServerCardDataSourceModel struct {
	ID             types.String `tfsdk:"id"`
	Slug           types.String `tfsdk:"slug"`
	CardID         types.String `tfsdk:"card_id"`
	Source         types.String `tfsdk:"source"`
	Checksum       types.String `tfsdk:"checksum"`
	AdminUpload    types.Bool   `tfsdk:"admin_upload"`
	Classpath      types.Bool   `tfsdk:"classpath"`
	CreatedAt      types.String `tfsdk:"created_at"`
	LastIngestedAt types.String `tfsdk:"last_ingested_at"`
}

func NewMCPServerCardDataSource() datasource.DataSource { return &MCPServerCardDataSource{} }

var (
	_ datasource.DataSource              = (*MCPServerCardDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*MCPServerCardDataSource)(nil)
)

func (d *MCPServerCardDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_server_card"
}

func (d *MCPServerCardDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *MCPServerCardDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetch one published MCP server-card manifest by slug. v0.1 surfaces only identity " +
			"+ provenance; the manifest content (tools, prompts, resources) is in the platform's CardContent " +
			"JSON blob and isn't exposed in this version. Use the §6.5 design-doc pattern in v1.1+.",

		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true},
			"slug":             schema.StringAttribute{Required: true, MarkdownDescription: "Card slug."},
			"card_id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Server-generated UUID for the card."},
			"source":           schema.StringAttribute{Computed: true, MarkdownDescription: "Card source (e.g. URL or `classpath`)."},
			"checksum":         schema.StringAttribute{Computed: true},
			"admin_upload":     schema.BoolAttribute{Computed: true},
			"classpath":        schema.BoolAttribute{Computed: true},
			"created_at":       schema.StringAttribute{Computed: true},
			"last_ingested_at": schema.StringAttribute{Computed: true},
		},
	}
}

func (d *MCPServerCardDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data MCPServerCardDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	card, err := d.sdk.Catalogs().MCPServerCardBySlug(ctx, data.Slug.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.Diagnostics.AddError("MCP server card not found",
				fmt.Sprintf("No published card with slug %q.", data.Slug.ValueString()))
			return
		}
		resp.Diagnostics.AddError("Failed to read MCP server card", err.Error())
		return
	}

	data.ID = data.Slug
	if card.Id != nil {
		data.CardID = types.StringValue(card.Id.String())
	} else {
		data.CardID = types.StringNull()
	}
	data.Source = strPtrToTF(card.Source)
	data.Checksum = strPtrToTF(card.Checksum)
	data.AdminUpload = boolPtrOrDefault(card.AdminUpload)
	data.Classpath = boolPtrOrDefault(card.Classpath)
	data.CreatedAt = timePtrToTF(card.CreatedAt)
	data.LastIngestedAt = timePtrToTF(card.LastIngestedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
