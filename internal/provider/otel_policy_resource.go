package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// OtelPolicyResource is the `ferentin_otel_policy` Terraform resource —
// governs what telemetry the platform emits and to which sinks. v0.1
// exposes the flat / list attributes; nested config maps (Criteria,
// Processors, Filters, etc.) deferred to v0.2.
type OtelPolicyResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type OtelPolicyResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Priority    types.Int64  `tfsdk:"priority"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	Signals     types.List   `tfsdk:"signals"`  // []string — "traces", "metrics", "logs"
	SinkIDs     types.List   `tfsdk:"sink_ids"` // []string

	PolicyID  types.String `tfsdk:"policy_id"`
	SinkCount types.Int64  `tfsdk:"sink_count"`
	CreatedAt types.String `tfsdk:"created_at"`
	CreatedBy types.String `tfsdk:"created_by"`
	UpdatedAt types.String `tfsdk:"updated_at"`
	UpdatedBy types.String `tfsdk:"updated_by"`
}

func NewOtelPolicyResource() resource.Resource { return &OtelPolicyResource{} }

var (
	_ resource.Resource                = (*OtelPolicyResource)(nil)
	_ resource.ResourceWithConfigure   = (*OtelPolicyResource)(nil)
	_ resource.ResourceWithImportState = (*OtelPolicyResource)(nil)
)

func (r *OtelPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_otel_policy"
}

func (r *OtelPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected ProviderData, got %T", req.ProviderData))
		return
	}
	r.sdk = pd.SDK
	r.tenantID = pd.TenantID
}

func (r *OtelPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Tenant OTEL policy — selects which telemetry signals (traces / metrics / logs) " +
			"flow to which sinks, plus optional filtering / sampling. Nested processor / criteria configs " +
			"are deferred to v0.2.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant_id": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name":        schema.StringAttribute{Required: true},
			"description": schema.StringAttribute{Optional: true, Computed: true},
			"priority":    schema.Int64Attribute{Optional: true, Computed: true},
			"enabled":     schema.BoolAttribute{Optional: true, Computed: true},
			"signals": schema.ListAttribute{
				MarkdownDescription: "Signal types this policy applies to. Allowed: `traces`, `metrics`, `logs`.",
				Optional:            true, Computed: true,
				ElementType: types.StringType,
			},
			"sink_ids": schema.ListAttribute{
				MarkdownDescription: "UUIDs of sinks that receive matching telemetry. Pull from " +
					"`ferentin_otel_sink.<name>.sink_id`.",
				Optional: true, Computed: true,
				ElementType: types.StringType,
			},
			"policy_id":  schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"sink_count": schema.Int64Attribute{Computed: true},
			"created_at": schema.StringAttribute{Computed: true},
			"created_by": schema.StringAttribute{Computed: true},
			"updated_at": schema.StringAttribute{Computed: true},
			"updated_by": schema.StringAttribute{Computed: true},
		},
	}
}

func (r *OtelPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan OtelPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	// OtelPolicyCreate has Name, Priority, SinkIds as required non-pointer.
	body := adminapi.OtelPolicyCreate{
		Name:    plan.Name.ValueString(),
		SinkIds: stringListToSDK(ctx, plan.SinkIDs),
	}
	if !plan.Priority.IsNull() && !plan.Priority.IsUnknown() {
		body.Priority = int32(plan.Priority.ValueInt64())
	} else {
		body.Priority = 100
	}
	setStringPtr(plan.Description, &body.Description)
	setBoolPtr(plan.Enabled, &body.Enabled)
	if !plan.Signals.IsNull() && !plan.Signals.IsUnknown() {
		s := stringListToSDK(ctx, plan.Signals)
		body.Signals = &s
	}

	pol, err := r.sdk.OtelPolicies().Create(ctx, tenantID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create OTEL policy", err.Error())
		return
	}
	state := otelPolicyToModel(tenantID, pol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OtelPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OtelPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	pol, err := r.sdk.OtelPolicies().Get(ctx, tenantID, state.PolicyID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read OTEL policy", err.Error())
		return
	}
	refreshed := otelPolicyToModel(tenantID, pol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *OtelPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state OtelPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	// OtelPolicyUpdate is fully pointer-based (vs Create's required non-pointer
	// Name/Priority/SinkIds).
	body := adminapi.OtelPolicyUpdate{}
	setStringPtr(plan.Name, &body.Name)
	setInt32Ptr(plan.Priority, &body.Priority)
	setStringPtr(plan.Description, &body.Description)
	setBoolPtr(plan.Enabled, &body.Enabled)
	if !plan.Signals.IsNull() && !plan.Signals.IsUnknown() {
		s := stringListToSDK(ctx, plan.Signals)
		body.Signals = &s
	}
	if !plan.SinkIDs.IsNull() && !plan.SinkIDs.IsUnknown() {
		s := stringListToSDK(ctx, plan.SinkIDs)
		body.SinkIds = &s
	}

	v := strconv.FormatInt(0, 10) // OTEL policy doesn't expose version; send empty If-Match
	pol, err := r.sdk.OtelPolicies().Update(ctx, tenantID, state.PolicyID.ValueString(), v, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update OTEL policy", err.Error())
		return
	}
	refreshed := otelPolicyToModel(tenantID, pol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *OtelPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OtelPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	err := r.sdk.OtelPolicies().Delete(ctx, tenantID, state.PolicyID.ValueString(), "")
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		resp.Diagnostics.AddError("Failed to delete OTEL policy", err.Error())
	}
}

func (r *OtelPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, policyID string
	switch len(parts) {
	case 1:
		tenantID, policyID = r.tenantID, parts[0]
	case 2:
		tenantID, policyID = parts[0], parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>/<policy_id>` or configure `tenant_id` on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_id"), policyID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+policyID)...)
}

func (r *OtelPolicyResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

func otelPolicyToModel(tenantID string, pol *adminapi.OtelPolicy) OtelPolicyResourceModel {
	m := OtelPolicyResourceModel{TenantID: types.StringValue(tenantID)}
	if pol.Id != nil {
		m.PolicyID = types.StringValue(pol.Id.String())
	} else {
		m.PolicyID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.PolicyID.ValueString())
	m.Name = strPtrToTF(pol.Name)
	m.Description = strPtrToTF(pol.Description)
	m.Priority = int32PtrToTF(pol.Priority)
	m.Enabled = boolPtrOrDefault(pol.Enabled)
	m.SinkCount = int32PtrToTF(pol.SinkCount)
	m.CreatedAt = timePtrToTF(pol.CreatedAt)
	m.CreatedBy = strPtrToTF(pol.CreatedBy)
	m.UpdatedAt = timePtrToTF(pol.UpdatedAt)
	m.UpdatedBy = strPtrToTF(pol.UpdatedBy)
	m.Signals = stringSliceToList(pol.Signals)
	m.SinkIDs = stringSliceToList(pol.SinkIds)
	return m
}

// stringSliceToList is a helper for converting *[]string from SDK responses
// into a types.List.
func stringSliceToList(p *[]string) types.List {
	if p == nil {
		return types.ListNull(types.StringType)
	}
	elems := make([]attr.Value, 0, len(*p))
	for _, s := range *p {
		elems = append(elems, types.StringValue(s))
	}
	lv, _ := types.ListValue(types.StringType, elems)
	return lv
}
