package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// WorkloadOAuthClientTestDataSource runs the platform's IdP-probe action
// on a `ferentin_workload_oauth_client` and exposes the result. Useful for
// guardrailing config in CI: failing the data-source read fails the plan
// before the user applies a known-broken IdP binding.
//
// Re-runs on every refresh — the platform action mints a token at the
// upstream IdP, so this incurs a real round-trip. Mark the dependent
// resource lifecycle accordingly if you don't want this in every plan.
type WorkloadOAuthClientTestDataSource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type WorkloadOAuthClientTestDataSourceModel struct {
	ID            types.String `tfsdk:"id"`
	TenantID      types.String `tfsdk:"tenant_id"`
	ClientID      types.String `tfsdk:"client_id"`
	OverallPass   types.Bool   `tfsdk:"overall_pass"`
	Error         types.String `tfsdk:"error"`
	ErrorDetail   types.String `tfsdk:"error_detail"`
	TokenType     types.String `tfsdk:"token_type"`
	TokenEndpoint types.String `tfsdk:"token_endpoint"`
}

func NewWorkloadOAuthClientTestDataSource() datasource.DataSource {
	return &WorkloadOAuthClientTestDataSource{}
}

var (
	_ datasource.DataSource              = (*WorkloadOAuthClientTestDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*WorkloadOAuthClientTestDataSource)(nil)
)

func (d *WorkloadOAuthClientTestDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workload_oauth_client_test"
}

func (d *WorkloadOAuthClientTestDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *WorkloadOAuthClientTestDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Probes a `ferentin_workload_oauth_client` against its bound IdP and exposes the " +
			"result. Each plan / apply triggers a fresh round-trip — gate downstream resources on " +
			"`overall_pass` to fail fast when the IdP config is broken.",
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{Computed: true},
			"tenant_id":      schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Tenant UUID. Defaults to provider-level value."},
			"client_id":      schema.StringAttribute{Required: true, MarkdownDescription: "UUID of the workload OAuth client to probe."},
			"overall_pass":   schema.BoolAttribute{Computed: true, MarkdownDescription: "True iff every per-check assertion passed."},
			"error":          schema.StringAttribute{Computed: true, MarkdownDescription: "Sanitized top-level error category when the mint failed (one of `config`, `connection_refused`, `timeout`, `unauthorized`, `forbidden`, `rate_limited`, `server_error`, `generic`). Null on success."},
			"error_detail":   schema.StringAttribute{Computed: true, MarkdownDescription: "Operator-actionable hint for the `error` category."},
			"token_type":     schema.StringAttribute{Computed: true, MarkdownDescription: "`jwt` (3-segment JWT), `opaque`, or null on hard mint failure."},
			"token_endpoint": schema.StringAttribute{Computed: true, MarkdownDescription: "The token endpoint the probe hit, echoed back."},
		},
	}
}

func (d *WorkloadOAuthClientTestDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m WorkloadOAuthClientTestDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := m.TenantID.ValueString()
	if tenantID == "" {
		tenantID = d.tenantID
	}

	out, err := d.sdk.WorkloadOAuthClients().Test(ctx, tenantID, m.ClientID.ValueString())
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to probe workload OAuth client", err)
		return
	}

	state := WorkloadOAuthClientTestDataSourceModel{
		ID:            types.StringValue(tenantID + "/" + m.ClientID.ValueString() + "/test"),
		TenantID:      types.StringValue(tenantID),
		ClientID:      m.ClientID,
		OverallPass:   boolPtrOrDefault(out.OverallPass),
		ErrorDetail:   strPtrToTF(out.ErrorDetail),
		TokenEndpoint: strPtrToTF(out.TokenEndpoint),
	}
	if out.Error != nil {
		state.Error = types.StringValue(string(*out.Error))
	} else {
		state.Error = types.StringNull()
	}
	if out.TokenType != nil {
		state.TokenType = types.StringValue(string(*out.TokenType))
	} else {
		state.TokenType = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
