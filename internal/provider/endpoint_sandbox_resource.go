package provider

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// Sandbox grammar, mirrored from shared-core EndpointSandboxGrammar (itself the
// mirror of the device's sandbox.rs). The device refuses a bundle carrying a
// value outside it, and a refused section pins every device of the tenant to
// its last-known-good bundle, so the cheap rules are checked at plan.
var (
	sandboxTools = []string{"claude-code", "codex", "copilot", "cursor", "antigravity"}

	// sandboxForbiddenPathBytes: bytes no path may carry.
	sandboxForbiddenPathBytes = "\"\\*?[]{}(),"
)

const (
	sandboxMaxPaths      = 64
	sandboxMaxDomains    = 256
	sandboxMaxPathLen    = 1024
	sandboxMaxDomainLen  = 253
	sandboxMaxDescLength = 500
)

// EndpointSandboxResource is the `ferentin_endpoint_sandbox` resource: the
// tenant's one agent sandbox policy (ferentin-endpoint#1382). Which AI tools on
// managed devices have Ferentin's sandbox written into their native settings,
// the folders their commands may write or must not touch, and the domains they
// may reach.
//
// One row per tenant, written by an upsert PUT that REPLACES the whole policy,
// with version + provenance. There is no DELETE endpoint: destroying the
// resource saves a policy that enables no tool, which is what the platform's
// "configured, nothing enabled" state means on devices (each tool's sandbox is
// left to whoever else manages it). Create refuses to overwrite a policy that
// already exists; import it instead.
type EndpointSandboxResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type EndpointSandboxResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	Tools                 types.Map               `tfsdk:"tools"`
	Filesystem            *EndpointSandboxFSModel `tfsdk:"filesystem"`
	AllowedDomains        types.Set               `tfsdk:"allowed_domains"`
	IncludeDerivedDomains types.Bool              `tfsdk:"include_derived_domains"`
	Description           types.String            `tfsdk:"description"`

	StoredAllowedDomains types.Set    `tfsdk:"stored_allowed_domains"`
	DefaultDeniedPaths   types.List   `tfsdk:"default_denied_paths"`
	PolicyID             types.String `tfsdk:"policy_id"`
	CreatedAt            types.String `tfsdk:"created_at"`
	UpdatedAt            types.String `tfsdk:"updated_at"`

	Version           types.Int64  `tfsdk:"version"`
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

// EndpointSandboxFSModel is the filesystem block. Its presence is meaningful:
// absent keeps the devices' credential template, present is taken as written.
type EndpointSandboxFSModel struct {
	Writable types.Set `tfsdk:"writable"`
	Denied   types.Set `tfsdk:"denied"`
}

// sandboxToolAttrTypes is the element type of the tools map.
var sandboxToolAttrTypes = map[string]attr.Type{
	"enabled":           types.BoolType,
	"allow_unsandboxed": types.BoolType,
	"reach":             types.StringType,
}

func NewEndpointSandboxResource() resource.Resource { return &EndpointSandboxResource{} }

var (
	_ resource.Resource                   = (*EndpointSandboxResource)(nil)
	_ resource.ResourceWithConfigure      = (*EndpointSandboxResource)(nil)
	_ resource.ResourceWithImportState    = (*EndpointSandboxResource)(nil)
	_ resource.ResourceWithValidateConfig = (*EndpointSandboxResource)(nil)
)

func (r *EndpointSandboxResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_endpoint_sandbox"
}

