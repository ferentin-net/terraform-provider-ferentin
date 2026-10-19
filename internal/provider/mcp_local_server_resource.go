package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// catalogSlugPattern is the platform's AddFromCatalogRequest slug pattern.
var catalogSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// MCPLocalServerResource is the `ferentin_mcp_local_server` resource: an
// approved (or blocked) local MCP server (ferentin-platform#2126, IaC-ready in
// #3188). It creates one from a command-line template or a catalog entry, and
// manages the verdict of any server, including one a device reported, after
// an import.
type MCPLocalServerResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type MCPLocalServerResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	ArgvTemplate types.List   `tfsdk:"argv_template"`
	DisplayName  types.String `tfsdk:"display_name"`
	CatalogSlug  types.String `tfsdk:"catalog_slug"`
	Status       types.String `tfsdk:"status"`
	Reason       types.String `tfsdk:"reason"`

	ServerID              types.String `tfsdk:"server_id"`
	Slug                  types.String `tfsdk:"slug"`
	Kind                  types.String `tfsdk:"kind"`
	DeclaredVia           types.String `tfsdk:"declared_via"`
	EffectiveStatus       types.String `tfsdk:"effective_status"`
	Packages              types.List   `tfsdk:"packages"`
	Steerable             types.Bool   `tfsdk:"steerable"`
	ArgvHash              types.String `tfsdk:"argv_hash"`
	Approvable            types.Bool   `tfsdk:"approvable"`
	ApprovalRefused       types.Bool   `tfsdk:"approval_refused"`
	ApprovalRefusedReason types.String `tfsdk:"approval_refused_reason"`

	Version           types.Int64  `tfsdk:"version"`
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

func NewMCPLocalServerResource() resource.Resource { return &MCPLocalServerResource{} }

var (
	_ resource.Resource                   = (*MCPLocalServerResource)(nil)
	_ resource.ResourceWithConfigure      = (*MCPLocalServerResource)(nil)
	_ resource.ResourceWithImportState    = (*MCPLocalServerResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*MCPLocalServerResource)(nil)
	_ resource.ResourceWithValidateConfig = (*MCPLocalServerResource)(nil)
)

func (r *MCPLocalServerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_local_server"
}

