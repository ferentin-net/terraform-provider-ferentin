package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// WorkloadIdentityProviderTestDataSource runs the platform's trust-config
// probe on a `ferentin_workload_identity_provider` and exposes the raw
// response body. The platform doesn't define a typed response schema for
// this endpoint, so we surface the JSON as a string for callers to parse.
type WorkloadIdentityProviderTestDataSource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type WorkloadIdentityProviderTestDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	TenantID   types.String `tfsdk:"tenant_id"`
	ProviderID types.String `tfsdk:"provider_id"`
	Result     types.String `tfsdk:"result"` // raw JSON body
}

func NewWorkloadIdentityProviderTestDataSource() datasource.DataSource {
	return &WorkloadIdentityProviderTestDataSource{}
}

var (
	_ datasource.DataSource              = (*WorkloadIdentityProviderTestDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*WorkloadIdentityProviderTestDataSource)(nil)
)

func (d *WorkloadIdentityProviderTestDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workload_identity_provider_test"
}

func (d *WorkloadIdentityProviderTestDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
	d.tenantID = pd.TenantID
}

func (d *WorkloadIdentityProviderTestDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Probes a `ferentin_workload_identity_provider` trust config and exposes the raw " +
			"response body as a JSON string. The platform's response shape isn't typed in the OpenAPI spec, so " +
			"parse with `jsondecode()` in HCL to extract the fields you need.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true},
			"tenant_id":   schema.StringAttribute{Optional: true, Computed: true},
			"provider_id": schema.StringAttribute{Required: true, MarkdownDescription: "UUID of the workload identity provider to probe."},
			"result":      schema.StringAttribute{Computed: true, MarkdownDescription: "Raw JSON response from the platform. Use `jsondecode()` to extract fields."},
		},
	}
}

func (d *WorkloadIdentityProviderTestDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m WorkloadIdentityProviderTestDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := m.TenantID.ValueString()
	if tenantID == "" {
		tenantID = d.tenantID
	}
	out, err := d.sdk.WorkloadIdentityProviders().Test(ctx, tenantID, m.ProviderID.ValueString())
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to probe workload identity provider", err)
		return
	}
	state := WorkloadIdentityProviderTestDataSourceModel{
		ID:         types.StringValue(tenantID + "/" + m.ProviderID.ValueString() + "/test"),
		TenantID:   types.StringValue(tenantID),
		ProviderID: m.ProviderID,
		Result:     types.StringValue(string(out)),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