func (r *EndpointSandboxResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *EndpointSandboxResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The tenant's agent sandbox policy: which AI tools on managed devices have " +
			"Ferentin's sandbox written into their native sandbox settings, the folders their commands " +
			"may write or must not touch, and the domains they may reach. **One per tenant.**\n\n" +
			"Every value is checked with the device's own grammar; a value devices would refuse is " +
			"refused at plan where the provider can tell, and by the platform (a 400 naming the field) " +
			"otherwise. A change reaches devices on the tenant's next policy activation.\n\n" +
			"Claude Code, Codex and Copilot read a managed file the developer cannot switch off " +
			"(`reach = \"enforced\"`). Cursor and Antigravity read the sandbox only from the developer's " +
			"own settings, so there it is a default the developer can undo (`reach = \"default_only\"`).\n\n" +
			"~> **Destroy** saves a policy that enables no tool (the platform has no delete): Ferentin stops " +
			"writing the sandbox into every tool's settings, and the folders and domains are cleared.\n\n" +
			"## Import\n\n" +
			"Create refuses to overwrite a policy the tenant already saved. Import it instead:\n\n" +
			"```\n" +
			"terraform import ferentin_endpoint_sandbox.this <tenant_id>\n" +
			"```",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The tenant id: there is one sandbox policy per tenant.",
				Computed:            true, PlanModifiers: keep,
			},
			"tenant_id": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tools": schema.MapNestedAttribute{
				MarkdownDescription: "Per AI tool, keyed by the hook vendor's name: `claude-code`, `codex`, " +
					"`copilot`, `cursor`, `antigravity`. A tool absent from the map, or with `enabled = false`, " +
					"is left to whoever else manages its sandbox.",
				Optional: true,
				Validators: []validator.Map{
					mapvalidator.KeysAre(stringvalidator.OneOf(sandboxTools...)),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"enabled": schema.BoolAttribute{
							MarkdownDescription: "Whether Ferentin writes the sandbox into this tool's settings.",
							Required:            true,
						},
						"allow_unsandboxed": schema.BoolAttribute{
							MarkdownDescription: "The escape hatch. `false` (the default) is strict: the tool may " +
								"not retry a command outside the sandbox and the developer may not switch it off. " +
								"`true` keeps the sandbox on and lets the tool's own approval prompt run a command " +
								"outside it (Docker and similar). On Codex, `false` also restricts the approval " +
								"policy to never.",
							Optional: true, Computed: true,
							Default: booldefault.StaticBool(false),
						},
						"reach": schema.StringAttribute{
							MarkdownDescription: "Read-only. `enforced` (a managed file the developer cannot " +
								"switch off) or `default_only` (a default in the developer's own settings).",
							Computed:      true,
							PlanModifiers: keep,
						},
					},
				},
			},
			"filesystem": schema.SingleNestedAttribute{
				MarkdownDescription: "Folders sandboxed commands may write or must not touch. **Omit the " +
					"block** to keep the devices' credential template: they deny `~/.ssh`, `~/.aws`, " +
					"`~/.gnupg`, `~/.config/gh` and `~/Library/Keychains` (see `default_denied_paths`) and " +
					"allow nothing extra writable. Present, it is taken as written.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"writable": schema.SetAttribute{
						MarkdownDescription: "Folders commands may write besides the project and the temporary " +
							"directory, which are always writable. Absolute (`/opt/cache`) or home-relative " +
							"(`~/scratch`). At most 64. Refused: a home folder, a `Library` or `LaunchAgents` " +
							"folder, `/var/root`, an agent's or editor's settings folder, and a folder inside a " +
							"denied one.",
						Optional: true, ElementType: types.StringType,
						Validators: []validator.Set{setvalidator.SizeAtMost(sandboxMaxPaths)},
					},
					"denied": schema.SetAttribute{
						MarkdownDescription: "Folders commands may neither read nor write. Required in the block; " +
							"an empty set denies nothing (not even the credential template). At most 64.",
						Required: true, ElementType: types.StringType,
						Validators: []validator.Set{setvalidator.SizeAtMost(sandboxMaxPaths)},
					},
				},
			},
			"allowed_domains": schema.SetAttribute{
				MarkdownDescription: "Domains sandboxed commands may reach: a DNS name of two or more labels, " +
					"optionally `*.`-prefixed for its subdomains. No port, scheme, IP address or bare `*`. At " +
					"most 256. Empty or unset means no network, unless `include_derived_domains` adds some.\n\n" +
					"The platform trims, lowercases and strips trailing dots; Terraform keeps your spelling " +
					"here and the platform's in `stored_allowed_domains`.",
				Optional: true, ElementType: types.StringType,
				Validators: []validator.Set{setvalidator.SizeAtMost(sandboxMaxDomains)},
			},
			"include_derived_domains": schema.BoolAttribute{
				MarkdownDescription: "Also allow the hosts the tenant already allows elsewhere: unconditioned " +
					"allow destination rules, approved MCP servers and the Ferentin gateway. Derived hosts come " +
					"after yours and are cut first if the list would pass 256. Default `true`.",
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(true),
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form admin note. Never reaches the device. At most 500 characters.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(1, sandboxMaxDescLength)},
			},

			"stored_allowed_domains": schema.SetAttribute{
				MarkdownDescription: "Read-only. `allowed_domains` as the platform stores them, normalized.",
				Computed:            true, ElementType: types.StringType,
			},
			"default_denied_paths": schema.ListAttribute{
				MarkdownDescription: "Read-only. What devices deny while no `filesystem` block is set. Null " +
					"when one is.",
				Computed: true, ElementType: types.StringType,
			},
			"policy_id":  schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"created_at": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"updated_at": schema.StringAttribute{Computed: true},
			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version, sent as `If-Match` on update and destroy " +
					"so a concurrent console edit is rejected with 412 rather than overwritten.",
				Computed: true,
			},
			"managed_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the original creator (`iac` for this provider).",
				Computed:            true, PlanModifiers: keep,
			},
			"managed_by_client_id": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"managed_by_module":    schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent writer. Divergence from `managed_by` is " +
					"the drift signal.",
				Computed: true,
			},
		},
	}
}

