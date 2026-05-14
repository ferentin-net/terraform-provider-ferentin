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

// LLMProviderDataSource is the `ferentin_llm_provider` data source —
// read-only catalog lookup by slug. Exposed mainly so HCL can pull the slug
// in a typo-safe way:
//
//	data "ferentin_llm_provider" "anthropic" {
//	  slug = "anthropic"
//	}
//
//	resource "ferentin_llm_provider" "anthropic_prod" {
//	  provider_type = data.ferentin_llm_provider.anthropic.slug
//	  ...
//	}
//
// Resource and data source share the noun `ferentin_llm_provider` — they
// live in distinct Terraform namespaces (the block type disambiguates),
// the same pattern AWS uses for `aws_iam_policy`.
//
// The platform exposes a much richer DTO (LlmProviderDto with nested
// Capabilities / DataGovernance / EnterpriseReadiness / Identity / Operational
// sub-objects); for v0.1 we surface only `slug` and a stub `name`. Future
// versions can expand the schema to expose the catalog metadata.
type LLMProviderDataSource struct {
	sdk *adminapi.SDKClient
}

type LLMProviderDataSourceModel struct {
	ID   types.String `tfsdk:"id"`
	Slug types.String `tfsdk:"slug"`
	Name types.String `tfsdk:"name"`
}

func NewLLMProviderDataSource() datasource.DataSource {
	return &LLMProviderDataSource{}
}

var (
	_ datasource.DataSource              = (*LLMProviderDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*LLMProviderDataSource)(nil)
)

func (d *LLMProviderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_llm_provider"
}

func (d *LLMProviderDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *LLMProviderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an entry in the global LLM provider catalog by slug. Read-only.\n\n" +
			"**See also:** the [`ferentin_llm_provider` resource](../resources/llm_provider.md) — the tenant-scoped " +
			"binding that consumes this slug via `provider_type`. Resource and data source share the noun; " +
			"Terraform disambiguates them by block type (`resource` vs `data`).",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `slug`. Surfaced as `id` for Terraform convention.",
				Computed:            true,
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "Catalog slug (`anthropic`, `openai`, `google-vertex`, …).",
				Required:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Display name from the catalog.",
				Computed:            true,
			},
		},
	}
}

func (d *LLMProviderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data LLMProviderDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	slug := data.Slug.ValueString()
	prov, err := d.sdk.Catalogs().LLMProviderBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.Diagnostics.AddError(
				"LLM provider not found",
				fmt.Sprintf("No provider with slug %q exists in the global catalog. "+
					"Available providers can be listed with `Catalogs.LLMProviders` via the SDK.", slug),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to read LLM provider catalog", err.Error())
		return
	}

	data.ID = types.StringValue(slug)
	// LlmProviderDto's Identity.Name is the closest analog; if the field
	// isn't populated, fall back to the slug.
	if prov.Identity.Name != "" {
		data.Name = types.StringValue(prov.Identity.Name)
	} else {
		data.Name = types.StringValue(slug)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
