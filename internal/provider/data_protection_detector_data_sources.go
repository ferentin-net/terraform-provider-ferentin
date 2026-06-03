package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// Built-in detector catalog (US_SSN, EXFILTRATION_URL, …). Read-only;
// identical for every tenant. Shares dpCatalogTenant with the profile data
// sources.

// -------------------------------------------------------------------------
// Plural: ferentin_data_protection_detectors
// -------------------------------------------------------------------------

type DataProtectionDetectorsDataSource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type DataProtectionDetectorsDataSourceModel struct {
	TenantID  types.String                 `tfsdk:"tenant_id"`
	Detectors []DataProtectionDetectorItem `tfsdk:"detectors"`
}

type DataProtectionDetectorItem struct {
	ID           types.String `tfsdk:"id"`
	Description  types.String `tfsdk:"description"`
	Category     types.String `tfsdk:"category"`
	FpeSafe      types.Bool   `tfsdk:"fpe_safe"`
	HasValidator types.Bool   `tfsdk:"has_validator"`
}

func NewDataProtectionDetectorsDataSource() datasource.DataSource {
	return &DataProtectionDetectorsDataSource{}
}

var (
	_ datasource.DataSource              = (*DataProtectionDetectorsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*DataProtectionDetectorsDataSource)(nil)
)

func (d *DataProtectionDetectorsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_data_protection_detectors"
}

func (d *DataProtectionDetectorsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *DataProtectionDetectorsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List the built-in data-protection detectors (e.g. `US_SSN`, " +
			"`EXFILTRATION_URL`). Read-only catalog data.",
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{Optional: true},
			"detectors": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":            schema.StringAttribute{Computed: true},
						"description":   schema.StringAttribute{Computed: true},
						"category":      schema.StringAttribute{Computed: true},
						"fpe_safe":      schema.BoolAttribute{Computed: true},
						"has_validator": schema.BoolAttribute{Computed: true},
					},
				},
			},
		},
	}
}

func (d *DataProtectionDetectorsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data DataProtectionDetectorsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := dpCatalogTenant(data.TenantID, d.tenantID)
	detectors, err := d.sdk.DataProtectionPolicies().Detectors(ctx, tenantID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list data protection detectors", err.Error())
		return
	}
	out := make([]DataProtectionDetectorItem, 0, len(detectors))
	for _, det := range detectors {
		out = append(out, DataProtectionDetectorItem{
			ID:           strPtrToTF(det.Id),
			Description:  strPtrToTF(det.Description),
			Category:     strPtrToTF(det.Category),
			FpeSafe:      boolPtrOrDefault(det.FpeSafe),
			HasValidator: boolPtrOrDefault(det.HasValidator),
		})
	}
	data.Detectors = out
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// -------------------------------------------------------------------------
// Singular: ferentin_data_protection_detector
// -------------------------------------------------------------------------

type DataProtectionDetectorDataSource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type DataProtectionDetectorDataSourceModel struct {
	TenantID     types.String `tfsdk:"tenant_id"`
	ID           types.String `tfsdk:"id"`
	Description  types.String `tfsdk:"description"`
	Category     types.String `tfsdk:"category"`
	FpeSafe      types.Bool   `tfsdk:"fpe_safe"`
	HasValidator types.Bool   `tfsdk:"has_validator"`
}

func NewDataProtectionDetectorDataSource() datasource.DataSource {
	return &DataProtectionDetectorDataSource{}
}

var (
	_ datasource.DataSource              = (*DataProtectionDetectorDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*DataProtectionDetectorDataSource)(nil)
)

func (d *DataProtectionDetectorDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_data_protection_detector"
}

func (d *DataProtectionDetectorDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *DataProtectionDetectorDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up one built-in data-protection detector by ID (e.g. `US_SSN`). " +
			"Referencing `.id` gives plan-time validation that the detector exists.",
		Attributes: map[string]schema.Attribute{
			"tenant_id":     schema.StringAttribute{Optional: true},
			"id":            schema.StringAttribute{Required: true, MarkdownDescription: "Detector ID, e.g. `US_SSN`."},
			"description":   schema.StringAttribute{Computed: true},
			"category":      schema.StringAttribute{Computed: true},
			"fpe_safe":      schema.BoolAttribute{Computed: true},
			"has_validator": schema.BoolAttribute{Computed: true},
		},
	}
}

func (d *DataProtectionDetectorDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data DataProtectionDetectorDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := dpCatalogTenant(data.TenantID, d.tenantID)
	detectors, err := d.sdk.DataProtectionPolicies().Detectors(ctx, tenantID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read data protection detectors", err.Error())
		return
	}
	want := data.ID.ValueString()
	for _, det := range detectors {
		if det.Id != nil && *det.Id == want {
			data.Description = strPtrToTF(det.Description)
			data.Category = strPtrToTF(det.Category)
			data.FpeSafe = boolPtrOrDefault(det.FpeSafe)
			data.HasValidator = boolPtrOrDefault(det.HasValidator)
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
			return
		}
	}
	resp.Diagnostics.AddError(
		"Data protection detector not found",
		fmt.Sprintf("No built-in detector with ID %q. List them with the "+
			"ferentin_data_protection_detectors data source.", want),
	)
}