// ValidateConfig checks the device grammar where it is cheap to mirror: the
// path and domain shapes, the bound on each list after normalization, and a
// writable folder inside a denied one. The "too wide to write" rule is left to
// the platform, which names the refused entry.
func (r *EndpointSandboxResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var domains types.Set
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("allowed_domains"), &domains)...)
	var fs types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("filesystem"), &fs)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if vals, ok := knownStrings(domains); ok {
		seen := map[string]bool{}
		for _, v := range vals {
			n := normalizeSandboxDomain(v)
			seen[n] = true
			if n == "" || !validSandboxDomain(n) {
				resp.Diagnostics.AddAttributeError(path.Root("allowed_domains"),
					fmt.Sprintf("%q is not a domain devices accept", v),
					"A domain is a DNS name of two or more labels, optionally *.-prefixed for its "+
						"subdomains. No port, scheme, IP address or bare *.")
			}
		}
		if len(seen) > sandboxMaxDomains {
			resp.Diagnostics.AddAttributeError(path.Root("allowed_domains"), "Too many domains",
				fmt.Sprintf("Devices accept at most %d domains after lowercasing and deduplication.", sandboxMaxDomains))
		}
	}

	if fs.IsNull() || fs.IsUnknown() {
		return
	}
	writable, wOK := knownStrings(objSet(fs, "writable"))
	denied, dOK := knownStrings(objSet(fs, "denied"))
	for _, l := range []struct {
		name string
		vals []string
		ok   bool
	}{{"writable", writable, wOK}, {"denied", denied, dOK}} {
		if !l.ok {
			continue
		}
		at := path.Root("filesystem").AtName(l.name)
		norm := map[string]bool{}
		for _, v := range l.vals {
			n := normalizeSandboxPath(v)
			norm[n] = true
			if !validSandboxPath(n) {
				resp.Diagnostics.AddAttributeError(at, fmt.Sprintf("%q is not a folder devices accept", v),
					"A folder is absolute (/…) or home-relative (~/…), printable ASCII with no quote, "+
						"backslash, parenthesis, comma or glob character, no '.', '..' or empty component, no "+
						"component starting or ending with a space, and at most 1024 characters. / alone is "+
						"refused.")
			}
		}
		if len(norm) > sandboxMaxPaths {
			resp.Diagnostics.AddAttributeError(at, "Too many folders",
				fmt.Sprintf("Devices accept at most %d folders per list.", sandboxMaxPaths))
		}
	}
	if wOK && dOK {
		for _, w := range writable {
			nw := normalizeSandboxPath(w)
			for _, d := range denied {
				if sandboxPathWithin(nw, normalizeSandboxPath(d)) {
					resp.Diagnostics.AddAttributeError(path.Root("filesystem").AtName("writable"),
						fmt.Sprintf("%q is, or is inside, a denied folder (%q)", w, d),
						"A folder cannot be both writable and denied: devices refuse the policy.")
					break
				}
			}
		}
	}
}