func (r *MCPLocalServerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *MCPLocalServerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepOrReplace := []planmodifier.String{
		stringplanmodifier.UseStateForUnknown(),
		stringplanmodifier.RequiresReplaceIfConfigured(),
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Local MCP server approval — a locally spawned (stdio) MCP server that managed " +
			"devices run governed, through `ferentin mcp local`, instead of refusing it under " +
			"`unapproved_mcp_action`.\n\n" +
			"Create one from **exactly one** of:\n\n" +
			"* `argv_template` (with `display_name`) — the command line the approval covers, where `*` " +
			"matches exactly one argument: `[\"uv\", \"run\", \"--directory\", \"*\", \"okta-mcp-server\"]` " +
			"approves that server from any directory. Devices consult a template only for a command line " +
			"that names no package.\n" +
			"* `catalog_slug` — a local MCP catalog entry, keyed on its package identities.\n\n" +
			"Either way the server is approved on creation, tenant-wide. Its tools still start unvetted; " +
			"`local_mcp_risk_policy` on `ferentin_endpoint_policy_settings` decides what developers get " +
			"from them.\n\n" +
			"Import manages the verdict of a server a device reported, too. Such a server cannot be " +
			"deleted: destroying it leaves it, and its verdict, in place, with a warning.\n\n" +
			"~> The template is shipped **verbatim** in the signed policy bundle to every managed device. " +
			"Put `*` where a credential or anything private would go.\n\n" +
			"## Import\n\n" +
			"```\n" +
			"terraform import ferentin_mcp_local_server.example <tenant_id>/<server_id>\n" +
			"```",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"tenant_id": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"argv_template": schema.ListAttribute{
				MarkdownDescription: "The command line the approval covers, as the client's config spells it " +
					"(`argv[0]` included); `*` matches exactly one argument and every other token is compared " +
					"literally. At least two tokens must be literal. Changing it updates the server in place. " +
					"Leaving it out keeps the server's current template.",
				Optional: true, Computed: true,
				ElementType: types.StringType,
				Validators:  []validator.List{listvalidator.SizeAtLeast(2)},
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
			"display_name": schema.StringAttribute{
				MarkdownDescription: "Name shown in the console. Required with `argv_template`; a catalog " +
					"server takes its entry's. Changing it replaces the server.",
				Optional: true, Computed: true,
				PlanModifiers: keepOrReplace,
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"catalog_slug": schema.StringAttribute{
				MarkdownDescription: "Slug of the local MCP catalog entry to add the server from, e.g. " +
					"`local-playwright`. Changing it replaces the server.",
				Optional: true, Computed: true,
				PlanModifiers: keepOrReplace,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 64),
					stringvalidator.RegexMatches(catalogSlugPattern, "must be lowercase letters, digits and hyphens"),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "The verdict: `approved` (devices run it, governed), `blocked`, or " +
					"`pending` (no decision: `unapproved_mcp_action` applies). A created server starts " +
					"`approved`. Leaving it out keeps the current verdict.",
				Optional: true, Computed: true,
				PlanModifiers: keep,
				Validators:    []validator.String{stringvalidator.OneOf("approved", "blocked", "pending")},
			},
			"reason": schema.StringAttribute{
				MarkdownDescription: "Note recorded in the audit log with each write. Not read back.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthAtMost(1000)},
			},

			"server_id": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"slug":      schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"kind": schema.StringAttribute{
				MarkdownDescription: "`local` (spawned on a developer's machine) or `remote`.",
				Computed:            true, PlanModifiers: keep,
			},
			"declared_via": schema.StringAttribute{
				MarkdownDescription: "`template`, `catalog`, or null for a server a device reported.",
				Computed:            true, PlanModifiers: keep,
			},
			"effective_status": schema.StringAttribute{
				MarkdownDescription: "How admission treats the server now: `status`, except that a pending " +
					"server covered by an approved one reads `approved`.",
				Computed: true,
			},
			"packages": schema.ListAttribute{
				MarkdownDescription: "Registry-qualified package identities the approval is keyed on, e.g. " +
					"`npm:@acme/server`.",
				Computed: true, ElementType: types.StringType,
			},
			"steerable": schema.BoolAttribute{
				MarkdownDescription: "Whether the approval reaches the fleet: the device bundle carries a match " +
					"key for it (a template, packages, or an argv hash).",
				Computed: true,
			},
			"argv_hash": schema.StringAttribute{Computed: true},
			"approvable": schema.BoolAttribute{
				MarkdownDescription: "False when the command line is too long for a device to keep it " +
					"released once approved.",
				Computed: true,
			},
			"approval_refused": schema.BoolAttribute{
				MarkdownDescription: "True when the platform refuses to approve the server, for the reason in " +
					"`approval_refused_reason`.",
				Computed: true,
			},
			"approval_refused_reason": schema.StringAttribute{Computed: true},

			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version, sent as `If-Match` on update and delete " +
					"so a concurrent console edit is rejected with 412 rather than overwritten.",
				Computed: true,
			},
			"managed_by": schema.StringAttribute{
				MarkdownDescription: "Who created the server: `iac` for this provider, `console`, or `system` " +
					"for one a device reported. Immutable.",
				Computed: true, PlanModifiers: keep,
			},
			"managed_by_client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth2 `client_id` of the creator.",
				Computed:            true, PlanModifiers: keep,
			},
			"managed_by_module": schema.StringAttribute{
				MarkdownDescription: "Module label the creator sent via `X-Ferentin-Managed-By-Module`.",
				Computed:            true, PlanModifiers: keep,
			},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent governance write. Divergence from " +
					"`managed_by` is the drift signal.",
				Computed: true,
			},
		},
	}
}

func (r *MCPLocalServerResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// ValidateConfig refuses the combinations no create path takes. Requiring one
// of argv_template / catalog_slug waits for ModifyPlan, because an imported
// server a device reported has neither.
func (r *MCPLocalServerResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var tmpl types.List
	var slug, name types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("argv_template"), &tmpl)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("catalog_slug"), &slug)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("display_name"), &name)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !tmpl.IsNull() && !slug.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("catalog_slug"),
			"argv_template and catalog_slug are mutually exclusive",
			"A server is declared from a command-line template or added from a catalog entry, not both.")
	}
	if !slug.IsNull() && !name.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("display_name"),
			"display_name is not used with catalog_slug",
			"A server added from the catalog takes its catalog entry's name.")
	}
}

