package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// The detector-profile catalog is built-in and identical for every tenant;
// the tenant path segment is a platform routing artifact, so these data
// sources accept an optional `tenant_id` that falls back to the provider
// default.

// dpCatalogTenant resolves the tenant for the catalog data sources: the
// per-data-source attr if set, else the provider default.
func dpCatalogTenant(perDS types.String, def string) string {
	if !perDS.IsNull() && !perDS.IsUnknown() && perDS.ValueString() != "" {
		return perDS.ValueString()
	}
	return def
}

func profileDetectorIDs(p adminapi.DataProtectionProfile) types.List {
	if p.Detectors == nil {
		return types.ListNull(types.StringType)
	}
	elems := make([]attr.Value, 0, len(*p.Detectors))
	for _, d := range *p.Detectors {
		if d.Id != nil {
			elems = append(elems, types.StringValue(*d.Id))
		}
	}
	lv, _ := types.ListValue(types.StringType, elems)
	return lv
}

// -------------------------------------------------------------------------
// Plural: ferentin_data_protection_profiles
// -------------------------------------------------------------------------

type DataProtectionProfilesDataSource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type DataProtectionProfilesDataSourceModel struct {
	TenantID types.String                `tfsdk:"tenant_id"`
	Profiles []DataProtectionProfileItem `tfsdk:"profiles"`
}

type DataProtectionProfileItem struct {
	Name        types.String `tfsdk:"name"`
	DisplayName types.String `tfsdk:"display_name"`
	Description types.String `tfsdk:"description"`
	Region      types.String `tfsdk:"region"`
	Category    types.String `tfsdk:"category"`
	Detectors   types.List   `tfsdk:"detectors"`
}

func NewDataProtectionProfilesDataSource() datasource.DataSource {
	return &DataProtectionProfilesDataSource{}
}

var (
	_ datasource.DataSource              = (*DataProtectionProfilesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*DataProtectionProfilesDataSource)(nil)
)

func (d *DataProtectionProfilesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_data_protection_profiles"
}

func (d *DataProtectionProfilesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *DataProtectionProfilesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List the built-in data-protection detector profiles (e.g. `US_PII`, " +
			"`EXFILTRATION_DEFENSE`). Read-only catalog data, suitable for `for_each`.",
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{Optional: true},
			"profiles": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":         schema.StringAttribute{Computed: true},
						"display_name": schema.StringAttribute{Computed: true},
						"description":  schema.StringAttribute{Computed: true},
						"region":       schema.StringAttribute{Computed: true},
						"category":     schema.StringAttribute{Computed: true},
						"detectors": schema.ListAttribute{
							MarkdownDescription: "Detector IDs grouped under this profile.",
							Computed:            true,
							ElementType:         types.StringType,
						},
					},
				},
			},
		},
	}
}

func (d *DataProtectionProfilesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data DataProtectionProfilesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := dpCatalogTenant(data.TenantID, d.tenantID)
	profiles, err := d.sdk.DataProtectionPolicies().Profiles(ctx, tenantID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list data protection profiles", err.Error())
		return
	}
	out := make([]DataProtectionProfileItem, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, DataProtectionProfileItem{
			Name:        strPtrToTF(p.Name),
			DisplayName: strPtrToTF(p.DisplayName),
			Description: strPtrToTF(p.Description),
			Region:      strPtrToTF(p.Region),
			Category:    strPtrToTF(p.Category),
			Detectors:   profileDetectorIDs(p),
		})
	}
	data.Profiles = out
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// -------------------------------------------------------------------------
// Singular: ferentin_data_protection_profile
// -------------------------------------------------------------------------

type DataProtectionProfileDataSource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type DataProtectionProfileDataSourceModel struct {
	TenantID    types.String `tfsdk:"tenant_id"`
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	DisplayName types.String `tfsdk:"display_name"`
	Description types.String `tfsdk:"description"`
	Region      types.String `tfsdk:"region"`
	Category    types.String `tfsdk:"category"`
	Detectors   types.List   `tfsdk:"detectors"`
}

func NewDataProtectionProfileDataSource() datasource.DataSource {
	return &DataProtectionProfileDataSource{}
}

var (
	_ datasource.DataSource              = (*DataProtectionProfileDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*DataProtectionProfileDataSource)(nil)
)

func (d *DataProtectionProfileDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_data_protection_profile"
}

func (d *DataProtectionProfileDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *DataProtectionProfileDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up one built-in data-protection profile by name (e.g. `US_PII`). " +
			"Referencing `.name` gives plan-time validation that the profile exists.",
		Attributes: map[string]schema.Attribute{
			"tenant_id":    schema.StringAttribute{Optional: true},
			"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "Same as `name`."},
			"name":         schema.StringAttribute{Required: true, MarkdownDescription: "Profile name, e.g. `US_PII`."},
			"display_name": schema.StringAttribute{Computed: true},
			"description":  schema.StringAttribute{Computed: true},
			"region":       schema.StringAttribute{Computed: true},
			"category":     schema.StringAttribute{Computed: true},
			"detectors": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

func (d *DataProtectionProfileDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data DataProtectionProfileDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := dpCatalogTenant(data.TenantID, d.tenantID)
	profiles, err := d.sdk.DataProtectionPolicies().Profiles(ctx, tenantID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read data protection profiles", err.Error())
		return
	}
	want := data.Name.ValueString()
	for _, p := range profiles {
		if p.Name != nil && *p.Name == want {
			data.ID = types.StringValue(want)
			data.DisplayName = strPtrToTF(p.DisplayName)
			data.Description = strPtrToTF(p.Description)
			data.Region = strPtrToTF(p.Region)
			data.Category = strPtrToTF(p.Category)
			data.Detectors = profileDetectorIDs(p)
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
			return
		}
	}
	resp.Diagnostics.AddError(
		"Data protection profile not found",
		fmt.Sprintf("No built-in profile named %q. List them with the "+
			"ferentin_data_protection_profiles data source.", want),
	)
}
