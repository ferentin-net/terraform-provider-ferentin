package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// OtelSinkResource is the `ferentin_otel_sink` Terraform resource — a
// telemetry sink (Datadog, Honeycomb, OTLP, …) for a tenant. Skips the
// nested batch_config / retry_config / tls_config maps for v0.1; those
// can be added incrementally when customers ask.
type OtelSinkResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type OtelSinkResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	// Required
	Name     types.String `tfsdk:"name"`
	Endpoint types.String `tfsdk:"endpoint"`
	SinkType types.String `tfsdk:"sink_type"`

	// Optional
	Description types.String `tfsdk:"description"`
	Provider    types.String `tfsdk:"provider_slug"` // "provider" collides with HCL keyword; use provider_slug
	Region      types.String `tfsdk:"region"`
	Protocol    types.String `tfsdk:"protocol"`
	Compression types.String `tfsdk:"compression"`
	AuthType    types.String `tfsdk:"auth_type"`
	Timeout     types.String `tfsdk:"timeout"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	Headers     types.Map    `tfsdk:"headers"`
	Tags        types.Map    `tfsdk:"tags"`

	// Computed
	SinkID         types.String `tfsdk:"sink_id"`
	HasCredentials types.Bool   `tfsdk:"has_credentials"`
	CreatedAt      types.String `tfsdk:"created_at"`
	CreatedBy      types.String `tfsdk:"created_by"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
	UpdatedBy      types.String `tfsdk:"updated_by"`
}

func NewOtelSinkResource() resource.Resource { return &OtelSinkResource{} }

var (
	_ resource.Resource                = (*OtelSinkResource)(nil)
	_ resource.ResourceWithConfigure   = (*OtelSinkResource)(nil)
	_ resource.ResourceWithImportState = (*OtelSinkResource)(nil)
)

func (r *OtelSinkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_otel_sink"
}

func (r *OtelSinkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *OtelSinkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A tenant OTEL sink — a telemetry destination (Datadog, Honeycomb, OTLP, …). " +
			"Credentials for the sink are managed out-of-band via the `/credentials` sub-resource for v0.1; " +
			"a future write-only `credentials` attribute is planned.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant_id": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name":         schema.StringAttribute{Required: true},
			"endpoint":     schema.StringAttribute{Required: true, MarkdownDescription: "Sink endpoint URL."},
			"sink_type":    schema.StringAttribute{Required: true, MarkdownDescription: "Sink type (e.g. `otlp_grpc`, `otlp_http`, `datadog`)."},
			"description":  schema.StringAttribute{Optional: true, Computed: true},
			"provider_slug": schema.StringAttribute{Optional: true, Computed: true,
				MarkdownDescription: "Catalog provider slug — pull from `data \"ferentin_otel_sink_provider\"`."},
			"region":      schema.StringAttribute{Optional: true, Computed: true},
			"protocol":    schema.StringAttribute{Optional: true, Computed: true},
			"compression": schema.StringAttribute{Optional: true, Computed: true},
			"auth_type":   schema.StringAttribute{Optional: true, Computed: true},
			"timeout":     schema.StringAttribute{Optional: true, Computed: true},
			"enabled":     schema.BoolAttribute{Optional: true, Computed: true},
			"headers": schema.MapAttribute{
				MarkdownDescription: "Custom HTTP headers sent on every export to this sink.",
				Optional:            true, Computed: true,
				ElementType: types.StringType,
			},
			"tags": schema.MapAttribute{
				Optional: true, Computed: true,
				ElementType: types.StringType,
			},
			"sink_id":         schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"has_credentials": schema.BoolAttribute{Computed: true},
			"created_at":      schema.StringAttribute{Computed: true},
			"created_by":      schema.StringAttribute{Computed: true},
			"updated_at":      schema.StringAttribute{Computed: true},
			"updated_by":      schema.StringAttribute{Computed: true},
		},
	}
}

func (r *OtelSinkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan OtelSinkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	body := adminapi.OtelSinkCreate{
		Name:     plan.Name.ValueString(),
		Endpoint: plan.Endpoint.ValueString(),
		SinkType: gen.OtelSinkCreateRequestSinkType(plan.SinkType.ValueString()),
	}
	r.fillOptionals(ctx, &plan, &body)

	sink, err := r.sdk.OtelSinks().Create(ctx, tenantID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create OTEL sink", err.Error())
		return
	}
	state := otelSinkToModel(tenantID, sink)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OtelSinkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OtelSinkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	sink, err := r.sdk.OtelSinks().Get(ctx, tenantID, state.SinkID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read OTEL sink", err.Error())
		return
	}
	refreshed := otelSinkToModel(tenantID, sink)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *OtelSinkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state OtelSinkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	body := adminapi.OtelSinkUpdate{}
	setStringPtr(plan.Name, &body.Name)
	setStringPtr(plan.Endpoint, &body.Endpoint)
	setStringPtr(plan.Description, &body.Description)
	setStringPtr(plan.Region, &body.Region)
	setStringPtr(plan.Timeout, &body.Timeout)
	setBoolPtr(plan.Enabled, &body.Enabled)
	if !plan.Headers.IsNull() && !plan.Headers.IsUnknown() {
		var h map[string]string
		_ = plan.Headers.ElementsAs(ctx, &h, false)
		body.Headers = &h
	}
	if !plan.Tags.IsNull() && !plan.Tags.IsUnknown() {
		var t map[string]string
		_ = plan.Tags.ElementsAs(ctx, &t, false)
		body.Tags = &t
	}

	sink, err := r.sdk.OtelSinks().Update(ctx, tenantID, state.SinkID.ValueString(), "", body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update OTEL sink", err.Error())
		return
	}
	refreshed := otelSinkToModel(tenantID, sink)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *OtelSinkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OtelSinkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	err := r.sdk.OtelSinks().Delete(ctx, tenantID, state.SinkID.ValueString(), "")
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		resp.Diagnostics.AddError("Failed to delete OTEL sink", err.Error())
	}
}