// ModifyPlan requires a creation path when the server does not exist yet, and
// says out loud what a destroy does, which differs by who created the server.
func (r *MCPLocalServerResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		if req.Plan.Raw.IsNull() {
			return
		}
		var tmpl types.List
		var slug, name types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("argv_template"), &tmpl)...)
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("catalog_slug"), &slug)...)
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("display_name"), &name)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if tmpl.IsNull() && slug.IsNull() {
			resp.Diagnostics.AddError("A new local MCP server needs argv_template or catalog_slug",
				"Declare it from a command-line template (argv_template, with display_name) or add it from "+
					"the local MCP catalog (catalog_slug). To manage the verdict of a server a device already "+
					"reported, import it instead.")
		}
		if !tmpl.IsNull() && name.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("display_name"),
				"display_name is required with argv_template",
				"Name the server the console will show for it.")
		}
		return
	}

	if !req.Plan.Raw.IsNull() {
		return
	}
	var state MCPLocalServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.DisplayName.ValueString()
	if state.DeclaredVia.IsNull() {
		resp.Diagnostics.AddWarning(fmt.Sprintf("Local MCP server %q will not be deleted", name),
			"A device reported this server, so the platform keeps it as the record of what ran. Destroying "+
				"it stops Terraform managing it and leaves it, and its "+state.Status.ValueString()+
				" verdict, in place. To change the verdict, set status before destroying.")
		return
	}
	if state.Status.ValueString() == "approved" {
		resp.Diagnostics.AddWarning(fmt.Sprintf("Destroying the approval of local MCP server %q", name),
			"Devices drop the approval on their next policy fetch, and the server falls back to "+
				"unapproved_mcp_action: reported, quarantined or blocked.")
	}
}

func (r *MCPLocalServerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MCPLocalServerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)
	reason := stringPtrOrNil(plan.Reason)

	var got *adminapi.MCPLocalServer
	var err error
	if isUnsetString(plan.CatalogSlug) {
		got, err = r.sdk.MCPLocalServers().Declare(ctx, tenantID, adminapi.MCPLocalServerDeclare{
			ArgvTemplate: stringListToSDK(ctx, plan.ArgvTemplate),
			DisplayName:  plan.DisplayName.ValueString(),
			Reason:       reason,
		})
	} else {
		got, err = r.sdk.MCPLocalServers().AddFromCatalog(ctx, tenantID, adminapi.MCPLocalServerFromCatalog{
			ProviderSlug: plan.CatalogSlug.ValueString(),
			Reason:       reason,
		})
	}
	if err != nil {
		if errors.Is(err, adminapi.ErrConflict) {
			resp.Diagnostics.AddError("This local MCP server is already registered",
				"The tenant already has a server for this template or catalog entry. Import it instead "+
					"(data.ferentin_mcp_local_servers lists each server's import_id):\n\n"+
					"  terraform import <address> <tenant_id>/<server_id>\n\nDetails: "+err.Error())
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to create local MCP server", err)
		return
	}

	// A created server is approved. A configured blocked or pending verdict
	// is a second write, with the version the create returned.
	if want := plan.Status.ValueString(); !plan.Status.IsUnknown() && want != "" && want != derefEnum(got.Status) {
		got, err = r.setStatus(ctx, tenantID, got, want, reason)
		if err != nil {
			addSDKError(&resp.Diagnostics, "Created the local MCP server, but failed to set its status", err)
			// Record the server anyway, so the next apply retries the status
			// instead of declaring it again.
		}
	}
	state := localServerToModel(tenantID, got)
	state.Reason = plan.Reason
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *MCPLocalServerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MCPLocalServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	got, err := r.sdk.MCPLocalServers().Get(ctx, tenantID, state.ServerID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read local MCP server", err)
		return
	}
	refreshed := localServerToModel(tenantID, got)
	refreshed.Reason = state.Reason
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPLocalServerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state MCPLocalServerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	reason := stringPtrOrNil(plan.Reason)
	serverID := state.ServerID.ValueString()
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)

	var got *adminapi.MCPLocalServer
	if !plan.ArgvTemplate.IsUnknown() && !plan.ArgvTemplate.IsNull() && !plan.ArgvTemplate.Equal(state.ArgvTemplate) {
		tmpl := stringListToSDK(ctx, plan.ArgvTemplate)
		var err error
		got, err = r.sdk.MCPLocalServers().SetArgvTemplate(ctx, tenantID, serverID, version,
			adminapi.MCPLocalServerArgvTemplate{ArgvTemplate: &tmpl, Reason: reason})
		if err != nil {
			addSDKError(&resp.Diagnostics, "Failed to update local MCP server argv_template", err)
			return
		}
	}
	if want := plan.Status.ValueString(); !plan.Status.IsUnknown() && want != "" && want != state.Status.ValueString() {
		// After a template write, its returned version is the one to assert.
		v := state.Version.ValueInt64Pointer()
		if got != nil {
			v = got.Version
		}
		var err error
		got, err = r.setStatus(ctx, tenantID, &adminapi.MCPLocalServer{Id: &serverID, Version: v}, want, reason)
		if err != nil {
			addSDKError(&resp.Diagnostics, "Failed to update local MCP server status", err)
			return
		}
	}
	if got == nil {
		// Only `reason` changed; there is nothing to send.
		state.Reason = plan.Reason
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	refreshed := localServerToModel(tenantID, got)
	refreshed.Reason = plan.Reason
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPLocalServerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MCPLocalServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	err := r.sdk.MCPLocalServers().Delete(ctx, tenantID, state.ServerID.ValueString(), version)
	switch {
	case err == nil, errors.Is(err, adminapi.ErrNotFound):
	case errors.Is(err, adminapi.ErrLocalServerNotDeletable):
		resp.Diagnostics.AddWarning("Local MCP server left in place",
			"The platform keeps a server a device reported. Terraform no longer manages it; its verdict "+
				"is unchanged.")
	default:
		addSDKError(&resp.Diagnostics, "Failed to delete local MCP server", err)
	}
}

