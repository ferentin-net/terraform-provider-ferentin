package provider

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// List data sources for the unified MCP policy objects: every MCP policy, MCP
// route and scope with MCP scope defaults in the tenant, whoever created it.
// Same contract as the endpoint lists (endpoint_list_data_sources.go): each
// item carries the resource's `import_id` and the provenance pair, and items
// reuse the resources' own read mapping.

// ---------------------------------------------------------------------------
// ferentin_mcp_policies

const (
	// mcpPolicyListPageSize is the page size the list asks for. The platform
	// caps nothing (McpPolicyController.listMcpPolicies); 100 keeps one page
	// small enough to read and few enough pages for a large tenant.
	mcpPolicyListPageSize = 100
	// mcpPolicyListMaxPages bounds the paging loop, so a platform that kept
	// answering hasNext would fail the read instead of looping forever.
	mcpPolicyListMaxPages = 1000
)

type MCPPoliciesDataSource struct{ endpointListDataSource }

type MCPPoliciesDataSourceModel struct {
	TenantID types.String        `tfsdk:"tenant_id"`
	Policies []MCPPolicyListItem `tfsdk:"policies"`
}

type MCPPolicyListItem struct {
	PolicyID          types.String `tfsdk:"policy_id"`
	ImportID          types.String `tfsdk:"import_id"`
	Name              types.String `tfsdk:"name"`
	Priority          types.Int64  `tfsdk:"priority"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	Mode              types.String `tfsdk:"mode"`
	AppliesAt         types.String `tfsdk:"applies_at"`
	Effect            types.String `tfsdk:"effect"`
	ProviderInstances types.List   `tfsdk:"provider_instances"`
	TargetKinds       types.List   `tfsdk:"target_kinds"`
	SiblingOf         types.String `tfsdk:"sibling_of"`
	ManagedBy         types.String `tfsdk:"managed_by"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

func NewMCPPoliciesDataSource() datasource.DataSource { return &MCPPoliciesDataSource{} }

var _ datasource.DataSourceWithConfigure = (*MCPPoliciesDataSource)(nil)

func (d *MCPPoliciesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_policies"
}

func (d *MCPPoliciesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List every MCP policy in the tenant, remote and local, sorted by priority " +
			"then name. Includes `report_only` policies and the `<name> · local` rehearsal siblings the " +
			"platform split off (`sibling_of` set)." + endpointListAdoptionNote,
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Tenant to list. Defaults to the provider's."},
			"policies": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: withProvenance(map[string]schema.Attribute{
					"policy_id":  schema.StringAttribute{Computed: true},
					"import_id":  schema.StringAttribute{Computed: true, MarkdownDescription: "`<tenant_id>/<policy_id>`, for an `import` block."},
					"name":       schema.StringAttribute{Computed: true},
					"priority":   schema.Int64Attribute{Computed: true},
					"enabled":    schema.BoolAttribute{Computed: true},
					"mode":       schema.StringAttribute{Computed: true, MarkdownDescription: "`enforce` or `report_only`."},
					"applies_at": schema.StringAttribute{Computed: true, MarkdownDescription: "`admit`, `call` or `both`."},
					"effect":     schema.StringAttribute{Computed: true, MarkdownDescription: "The effect type: `allow`, `deny` or `step_up`."},
					"provider_instances": schema.ListAttribute{Computed: true, ElementType: types.StringType,
						MarkdownDescription: "MCP server UUIDs the policy names. Empty when it names device targets only."},
					"target_kinds": schema.ListAttribute{Computed: true, ElementType: types.StringType,
						MarkdownDescription: "The kinds of the device targets the policy names, in stored order. Empty when it names none."},
					"sibling_of": schema.StringAttribute{Computed: true,
						MarkdownDescription: "Set on a `<name> · local` rehearsal policy: the policy it was split from."},
				})},
			},
		},
	}
}