func (r *OtelSinkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	var tenantID, sinkID string
	switch len(parts) {
	case 1:
		tenantID, sinkID = r.tenantID, parts[0]
	case 2:
		tenantID, sinkID = parts[0], parts[1]
	}
	if tenantID == "" {
		resp.Diagnostics.AddError("Cannot determine tenant for import",
			"Pass `<tenant_id>/<sink_id>` or configure `tenant_id` on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("sink_id"), sinkID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+sinkID)...)
}

func (r *OtelSinkResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

func (r *OtelSinkResource) fillOptionals(ctx context.Context, plan *OtelSinkResourceModel, body *adminapi.OtelSinkCreate) {
	setStringPtr(plan.Description, &body.Description)
	setStringPtr(plan.Provider, &body.Provider)
	setStringPtr(plan.Region, &body.Region)
	setStringPtr(plan.Timeout, &body.Timeout)
	setBoolPtr(plan.Enabled, &body.Enabled)
	if !plan.Protocol.IsNull() && !plan.Protocol.IsUnknown() {
		v := gen.OtelSinkCreateRequestProtocol(plan.Protocol.ValueString())
		body.Protocol = &v
	}
	if !plan.Compression.IsNull() && !plan.Compression.IsUnknown() {
		v := gen.OtelSinkCreateRequestCompression(plan.Compression.ValueString())
		body.Compression = &v
	}
	if !plan.AuthType.IsNull() && !plan.AuthType.IsUnknown() {
		v := gen.OtelSinkCreateRequestAuthType(plan.AuthType.ValueString())
		body.AuthType = &v
	}
	if !plan.Headers.IsNull() && !plan.Headers.IsUnknown() {
		var h map[string]string
		_ = plan.Headers.ElementsAs(ctx, &h, false)
		body.Headers = &h
	}
	if !plan.Tags.IsNull() && !plan.Tags.IsUnknown() {
		var t map[string]string
		_ = plan.Tags.ElementsAs(ctx, &t, false)
		body.Tags = &t
	}
}

func otelSinkToModel(tenantID string, sink *adminapi.OtelSink) OtelSinkResourceModel {
	m := OtelSinkResourceModel{TenantID: types.StringValue(tenantID)}
	if sink.Id != nil {
		m.SinkID = types.StringValue(sink.Id.String())
	} else {
		m.SinkID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.SinkID.ValueString())
	m.Name = strPtrToTF(sink.Name)
	m.Endpoint = strPtrToTF(sink.Endpoint)
	m.SinkType = strPtrToTF(sink.SinkType)
	m.Description = strPtrToTF(sink.Description)
	m.Provider = strPtrToTF(sink.Provider)
	m.Region = strPtrToTF(sink.Region)
	m.Protocol = strPtrToTF(sink.Protocol)
	m.Compression = strPtrToTF(sink.Compression)
	m.AuthType = strPtrToTF(sink.AuthType)
	m.Timeout = strPtrToTF(sink.Timeout)
	m.Enabled = boolPtrOrDefault(sink.Enabled)
	m.HasCredentials = boolPtrOrDefault(sink.HasCredentials)
	m.CreatedAt = timePtrToTF(sink.CreatedAt)
	m.CreatedBy = strPtrToTF(sink.CreatedBy)
	m.UpdatedAt = timePtrToTF(sink.UpdatedAt)
	m.UpdatedBy = strPtrToTF(sink.UpdatedBy)
	if sink.Headers != nil {
		elems := make(map[string]attr.Value, len(*sink.Headers))
		for k, v := range *sink.Headers {
			elems[k] = types.StringValue(v)
		}
		mv, _ := types.MapValue(types.StringType, elems)
		m.Headers = mv
	} else {
		m.Headers = types.MapNull(types.StringType)
	}
	if sink.Tags != nil {
		elems := make(map[string]attr.Value, len(*sink.Tags))
		for k, v := range *sink.Tags {
			elems[k] = types.StringValue(v)
		}
		mv, _ := types.MapValue(types.StringType, elems)
		m.Tags = mv
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
	return m
}