func (r *MCPLocalServerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	tenantID, serverID := r.tenantID, req.ID
	if t, s, ok := strings.Cut(req.ID, "/"); ok {
		tenantID, serverID = t, s
	}
	if tenantID == "" || serverID == "" {
		resp.Diagnostics.AddError("Cannot determine the server to import",
			"Pass `<tenant_id>/<server_id>`, or `<server_id>` with `tenant_id` configured on the provider block.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("server_id"), serverID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+serverID)...)
}

func (r *MCPLocalServerResource) setStatus(ctx context.Context, tenantID string, server *adminapi.MCPLocalServer,
	status string, reason *string) (*adminapi.MCPLocalServer, error) {
	version := ""
	if server.Version != nil {
		version = strconv.FormatInt(*server.Version, 10)
	}
	return r.sdk.MCPLocalServers().SetStatus(ctx, tenantID, derefStr(server.Id), version, adminapi.MCPLocalServerStatus{
		Status: gen.LocalServerStatusRequestStatus(status),
		Reason: reason,
	})
}

func localServerToModel(tenantID string, s *adminapi.MCPLocalServer) MCPLocalServerResourceModel {
	m := MCPLocalServerResourceModel{TenantID: types.StringValue(tenantID)}
	m.ServerID = strPtrToTF(s.Id)
	m.ID = types.StringValue(tenantID + "/" + m.ServerID.ValueString())
	m.Slug = strPtrToTF(s.Slug)
	m.DisplayName = strPtrToTF(s.DisplayName)
	m.Kind = enumPtrToTF(s.Kind)
	m.DeclaredVia = enumPtrToTF(s.DeclaredVia)
	m.Status = enumPtrToTF(s.Status)
	m.EffectiveStatus = enumPtrToTF(s.EffectiveStatus)
	m.ArgvTemplate = emptyListAsNull(stringSliceToList(s.ArgvTemplate))
	m.Packages = stringSliceToList(s.Packages)
	if m.Packages.IsNull() {
		m.Packages = types.ListValueMust(types.StringType, []attr.Value{})
	}
	m.CatalogSlug = types.StringNull()
	if m.DeclaredVia.ValueString() == "catalog" {
		m.CatalogSlug = strPtrToTF(s.ProviderSlug)
	}
	m.Steerable = types.BoolValue(s.Steerable != nil && *s.Steerable)
	m.ArgvHash = strPtrToTF(s.ArgvHash)
	m.Approvable = types.BoolValue(s.Approvable == nil || *s.Approvable)
	m.ApprovalRefused = types.BoolValue(s.ApprovalRefused != nil && *s.ApprovalRefused)
	m.ApprovalRefusedReason = enumPtrToTF(s.ApprovalRefusedReason)
	m.Version = int64PtrToTF(s.Version)
	m.ManagedBy = enumPtrToTF(s.ManagedBy)
	m.ManagedByClientID = strPtrToTF(s.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(s.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(s.LastModifiedBy)
	return m
}

func stringPtrOrNil(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

func derefEnum[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}
