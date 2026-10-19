package provider

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// List data sources for the endpoint surface: every device group, destination
// rule, MCP rule and posture row in the tenant, whoever created it. Their first job is
// adoption — finding what the admin console created so it can be imported —
// which is why every item carries `import_id` (the resource's import ID) and
// `managed_by` (who created it). Items reuse the resources' own read mapping,
// so a list item and the resource's state agree on every shared field.

// endpointListDataSource holds what all three share: the SDK client and the
// provider's default tenant.
type endpointListDataSource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

func (d *endpointListDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

const endpointListAdoptionNote = "\n\nEvery item carries `import_id`, the ID its resource's `import` block takes, and " +
	"`managed_by`, who created it (`console`, `iac`, …). Filter on `managed_by = \"console\"` to find " +
	"what Terraform does not manage yet."

var endpointListProvenanceAttrs = map[string]schema.Attribute{
	"managed_by": schema.StringAttribute{Computed: true,
		MarkdownDescription: "Who created it: `console`, `iac`, `cli`, `system` or `unknown`. Never changes."},
	"last_modified_by": schema.StringAttribute{Computed: true,
		MarkdownDescription: "Who wrote it last. Divergence from `managed_by` means it changed hands."},
}

func withProvenance(attrs map[string]schema.Attribute) map[string]schema.Attribute {
	for k, v := range endpointListProvenanceAttrs {
		attrs[k] = v
	}
	return attrs
}

// ---------------------------------------------------------------------------
// ferentin_device_groups

type DeviceGroupsDataSource struct{ endpointListDataSource }

type DeviceGroupsDataSourceModel struct {
	TenantID types.String          `tfsdk:"tenant_id"`
	Groups   []DeviceGroupListItem `tfsdk:"groups"`
}

type DeviceGroupListItem struct {
	GroupID        types.String `tfsdk:"group_id"`
	ImportID       types.String `tfsdk:"import_id"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	Source         types.String `tfsdk:"source"`
	ExternalID     types.String `tfsdk:"external_id"`
	ManagedBy      types.String `tfsdk:"managed_by"`
	LastModifiedBy types.String `tfsdk:"last_modified_by"`
}

func NewDeviceGroupsDataSource() datasource.DataSource { return &DeviceGroupsDataSource{} }

var _ datasource.DataSourceWithConfigure = (*DeviceGroupsDataSource)(nil)

func (d *DeviceGroupsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_device_groups"
}

func (d *DeviceGroupsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List every device group in the tenant, sorted by name." + endpointListAdoptionNote,
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Tenant to list. Defaults to the provider's."},
			"groups": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: withProvenance(map[string]schema.Attribute{
					"group_id":    schema.StringAttribute{Computed: true},
					"import_id":   schema.StringAttribute{Computed: true, MarkdownDescription: "`<tenant_id>/<group_id>`, for an `import` block."},
					"name":        schema.StringAttribute{Computed: true},
					"description": schema.StringAttribute{Computed: true},
					"source":      schema.StringAttribute{Computed: true, MarkdownDescription: "`manual`, `scim`, `mdm`, … Immutable on the resource."},
					"external_id": schema.StringAttribute{Computed: true, MarkdownDescription: "Identifier in the upstream system. Immutable on the resource."},
				})},
			},
		},
	}
}

func (d *DeviceGroupsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data DeviceGroupsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := dpCatalogTenant(data.TenantID, d.tenantID)
	groups, err := d.sdk.DeviceGroups().List(ctx, tenantID)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to list device groups", err)
		return
	}
	data.Groups = make([]DeviceGroupListItem, 0, len(groups))
	for i := range groups {
		m := deviceGroupToModel(tenantID, &groups[i])
		data.Groups = append(data.Groups, DeviceGroupListItem{
			GroupID: m.GroupID, ImportID: m.ID, Name: m.Name, Description: m.Description,
			Source: m.Source, ExternalID: m.ExternalID,
			ManagedBy: m.ManagedBy, LastModifiedBy: m.LastModifiedBy,
		})
	}
	sort.SliceStable(data.Groups, func(i, j int) bool {
		return data.Groups[i].Name.ValueString() < data.Groups[j].Name.ValueString()
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// ferentin_endpoint_destination_rules

type EndpointDestinationRulesDataSource struct{ endpointListDataSource }

type EndpointDestinationRulesDataSourceModel struct {
	TenantID types.String                      `tfsdk:"tenant_id"`
	Rules    []EndpointDestinationRuleListItem `tfsdk:"rules"`
}

type EndpointDestinationRuleListItem struct {
	RuleID           types.String `tfsdk:"rule_id"`
	ImportID         types.String `tfsdk:"import_id"`
	Name             types.String `tfsdk:"name"`
	Priority         types.Int64  `tfsdk:"priority"`
	Enabled          types.Bool   `tfsdk:"enabled"`
	Action           types.String `tfsdk:"action"`
	DestinationKind  types.String `tfsdk:"destination_kind"`
	CatalogSlug      types.String `tfsdk:"catalog_slug"`
	DestinationHosts types.List   `tfsdk:"destination_hosts"`
	DeviceGroupIDs   types.List   `tfsdk:"device_group_ids"`
	ManagedBy        types.String `tfsdk:"managed_by"`
	LastModifiedBy   types.String `tfsdk:"last_modified_by"`
}

func NewEndpointDestinationRulesDataSource() datasource.DataSource {
	return &EndpointDestinationRulesDataSource{}
}

var _ datasource.DataSourceWithConfigure = (*EndpointDestinationRulesDataSource)(nil)

func (d *EndpointDestinationRulesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_endpoint_destination_rules"
}

func (d *EndpointDestinationRulesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List every endpoint destination rule in the tenant, in evaluation order " +
			"(ascending `priority`; equal priorities, which the platform allows, are listed by name)." +
			endpointListAdoptionNote,
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Tenant to list. Defaults to the provider's."},
			"rules": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: withProvenance(map[string]schema.Attribute{
					"rule_id":           schema.StringAttribute{Computed: true},
					"import_id":         schema.StringAttribute{Computed: true, MarkdownDescription: "`<tenant_id>/<rule_id>`, for an `import` block."},
					"name":              schema.StringAttribute{Computed: true},
					"priority":          schema.Int64Attribute{Computed: true},
					"enabled":           schema.BoolAttribute{Computed: true},
					"action":            schema.StringAttribute{Computed: true},
					"destination_kind":  schema.StringAttribute{Computed: true},
					"catalog_slug":      schema.StringAttribute{Computed: true},
					"destination_hosts": schema.ListAttribute{Computed: true, ElementType: types.StringType},
					"device_group_ids":  schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Null = every device in the tenant."},
				})},
			},
		},
	}
}

func (d *EndpointDestinationRulesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data EndpointDestinationRulesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := dpCatalogTenant(data.TenantID, d.tenantID)
	rules, err := d.sdk.EndpointPolicies().ListRules(ctx, tenantID)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to list endpoint destination rules", err)
		return
	}
	data.Rules = make([]EndpointDestinationRuleListItem, 0, len(rules))
	for i := range rules {
		// Criteria are not exposed here, so their diagnostics are not either.
		m := endpointRuleToModel(tenantID, &rules[i], &diag.Diagnostics{})
		data.Rules = append(data.Rules, EndpointDestinationRuleListItem{
			RuleID: m.RuleID, ImportID: m.ID, Name: m.Name, Priority: m.Priority, Enabled: enabledOrDefault(m.Enabled),
			Action: m.Action, DestinationKind: m.DestinationKind, CatalogSlug: m.CatalogSlug,
			DestinationHosts: m.DestinationHosts, DeviceGroupIDs: m.DeviceGroupIDs,
			ManagedBy: m.ManagedBy, LastModifiedBy: m.LastModifiedBy,
		})
	}
	sort.SliceStable(data.Rules, func(i, j int) bool {
		return ruleOrderLess(data.Rules[i].Priority, data.Rules[i].Name, data.Rules[j].Priority, data.Rules[j].Name)
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// ferentin_endpoint_mcp_rules

type EndpointMCPRulesDataSource struct{ endpointListDataSource }

type EndpointMCPRulesDataSourceModel struct {
	TenantID types.String              `tfsdk:"tenant_id"`
	Rules    []EndpointMCPRuleListItem `tfsdk:"rules"`
}

type EndpointMCPRuleListItem struct {
	RuleID         types.String `tfsdk:"rule_id"`
	ImportID       types.String `tfsdk:"import_id"`
	Name           types.String `tfsdk:"name"`
	Priority       types.Int64  `tfsdk:"priority"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	Action         types.String `tfsdk:"action"`
	MatchKind      types.String `tfsdk:"match_kind"`
	MatchValues    types.List   `tfsdk:"match_values"`
	DeviceGroupIDs types.List   `tfsdk:"device_group_ids"`
	Enforcement    types.String `tfsdk:"enforcement"`
	ManagedBy      types.String `tfsdk:"managed_by"`
	LastModifiedBy types.String `tfsdk:"last_modified_by"`
}

func NewEndpointMCPRulesDataSource() datasource.DataSource { return &EndpointMCPRulesDataSource{} }

var _ datasource.DataSourceWithConfigure = (*EndpointMCPRulesDataSource)(nil)

func (d *EndpointMCPRulesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_endpoint_mcp_rules"
}

func (d *EndpointMCPRulesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List every endpoint MCP rule in the tenant (remote and local server rules, " +
			"tool call and behaviour rules), in evaluation order (ascending `priority`; equal priorities, " +
			"which the platform allows, are listed by name)." +
			endpointListAdoptionNote,
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Tenant to list. Defaults to the provider's."},
			"rules": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: withProvenance(map[string]schema.Attribute{
					"rule_id":          schema.StringAttribute{Computed: true},
					"import_id":        schema.StringAttribute{Computed: true, MarkdownDescription: "`<tenant_id>/<rule_id>`, for an `import` block."},
					"name":             schema.StringAttribute{Computed: true},
					"priority":         schema.Int64Attribute{Computed: true},
					"enabled":          schema.BoolAttribute{Computed: true},
					"action":           schema.StringAttribute{Computed: true},
					"match_kind":       schema.StringAttribute{Computed: true},
					"match_values":     schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "As the platform stores them, after normalization."},
					"device_group_ids": schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Null = every device in the tenant."},
					"enforcement":      schema.StringAttribute{Computed: true, MarkdownDescription: "Whether the fleet enforces the rule: `active`, `pending`, `not_enforced` or `never_matched`."},
				})},
			},
		},
	}
}