func (r *EndpointSandboxResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan EndpointSandboxResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	// The PUT is an upsert that replaces the whole policy. Overwriting one the
	// tenant already saved would silently discard a console author's choices.
	existing, err := r.sdk.EndpointSandbox().Get(ctx, tenantID)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to read the endpoint sandbox policy", err)
		return
	}
	if existing.Configured != nil && *existing.Configured {
		by := "unknown"
		if existing.LastModifiedBy != nil {
			by = string(*existing.LastModifiedBy)
		}
		resp.Diagnostics.AddError("The tenant already has an agent sandbox policy",
			fmt.Sprintf("Tenant %s saved one (last written by %q). Create would replace it whole. Adopt it "+
				"instead:\n\n  terraform import ferentin_endpoint_sandbox.<name> %s\n\nor use an `import` block.",
				tenantID, by, tenantID))
		return
	}

	body, diags := plan.toWrite(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// No If-Match on the first write: the platform answers an If-Match against a
	// tenant that never saved a policy with 412.
	got, err := r.sdk.EndpointSandbox().Put(ctx, tenantID, "", body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to save the endpoint sandbox policy", err)
		return
	}
	state := sandboxToModel(tenantID, got, plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *EndpointSandboxResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state EndpointSandboxResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	got, err := r.sdk.EndpointSandbox().Get(ctx, tenantID)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to read the endpoint sandbox policy", err)
		return
	}
	if got.Configured == nil || !*got.Configured {
		// Never saved (an import of a tenant with no policy), or gone.
		resp.State.RemoveResource(ctx)
		return
	}
	refreshed := sandboxToModel(tenantID, got, state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *EndpointSandboxResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state EndpointSandboxResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	body, diags := plan.toWrite(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	got, err := r.sdk.EndpointSandbox().Put(ctx, tenantID, version, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to save the endpoint sandbox policy", err)
		return
	}
	refreshed := sandboxToModel(tenantID, got, plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

// Delete saves a policy that enables no tool. There is no DELETE endpoint, and
// a policy with every tool absent is what the platform means by "Ferentin does
// not manage any tool's sandbox": devices stop writing it.
func (r *EndpointSandboxResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state EndpointSandboxResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	tools := map[string]gen.EndpointSandboxToolRequest{}
	derived := true
	body := adminapi.EndpointSandboxPolicyWrite{Tools: &tools, IncludeDerivedDomains: &derived}
	version := strconv.FormatInt(state.Version.ValueInt64(), 10)
	if _, err := r.sdk.EndpointSandbox().Put(ctx, tenantID, version, body); err != nil {
		addSDKError(&resp.Diagnostics, "Failed to clear the endpoint sandbox policy", err)
	}
}

func (r *EndpointSandboxResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	tenantID := req.ID
	if tenantID == "" {
		tenantID = r.tenantID
	}
	if tenantID == "" || strings.Contains(tenantID, "/") {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<tenant_id>`, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID)...)
}

func (r *EndpointSandboxResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// toWrite builds the PUT body, which replaces the whole policy: an unset
// attribute is sent as absent and clears what it held.
func (m *EndpointSandboxResourceModel) toWrite(ctx context.Context) (adminapi.EndpointSandboxPolicyWrite, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := adminapi.EndpointSandboxPolicyWrite{}

	tools := map[string]gen.EndpointSandboxToolRequest{}
	if !m.Tools.IsNull() && !m.Tools.IsUnknown() {
		for k, v := range m.Tools.Elements() {
			obj, ok := v.(types.Object)
			if !ok {
				continue
			}
			attrs := obj.Attributes()
			enabled, _ := attrs["enabled"].(types.Bool)
			allow, _ := attrs["allow_unsandboxed"].(types.Bool)
			t := gen.EndpointSandboxToolRequest{Enabled: enabled.ValueBool()}
			if !allow.IsNull() && !allow.IsUnknown() {
				b := allow.ValueBool()
				t.AllowUnsandboxed = &b
			}
			tools[k] = t
		}
	}
	body.Tools = &tools

	if m.Filesystem != nil {
		writable := sandboxSetStrings(ctx, m.Filesystem.Writable, &diags)
		denied := sandboxSetStrings(ctx, m.Filesystem.Denied, &diags)
		if denied == nil {
			denied = []string{}
		}
		fs := gen.EndpointSandboxFilesystemRequest{Denied: &denied}
		if writable != nil {
			fs.Writable = &writable
		}
		body.Filesystem = &fs
	}
	if d := sandboxSetStrings(ctx, m.AllowedDomains, &diags); d != nil {
		body.AllowedDomains = &d
	}
	setBoolPtr(m.IncludeDerivedDomains, &body.IncludeDerivedDomains)
	setStringPtr(m.Description, &body.Description)
	return body, diags
}

// sandboxToModel maps the response onto the model. prior is the plan (after a
// write) or the state (on refresh); it decides which tools appear and which
// spelling the configured sets keep.
//
//   - The response lists every tool the device knows, enabled or not. State
//     keeps the tools prior names, plus any the platform holds enabled or with
//     the escape hatch set (a console edit, shown as drift). An absent tool
//     and a disabled one mean the same on devices.
//   - Domains and folders come back normalized, sorted and deduplicated. When
//     the stored set is what prior's spelling normalizes to, prior's spelling
//     is kept; otherwise the stored set is taken, so a console edit shows.
func sandboxToModel(tenantID string, p *adminapi.EndpointSandboxPolicy, prior EndpointSandboxResourceModel, diags *diag.Diagnostics) EndpointSandboxResourceModel {
	m := EndpointSandboxResourceModel{
		ID:       types.StringValue(tenantID),
		TenantID: types.StringValue(tenantID),
	}

	priorTools := map[string]bool{}
	if !prior.Tools.IsNull() && !prior.Tools.IsUnknown() {
		for k := range prior.Tools.Elements() {
			priorTools[k] = true
		}
	}
	objType := types.ObjectType{AttrTypes: sandboxToolAttrTypes}
	toolVals := map[string]attr.Value{}
	if p.Tools != nil {
		for k, t := range *p.Tools {
			enabled := t.Enabled != nil && *t.Enabled
			allow := t.AllowUnsandboxed != nil && *t.AllowUnsandboxed
			if !priorTools[k] && !enabled && !allow {
				continue
			}
			obj, d := types.ObjectValue(sandboxToolAttrTypes, map[string]attr.Value{
				"enabled":           types.BoolValue(enabled),
				"allow_unsandboxed": types.BoolValue(allow),
				"reach":             enumPtrToTF(t.Reach),
			})
			diags.Append(d...)
			toolVals[k] = obj
		}
	}
	if len(toolVals) == 0 && (prior.Tools.IsNull() || prior.Tools.IsUnknown()) {
		m.Tools = types.MapNull(objType)
	} else {
		mv, d := types.MapValue(objType, toolVals)
		diags.Append(d...)
		m.Tools = mv
	}

	if p.Filesystem != nil {
		var priorW, priorD types.Set
		if prior.Filesystem != nil {
			priorW, priorD = prior.Filesystem.Writable, prior.Filesystem.Denied
		} else {
			priorW, priorD = types.SetNull(types.StringType), types.SetNull(types.StringType)
		}
		w := reconcileSandboxSet(priorW, ptrSliceToSet(p.Filesystem.Writable), normalizeSandboxPath)
		// writable is Optional: the platform answers [] for none.
		if len(w.Elements()) == 0 && (priorW.IsNull() || priorW.IsUnknown()) {
			w = types.SetNull(types.StringType)
		}
		d := reconcileSandboxSet(priorD, ptrSliceToSet(p.Filesystem.Denied), normalizeSandboxPath)
		if d.IsNull() {
			d = types.SetValueMust(types.StringType, []attr.Value{})
		}
		m.Filesystem = &EndpointSandboxFSModel{Writable: w, Denied: d}
	}

	m.StoredAllowedDomains = ptrSliceToSet(p.AllowedDomains)
	if m.StoredAllowedDomains.IsNull() {
		m.StoredAllowedDomains = types.SetValueMust(types.StringType, []attr.Value{})
	}
	m.AllowedDomains = reconcileSandboxSet(prior.AllowedDomains, m.StoredAllowedDomains, normalizeSandboxDomain)
	if len(m.AllowedDomains.Elements()) == 0 && (prior.AllowedDomains.IsNull() || prior.AllowedDomains.IsUnknown()) {
		m.AllowedDomains = types.SetNull(types.StringType)
	}

	m.IncludeDerivedDomains = types.BoolValue(p.IncludeDerivedDomains == nil || *p.IncludeDerivedDomains)
	m.Description = emptyStringAsNull(p.Description)
	m.DefaultDeniedPaths = stringSliceToList(p.DefaultDeniedPaths)
	if p.Id != nil {
		m.PolicyID = types.StringValue(p.Id.String())
	} else {
		m.PolicyID = types.StringNull()
	}
	m.CreatedAt = timePtrToTF(p.CreatedAt)
	m.UpdatedAt = timePtrToTF(p.UpdatedAt)
	m.Version = int64PtrToTF(p.Version)
	m.ManagedBy = enumPtrToTF(p.ManagedBy)
	m.ManagedByClientID = strPtrToTF(p.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(p.ManagedByModule)
	m.LastModifiedBy = strPtrToTF(p.LastModifiedBy)
	return m
}

// reconcileSandboxSet keeps prior's spelling when the stored set is exactly
// what it normalizes to.
func reconcileSandboxSet(prior, stored types.Set, normalize func(string) string) types.Set {
	priorVals, ok := knownStrings(prior)
	storedVals, sok := knownStrings(stored)
	if !ok || !sok || prior.IsNull() {
		return stored
	}
	want := map[string]bool{}
	for _, v := range priorVals {
		want[normalize(v)] = true
	}
	got := map[string]bool{}
	for _, v := range storedVals {
		got[v] = true
	}
	if len(want) != len(got) {
		return stored
	}
	for k := range want {
		if !got[k] {
			return stored
		}
	}
	return prior
}

func ptrSliceToSet(p *[]string) types.Set {
	if p == nil {
		return types.SetNull(types.StringType)
	}
	vals := make([]attr.Value, 0, len(*p))
	for _, s := range *p {
		vals = append(vals, types.StringValue(s))
	}
	return types.SetValueMust(types.StringType, vals)
}

// sandboxSetStrings returns the set's strings, or nil when it is null or unknown.
func sandboxSetStrings(ctx context.Context, s types.Set, diags *diag.Diagnostics) []string {
	if s.IsNull() || s.IsUnknown() {
		return nil
	}
	out := []string{}
	diags.Append(s.ElementsAs(ctx, &out, false)...)
	return out
}

// knownStrings returns a set's elements when the set and every element are
// known; ok is false otherwise. A null set is known and empty.
func knownStrings(s types.Set) ([]string, bool) {
	if s.IsUnknown() {
		return nil, false
	}
	out := []string{}
	for _, e := range s.Elements() {
		v, ok := e.(types.String)
		if !ok || v.IsUnknown() {
			return nil, false
		}
		if !v.IsNull() {
			out = append(out, v.ValueString())
		}
	}
	return out, true
}

func objSet(o types.Object, name string) types.Set {
	if v, ok := o.Attributes()[name].(types.Set); ok {
		return v
	}
	return types.SetNull(types.StringType)
}

// normalizeSandboxPath is EndpointSandboxGrammar.normalisePath: one trailing
// slash removed, unless that leaves "" or "~". Nothing is trimmed.
func normalizeSandboxPath(p string) string {
	if strings.HasSuffix(p, "/") {
		rest := p[:len(p)-1]
		if rest != "" && rest != "~" {
			return rest
		}
	}
	return p
}

// normalizeSandboxDomain is EndpointSandboxGrammar.normaliseDomain: trimmed,
// trailing dots removed, ASCII-lowercased.
func normalizeSandboxDomain(d string) string {
	v := strings.TrimSpace(d)
	v = strings.TrimRight(v, ".")
	return asciiLower(v)
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// validSandboxPath is EndpointSandboxGrammar.isValidPath, on a normalized path.
func validSandboxPath(p string) bool {
	if p == "" || len(p) > sandboxMaxPathLen {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c < 0x20 || c >= 0x7f || strings.IndexByte(sandboxForbiddenPathBytes, c) >= 0 {
			return false
		}
	}
	var rest string
	switch {
	case strings.HasPrefix(p, "~/"):
		rest = p[2:]
	case strings.HasPrefix(p, "/"):
		rest = p[1:]
		if rest == "" {
			return false
		}
	default:
		return false
	}
	for _, c := range strings.Split(rest, "/") {
		if c == "" || c == "." || c == ".." || strings.HasPrefix(c, " ") || strings.HasSuffix(c, " ") {
			return false
		}
	}
	return true
}

// sandboxPathWithin is EndpointSandboxGrammar.pathWithin: path is folder or
// inside it, component by component, ignoring ASCII case.
func sandboxPathWithin(p, folder string) bool {
	lp, lf := asciiLower(p), asciiLower(folder)
	if !strings.HasPrefix(lp, lf) {
		return false
	}
	rest := lp[len(lf):]
	return rest == "" || strings.HasPrefix(rest, "/")
}

// validSandboxDomain is EndpointSandboxGrammar.isValidDomain, on a normalized
// domain.
func validSandboxDomain(d string) bool {
	if d == "" || len(d) > sandboxMaxDomainLen {
		return false
	}
	name := strings.TrimPrefix(d, "*.")
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
		}
	}
	tld := labels[len(labels)-1]
	return slices.ContainsFunc([]byte(tld), func(c byte) bool { return c < '0' || c > '9' })
}