func (d *MCPPoliciesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data MCPPoliciesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := dpCatalogTenant(data.TenantID, d.tenantID)
	policies, err := listAllMCPPolicies(ctx, d.sdk.MCPPolicies(), tenantID)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to list MCP policies", err)
		return
	}
	data.Policies = make([]MCPPolicyListItem, 0, len(policies))
	for i := range policies {
		m := mcpPolicyToModel(tenantID, &policies[i])
		effect := types.StringNull()
		if m.Effect != nil {
			effect = m.Effect.Type
		}
		kinds := make([]string, 0, len(m.Targets))
		for _, t := range m.Targets {
			kinds = append(kinds, t.Kind.ValueString())
		}
		instances := m.ProviderInstances
		if instances.IsNull() {
			instances = types.ListValueMust(types.StringType, nil)
		}
		data.Policies = append(data.Policies, MCPPolicyListItem{
			PolicyID: m.PolicyID, ImportID: m.ID, Name: m.Name, Priority: m.Priority,
			Enabled: enabledOrDefault(m.Enabled), Mode: m.Mode, AppliesAt: m.AppliesAt, Effect: effect,
			ProviderInstances: instances, TargetKinds: stringSliceToList(&kinds), SiblingOf: m.SiblingOf,
			ManagedBy: m.ManagedBy, LastModifiedBy: m.LastModifiedBy,
		})
	}
	sort.SliceStable(data.Policies, func(i, j int) bool {
		return ruleOrderLess(data.Policies[i].Priority, data.Policies[i].Name, data.Policies[j].Priority, data.Policies[j].Name)
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// listAllMCPPolicies reads every page of /mcp-policies. Pages are 1-based and
// the platform orders them by priority then created_at (McpPolicyRepository),
// with LIMIT/OFFSET, so a policy written mid-read can shift a row across a page
// boundary: rows are de-duplicated by id, and a read that ends with fewer rows
// than the first page's total is an error rather than a silently short list.
func listAllMCPPolicies(ctx context.Context, api adminapi.MCPPoliciesAPI, tenantID string) ([]adminapi.MCPPolicy, error) {
	size := int32(mcpPolicyListPageSize)
	seen := map[string]bool{}
	var out []adminapi.MCPPolicy
	var total int64 = -1
	for page := int32(1); page <= mcpPolicyListMaxPages; page++ {
		p := page
		res, err := api.List(ctx, tenantID, &adminapi.ListMCPPoliciesParams{Page: &p, Size: &size})
		if err != nil {
			return nil, err
		}
		items := []adminapi.MCPPolicy{}
		if res.Items != nil {
			items = *res.Items
		}
		for _, pol := range items {
			key := ""
			if pol.Id != nil {
				key = pol.Id.String()
			}
			if key != "" && seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, pol)
		}
		if total < 0 && res.Pagination != nil && res.Pagination.Total != nil {
			total = *res.Pagination.Total
		}
		hasNext := res.Pagination != nil && res.Pagination.HasNext != nil && *res.Pagination.HasNext
		if !hasNext || len(items) == 0 {
			if total >= 0 && int64(len(out)) < total {
				return nil, fmt.Errorf("listed %d MCP policies but the platform reported %d: policies changed "+
					"during the read; run it again", len(out), total)
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("listing MCP policies did not finish within %d pages of %d", mcpPolicyListMaxPages, size)
}

// ---------------------------------------------------------------------------
// ferentin_mcp_routes

type MCPRoutesDataSource struct{ endpointListDataSource }

type MCPRoutesDataSourceModel struct {
	TenantID types.String       `tfsdk:"tenant_id"`
	Routes   []MCPRouteListItem `tfsdk:"routes"`
}

type MCPRouteListItem struct {
	RouteID        types.String `tfsdk:"route_id"`
	ImportID       types.String `tfsdk:"import_id"`
	Name           types.String `tfsdk:"name"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	TargetKind     types.String `tfsdk:"target_kind"`
	TargetValues   types.List   `tfsdk:"target_values"`
	Mechanism      types.String `tfsdk:"mechanism"`
	SteerToURL     types.String `tfsdk:"steer_to_url"`
	DeviceGroupIDs types.List   `tfsdk:"device_group_ids"`
	ManagedBy      types.String `tfsdk:"managed_by"`
	LastModifiedBy types.String `tfsdk:"last_modified_by"`
}

func NewMCPRoutesDataSource() datasource.DataSource { return &MCPRoutesDataSource{} }

var _ datasource.DataSourceWithConfigure = (*MCPRoutesDataSource)(nil)

func (d *MCPRoutesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_routes"
}

func (d *MCPRoutesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List every MCP route in the tenant, sorted by name. Includes the routes the " +
			"platform's endpoint MCP rule conversion made from `steer` rules." + endpointListAdoptionNote,
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Tenant to list. Defaults to the provider's."},
			"routes": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: withProvenance(map[string]schema.Attribute{
					"route_id":    schema.StringAttribute{Computed: true},
					"import_id":   schema.StringAttribute{Computed: true, MarkdownDescription: "`<tenant_id>/<route_id>`, for an `import` block."},
					"name":        schema.StringAttribute{Computed: true},
					"enabled":     schema.BoolAttribute{Computed: true},
					"target_kind": schema.StringAttribute{Computed: true, MarkdownDescription: "`host`, `url_prefix`, `package` or `argv_contains`."},
					"target_values": schema.ListAttribute{Computed: true, ElementType: types.StringType,
						MarkdownDescription: "The target's values as the platform stores them."},
					"mechanism":    schema.StringAttribute{Computed: true, MarkdownDescription: "`shim_wrap` (local) or `rewrite_to_gateway` (remote)."},
					"steer_to_url": schema.StringAttribute{Computed: true, MarkdownDescription: "The gateway route a remote route rewrites to. Null: the scope's own gateway."},
					"device_group_ids": schema.ListAttribute{Computed: true, ElementType: types.StringType,
						MarkdownDescription: "Device groups the route is narrowed to. Empty: every device."},
				})},
			},
		},
	}
}

func (d *MCPRoutesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data MCPRoutesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := dpCatalogTenant(data.TenantID, d.tenantID)
	routes, err := d.sdk.MCPRoutes().List(ctx, tenantID)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to list MCP routes", err)
		return
	}
	data.Routes = make([]MCPRouteListItem, 0, len(routes))
	for i := range routes {
		m := mcpRouteToModel(tenantID, &routes[i])
		kind := types.StringNull()
		if m.Target != nil {
			kind = m.Target.Kind
		}
		groups := m.DeviceGroupIDs
		if groups.IsNull() {
			groups = types.ListValueMust(types.StringType, nil)
		}
		data.Routes = append(data.Routes, MCPRouteListItem{
			RouteID: m.RouteID, ImportID: m.ID, Name: m.Name, Enabled: enabledOrDefault(m.Enabled),
			TargetKind: kind, TargetValues: m.StoredTargetValues, Mechanism: m.Mechanism,
			SteerToURL: m.SteerToURL, DeviceGroupIDs: groups,
			ManagedBy: m.ManagedBy, LastModifiedBy: m.LastModifiedBy,
		})
	}
	sort.SliceStable(data.Routes, func(i, j int) bool {
		return data.Routes[i].Name.ValueString() < data.Routes[j].Name.ValueString()
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// ferentin_mcp_scope_defaults_scopes

type MCPScopeDefaultsScopesDataSource struct{ endpointListDataSource }

type MCPScopeDefaultsScopesDataSourceModel struct {
	TenantID types.String                `tfsdk:"tenant_id"`
	Scopes   []MCPScopeDefaultsScopeItem `tfsdk:"scopes"`
}

type MCPScopeDefaultsScopeItem struct {
	DeviceGroupID      types.String `tfsdk:"device_group_id"`
	ImportID           types.String `tfsdk:"import_id"`
	Source             types.String `tfsdk:"source"`
	ServerApproval     types.String `tfsdk:"server_approval"`
	ServerApprovalMode types.String `tfsdk:"server_approval_mode"`
	Unattributed       types.String `tfsdk:"unattributed"`
	UnattributedMode   types.String `tfsdk:"unattributed_mode"`
	ToolVettingMode    types.String `tfsdk:"tool_vetting_mode"`
	Version            types.Int64  `tfsdk:"version"`
}

func NewMCPScopeDefaultsScopesDataSource() datasource.DataSource {
	return &MCPScopeDefaultsScopesDataSource{}
}

var _ datasource.DataSourceWithConfigure = (*MCPScopeDefaultsScopesDataSource)(nil)

func (d *MCPScopeDefaultsScopesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_scope_defaults_scopes"
}

func (d *MCPScopeDefaultsScopesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List every scope with MCP scope defaults of its own — one " +
			"`ferentin_mcp_scope_defaults` per scope: the tenant default first, then each device group " +
			"that has its own posture row. A group with no row of its own runs the tenant default's and " +
			"is not listed.\n\n" +
			"The defaults are an API over the endpoint posture row, so a scope here is also a row of " +
			"`ferentin_endpoint_policy_settings_scopes`, and the two share `version`. The scope " +
			"defaults carry no provenance of their own; that row's `managed_by` is the one to check.\n\n" +
			"Every item carries `import_id`, the ID `ferentin_mcp_scope_defaults`' `import` block takes.",
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Tenant to list. Defaults to the provider's."},
			"scopes": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"device_group_id": schema.StringAttribute{Computed: true,
						MarkdownDescription: "The device group. Null on the tenant default."},
					"import_id": schema.StringAttribute{Computed: true,
						MarkdownDescription: "`<tenant_id>` for the tenant default, `<tenant_id>/<device_group_id>` for a group."},
					"source": schema.StringAttribute{Computed: true,
						MarkdownDescription: "`device_group`, `tenant` or `built_in`: where the values come from."},
					"server_approval":      schema.StringAttribute{Computed: true},
					"server_approval_mode": schema.StringAttribute{Computed: true},
					"unattributed":         schema.StringAttribute{Computed: true},
					"unattributed_mode":    schema.StringAttribute{Computed: true},
					"tool_vetting_mode": schema.StringAttribute{Computed: true,
						MarkdownDescription: "The scope's own value; `inherit` or null on a group that follows the tenant's."},
					"version": schema.Int64Attribute{Computed: true,
						MarkdownDescription: "The posture row's version. Null for a tenant with no posture row yet."},
				}},
			},
		},
	}
}

func (d *MCPScopeDefaultsScopesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data MCPScopeDefaultsScopesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := dpCatalogTenant(data.TenantID, d.tenantID)

	// The tenant default always has defaults, a row or not (built_in).
	tenant, err := d.sdk.MCPScopeDefaults().GetTenant(ctx, tenantID)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to read the tenant's MCP scope defaults", err)
		return
	}
	data.Scopes = []MCPScopeDefaultsScopeItem{scopeDefaultsListItem(tenantID, types.StringNull(), tenant)}

	// A device group has defaults of its own exactly when it has a posture row.
	rows, err := d.sdk.EndpointPolicies().ListSettings(ctx, tenantID)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to list endpoint posture rows", err)
		return
	}
	var groups []string
	for _, row := range rows {
		if row.DeviceGroupId != nil {
			groups = append(groups, row.DeviceGroupId.String())
		}
	}
	sort.Strings(groups)
	for _, g := range groups {
		got, err := d.sdk.MCPScopeDefaults().GetGroup(ctx, tenantID, g)
		if err != nil {
			addSDKError(&resp.Diagnostics, fmt.Sprintf("Failed to read device group %s's MCP scope defaults", g), err)
			return
		}
		data.Scopes = append(data.Scopes, scopeDefaultsListItem(tenantID, types.StringValue(g), got))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func scopeDefaultsListItem(tenantID string, groupID types.String, d *adminapi.MCPScopeDefaults) MCPScopeDefaultsScopeItem {
	m := scopeDefaultsToModel(tenantID, groupID, d)
	return MCPScopeDefaultsScopeItem{
		DeviceGroupID: m.DeviceGroupID, ImportID: m.ID, Source: m.Source,
		ServerApproval: m.ServerApproval, ServerApprovalMode: m.ServerApprovalMode,
		Unattributed: m.Unattributed, UnattributedMode: m.UnattributedMode,
		ToolVettingMode: m.ToolVettingMode, Version: m.Version,
	}
}