func (d *EndpointMCPRulesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data EndpointMCPRulesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := dpCatalogTenant(data.TenantID, d.tenantID)
	rules, err := d.sdk.EndpointMCPRules().List(ctx, tenantID)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to list endpoint MCP rules", err)
		return
	}
	data.Rules = make([]EndpointMCPRuleListItem, 0, len(rules))
	for i := range rules {
		m := endpointMCPRuleToModel(tenantID, &rules[i], &diag.Diagnostics{})
		data.Rules = append(data.Rules, EndpointMCPRuleListItem{
			RuleID: m.RuleID, ImportID: m.ID, Name: m.Name, Priority: m.Priority, Enabled: enabledOrDefault(m.Enabled),
			Action: m.Action, MatchKind: m.MatchKind, MatchValues: m.StoredMatchValues,
			DeviceGroupIDs: m.DeviceGroupIDs, Enforcement: m.Enforcement,
			ManagedBy: m.ManagedBy, LastModifiedBy: m.LastModifiedBy,
		})
	}
	sort.SliceStable(data.Rules, func(i, j int) bool {
		return ruleOrderLess(data.Rules[i].Priority, data.Rules[i].Name, data.Rules[j].Priority, data.Rules[j].Name)
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// ferentin_endpoint_policy_settings_scopes

type EndpointPolicySettingsScopesDataSource struct{ endpointListDataSource }

type EndpointPolicySettingsScopesDataSourceModel struct {
	TenantID types.String                      `tfsdk:"tenant_id"`
	Scopes   []EndpointPolicySettingsScopeItem `tfsdk:"scopes"`
}

type EndpointPolicySettingsScopeItem struct {
	DeviceGroupID            types.String `tfsdk:"device_group_id"`
	ImportID                 types.String `tfsdk:"import_id"`
	UnapprovedMcpAction      types.String `tfsdk:"unapproved_mcp_action"`
	DefaultDestinationAction types.String `tfsdk:"default_destination_action"`
	HookRuleEnforcement      types.String `tfsdk:"hook_rule_enforcement"`
	ManagedBy                types.String `tfsdk:"managed_by"`
	LastModifiedBy           types.String `tfsdk:"last_modified_by"`
}

func NewEndpointPolicySettingsScopesDataSource() datasource.DataSource {
	return &EndpointPolicySettingsScopesDataSource{}
}

var _ datasource.DataSourceWithConfigure = (*EndpointPolicySettingsScopesDataSource)(nil)

func (d *EndpointPolicySettingsScopesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_endpoint_policy_settings_scopes"
}

func (d *EndpointPolicySettingsScopesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List every endpoint posture row in the tenant — one " +
			"`ferentin_endpoint_policy_settings` per scope: the tenant default first, then each device " +
			"group that has its own override. A device group with no override has no row here; it " +
			"resolves through the tenant default." + endpointListAdoptionNote,
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Tenant to list. Defaults to the provider's."},
			"scopes": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: withProvenance(map[string]schema.Attribute{
					"device_group_id": schema.StringAttribute{Computed: true,
						MarkdownDescription: "The group this row overrides. Null on the tenant default."},
					"import_id": schema.StringAttribute{Computed: true,
						MarkdownDescription: "`<tenant_id>` for the tenant default, `<tenant_id>/<device_group_id>` for an override."},
					"unapproved_mcp_action":      schema.StringAttribute{Computed: true},
					"default_destination_action": schema.StringAttribute{Computed: true},
					"hook_rule_enforcement":      schema.StringAttribute{Computed: true},
				})},
			},
		},
	}
}

func (d *EndpointPolicySettingsScopesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data EndpointPolicySettingsScopesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := dpCatalogTenant(data.TenantID, d.tenantID)
	rows, err := d.sdk.EndpointPolicies().ListSettings(ctx, tenantID)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to list endpoint policy settings", err)
		return
	}
	data.Scopes = make([]EndpointPolicySettingsScopeItem, 0, len(rows))
	for i := range rows {
		m := endpointSettingsToModel(tenantID, &rows[i])
		data.Scopes = append(data.Scopes, EndpointPolicySettingsScopeItem{
			DeviceGroupID: m.DeviceGroupID, ImportID: m.ID,
			UnapprovedMcpAction: m.UnapprovedMcpAction, DefaultDestinationAction: m.DefaultDestinationAction,
			HookRuleEnforcement: m.HookRuleEnforcement,
			ManagedBy:           m.ManagedBy, LastModifiedBy: m.LastModifiedBy,
		})
	}
	// Tenant default first, then overrides by group id, so the list is stable.
	sort.SliceStable(data.Scopes, func(i, j int) bool {
		gi, gj := data.Scopes[i].DeviceGroupID, data.Scopes[j].DeviceGroupID
		if gi.IsNull() != gj.IsNull() {
			return gi.IsNull()
		}
		return gi.ValueString() < gj.ValueString()
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ruleOrderLess orders rules by ascending priority, the order devices evaluate
// them in. The platform allows two rules the same priority; the name is only a
// stable tie-break for this list, not a claim about which one a device tries
// first.
func ruleOrderLess(pi types.Int64, ni types.String, pj types.Int64, nj types.String) bool {
	if pi.ValueInt64() != pj.ValueInt64() {
		return pi.ValueInt64() < pj.ValueInt64()
	}
	return ni.ValueString() < nj.ValueString()
}

// enabledOrDefault reads a rule the platform returned without `enabled` as
// enabled, the rule default. The resources get the same from their schema
// default; a list item has none.
func enabledOrDefault(v types.Bool) types.Bool {
	if v.IsNull() {
		return types.BoolValue(true)
	}
	return v
}
