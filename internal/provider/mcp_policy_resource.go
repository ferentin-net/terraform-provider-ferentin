package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// Device target kinds (ADR 0029 decision 5, McpPolicyTargets on the platform).
var (
	mcpPolicyTargetKinds = []string{"host", "url_prefix", "package", "argv_template", "argv_hash",
		"argv_contains", "class", "app_bundle_id"}
	// An allow may not name these: they do not identify a server, so they may
	// only deny (McpPolicyTargets.DENY_ONLY_KINDS).
	mcpPolicyDenyOnlyKinds = []string{"argv_contains", "class"}
	// class values: the three server classes the scope defaults ask about.
	mcpPolicyTargetClasses = []string{"local", "remote", "unapproved"}
)

const (
	// The device's per-list limit (EndpointPolicyWireLimits.MAX_VALUES_PER_LIST).
	mcpPolicyMaxTargetValues = 64
	mcpPolicyMaxTargets      = 64
)

// MCPPolicyResource is the `ferentin_mcp_policy` Terraform resource: the
// unified MCP policy (ferentin-platform#3256, ADR 0029), one policy model for
// remote MCP servers reached through the gateway and local servers started on
// managed devices.
//
// A policy selects servers by provider instance (`provider_instances`, read by
// the gateway and by devices for a local instance) and by device target
// (`targets`, read by devices only), decides at `applies_at` (a server
// starting, each tool call, or both), and either enforces or only records
// (`mode`). This replaces what `ferentin_endpoint_mcp_rule` did for MCP
// servers: a block is a deny with targets, an allow an allow with targets, and
// a steer a `ferentin_mcp_route`.
//
// The update is a partial update: the platform keeps every field the request
// omits. So every field the resource owns is restated on each write, with the
// empty form where the config has none (criteria `[]`, targets `[]`, unless
// with no groups, description ""), or removing it from config would never
// reach the platform.
type MCPPolicyResource struct {
	sdk      *adminapi.SDKClient
	tenantID string
}

type MCPPolicyResourceModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`

	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	Priority          types.Int64  `tfsdk:"priority"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	Mode              types.String `tfsdk:"mode"`
	AppliesAt         types.String `tfsdk:"applies_at"`
	ProviderInstances types.List   `tfsdk:"provider_instances"`
	ValidateArguments types.Bool   `tfsdk:"validate_arguments"`

	Targets       []MCPPolicyTargetModel `tfsdk:"targets"`
	StoredTargets types.List             `tfsdk:"stored_targets"`

	Effect             *MCPPolicyEffectModel `tfsdk:"effect"`
	CriteriaCombinator types.String          `tfsdk:"criteria_combinator"`
	Criteria           []PolicyCriteriaModel `tfsdk:"criteria"`
	Unless             *MCPPolicyUnlessModel `tfsdk:"unless"`

	SettingsInForce types.Bool   `tfsdk:"settings_in_force"`
	SiblingOf       types.String `tfsdk:"sibling_of"`

	PolicyID  types.String `tfsdk:"policy_id"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`

	Version           types.Int64  `tfsdk:"version"` // for If-Match
	ManagedBy         types.String `tfsdk:"managed_by"`
	ManagedByClientID types.String `tfsdk:"managed_by_client_id"`
	ManagedByModule   types.String `tfsdk:"managed_by_module"`
	LastModifiedBy    types.String `tfsdk:"last_modified_by"`
}

// MCPPolicyEffectModel mirrors gen.McpEffectDto.
type MCPPolicyEffectModel struct {
	Type               types.String `tfsdk:"type"` // allow | deny
	Message            types.String `tfsdk:"message"`
	AllowedTools       types.List   `tfsdk:"allowed_tools"`
	DeniedTools        types.List   `tfsdk:"denied_tools"`
	GrantToolsets      types.List   `tfsdk:"grant_toolsets"`
	DenyToolsets       types.List   `tfsdk:"deny_toolsets"`
	RateLimitPerMinute types.Int64  `tfsdk:"rate_limit_per_minute"`
}

// MCPPolicyTargetModel is one device target: a kind and the values it selects.
type MCPPolicyTargetModel struct {
	Kind   types.String `tfsdk:"kind"`
	Values types.List   `tfsdk:"values"`
}

// MCPPolicyUnlessModel is a deny's exception (ADR 0029 decision 4).
type MCPPolicyUnlessModel struct {
	Combinator types.String          `tfsdk:"combinator"`
	Groups     []PolicyCriteriaModel `tfsdk:"groups"`
}

var mcpPolicyTargetObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"kind":   types.StringType,
	"values": types.ListType{ElemType: types.StringType},
}}

func NewMCPPolicyResource() resource.Resource { return &MCPPolicyResource{} }

var (
	_ resource.Resource                   = (*MCPPolicyResource)(nil)
	_ resource.ResourceWithConfigure      = (*MCPPolicyResource)(nil)
	_ resource.ResourceWithImportState    = (*MCPPolicyResource)(nil)
	_ resource.ResourceWithValidateConfig = (*MCPPolicyResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*MCPPolicyResource)(nil)
)

func (r *MCPPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_policy"
}

func (r *MCPPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *MCPPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	unlessGroups := criteriaSchemaAttribute(criteriaSchemaOptions{
		Description: "Condition groups of the exception, in the same shape as `criteria`. At least one.",
		TypeDescription: "Criteria type. Allowed: `claims`, `context`, `request`, `time`. Server default " +
			"applies if unset.",
		FieldDescription: "Field path to evaluate (e.g. `groups`, `department`, `subject.agents`).",
		ValueExample:     "`jsonencode(\"Backend\")`",
	})
	unlessGroups.Optional = false
	unlessGroups.Required = true
	unlessGroups.Validators = []validator.List{listvalidator.SizeAtLeast(1)}

	resp.Schema = schema.Schema{
		MarkdownDescription: "MCP policy: one policy model for remote MCP servers reached through the " +
			"gateway and local MCP servers started on managed devices.\n\n" +
			"A policy selects servers two ways, and may use both:\n\n" +
			"* **`provider_instances`**: MCP servers (`ferentin_mcp_server.*.server_id`). The gateway " +
			"decides remote calls from these; devices decide a local (`stdio_local`) instance's calls.\n" +
			"* **`targets`**: what the policy selects on a device (a host, a package, a command line, an " +
			"app). Only devices read them.\n\n" +
			"`applies_at` picks where it decides (a server starting, each tool call, or both), and " +
			"`mode = \"report_only\"` records what it would have decided without deciding anything.\n\n" +
			"This is where an endpoint MCP rule's server rules went: a `block` is a `deny` with `targets`, " +
			"an `allow` an `allow` with `targets`, and a `steer` is a `ferentin_mcp_route`. Call-time tool " +
			"rules (`tool_name`, `command_binary`, `tool_behavior`) stay on `ferentin_endpoint_mcp_rule`.\n\n" +
			"~> **Permissions.** Writing `targets`, or changing any policy that names them, needs " +
			"`policy:rw`: a target is a device deny or release. `mcp:policies:rw` writes everything else.\n\n" +
			"## Import\n\n" +
			"```\n" +
			"terraform import ferentin_mcp_policy.example <tenant_id>/<policy_id>\n" +
			"```\n\n" +
			"An import has no configured spelling of `targets`, so it takes the platform's canonical " +
			"form; the first plan after import may show `targets` changing to your config's spelling, " +
			"which changes nothing on the platform.",

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
			"name": schema.StringAttribute{Required: true},
			// Optional, not Computed: the update keeps an omitted description, so
			// the provider sends "" to clear it, and removing it from config must
			// plan that change.
			"description": schema.StringAttribute{Optional: true},
			"priority": schema.Int64Attribute{
				MarkdownDescription: "Priority for conflict resolution when effects merge: 1 is highest, " +
					"1000 lowest. Default `100`.",
				Optional: true, Computed: true,
				Default:    int64default.StaticInt64(100),
				Validators: []validator.Int64{int64validator.Between(1, 1000)},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the policy is evaluated. Default `true`.",
				Optional:            true, Computed: true,
				Default: booldefault.StaticBool(true),
			},
			"mode": schema.StringAttribute{
				MarkdownDescription: "`enforce` (default) decides access. `report_only` evaluates the policy " +
					"and records what it would have decided on the audit trail, and decides nothing: it " +
					"never grants, denies or narrows. A policy created `report_only` also keeps its " +
					"logging and re-auth settings out of force (`settings_in_force`); switching an " +
					"enforcing policy to `report_only` keeps them.",
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("enforce"),
				Validators: []validator.String{stringvalidator.OneOf("enforce", "report_only")},
			},
			"applies_at": schema.StringAttribute{
				MarkdownDescription: "Where the policy decides: `admit` (a server starting on a device), " +
					"`call` (each tool call) or `both`. When unset the provider plans what the platform " +
					"would choose: `admit` for an `allow` with `targets` and no `provider_instances`, " +
					"`both` otherwise.\n\n" +
					"`admit` and `call` are accepted only when every instance in `provider_instances` is " +
					"local (`stdio_local`): the gateway reads every policy as `both`. A policy at `admit` " +
					"cannot list tools in its `effect`.",
				Optional: true, Computed: true,
				Validators: []validator.String{stringvalidator.OneOf("admit", "call", "both")},
			},
			"validate_arguments": schema.BoolAttribute{
				MarkdownDescription: "When true, validate tool-call arguments against the tool's input " +
					"schema before forwarding. Default `true`.",
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(true),
			},
			"provider_instances": schema.ListAttribute{
				MarkdownDescription: "MCP server UUIDs (`ferentin_mcp_server.*.server_id`) this policy " +
					"applies to. May be empty when `targets` names something; a policy naming neither is " +
					"refused. The platform stores UUIDs, so a non-UUID entry is rejected at plan time.",
				Optional:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					listvalidator.ValueStringsAre(policyInstanceRefValidator{
						attrHint: "`ferentin_mcp_server.<name>.server_id`",
					}),
				},
			},

			"targets": schema.ListNestedAttribute{
				MarkdownDescription: "Device targets: what the policy selects on a managed device, beside its " +
					"`provider_instances`. The targets are a union: the policy applies to whatever any of " +
					"them selects, and `app_bundle_id` narrows all the others to those MCP clients. At " +
					"most 64.\n\n" +
					"* `host`, `url_prefix`: a remote server's address\n" +
					"* `package`, `argv_template`, `argv_hash`: a local server's identity\n" +
					"* `argv_contains`: a token on a local server's command line (**deny only**)\n" +
					"* `class`: `local`, `remote` or `unapproved` (**deny only**)\n" +
					"* `app_bundle_id`: the MCP client, by macOS bundle id; narrows the other targets " +
					"and cannot stand alone\n\n" +
					"A policy with targets cannot be a step-up and cannot list tools or toolsets in its " +
					"`effect`: only an instance narrows its tools. `host` and `url_prefix` targets also " +
					"reach the device network filter as destination rules.\n\n" +
					"The platform stores values in canonical spelling (hosts lowercased, URLs " +
					"canonicalized). Terraform keeps your spelling here and the platform's in " +
					"`stored_targets`, so normalization does not show as drift.",
				Optional: true,
				Validators: []validator.List{
					listvalidator.SizeAtMost(mcpPolicyMaxTargets),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"kind": schema.StringAttribute{
							MarkdownDescription: "One of " + strings.Join(mcpPolicyTargetKinds, ", ") + ".",
							Required:            true,
							Validators:          []validator.String{stringvalidator.OneOf(mcpPolicyTargetKinds...)},
						},
						"values": schema.ListAttribute{
							MarkdownDescription: "1-64 values. For `argv_template`, ONE command line's " +
								"tokens with `*` matching one token (several templates are several " +
								"targets). For `argv_hash`, lowercase hex digests.",
							Required:    true,
							ElementType: types.StringType,
							Validators: []validator.List{
								listvalidator.SizeBetween(1, mcpPolicyMaxTargetValues),
								listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
							},
						},
					},
				},
			},
			"stored_targets": schema.ListNestedAttribute{
				MarkdownDescription: "Read-only. `targets` as the platform stores them, in canonical " +
					"spelling. A console edit shows up as a change here and in `targets` on the next plan.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"kind":   schema.StringAttribute{Computed: true},
						"values": schema.ListAttribute{Computed: true, ElementType: types.StringType},
					},
				},
			},

			"effect": schema.SingleNestedAttribute{
				MarkdownDescription: "Required effect: allow / deny, tool and toolset constraints, and an " +
					"optional rate limit.",
				Required: true,
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						MarkdownDescription: "Effect type: `allow` or `deny`.",
						Required:            true,
						Validators:          []validator.String{stringvalidator.OneOf("allow", "deny")},
					},
					"message": schema.StringAttribute{
						MarkdownDescription: "Message shown to the person or agent on every surface the " +
							"policy refuses on.",
						Optional: true,
					},
					"allowed_tools":  schema.ListAttribute{Optional: true, ElementType: types.StringType},
					"denied_tools":   schema.ListAttribute{Optional: true, ElementType: types.StringType},
					"grant_toolsets": schema.ListAttribute{Optional: true, ElementType: types.StringType},
					"deny_toolsets":  schema.ListAttribute{Optional: true, ElementType: types.StringType},
					"rate_limit_per_minute": schema.Int64Attribute{
						MarkdownDescription: "Per-minute rate limit for matching traffic.",
						Optional:            true,
					},
				},
			},

			"criteria_combinator": schema.StringAttribute{
				MarkdownDescription: "How the `criteria` groups combine: `AND` or `OR`. Default `OR`, the " +
					"platform's.",
				// Always sent: the platform replaces the combinator together with the
				// criteria on every update, defaulting it to OR when omitted.
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("OR"),
				Validators: []validator.String{stringvalidator.OneOf("AND", "OR")},
			},
			"criteria": criteriaSchemaAttribute(criteriaSchemaOptions{
				Description: "ABAC criteria for matching MCP requests. Groups combine via " +
					"`criteria_combinator`. Common shapes: JWT claims (`client_id`, `groups`, `email`), " +
					"`subject.agents` (`in` / `not_in` over AI platform catalog slugs) and " +
					"`device.group_id`.\n\n" +
					"~> An AI agent or `device.group_id` condition is decided only on devices, so it is " +
					"accepted only on a policy whose `provider_instances` are all local. The gateway's " +
					"token carries neither, and an absent value satisfies no condition there.\n\n" +
					"~> **Removing criteria widens the policy**, and the provider warns at plan when an " +
					"apply would.",
				TypeDescription:  "Criteria type. Allowed: `claims`, `context`, `request`, `time`. Server default applies if unset.",
				FieldDescription: "Field path to evaluate (e.g. `client_id`, `groups`, `subject.agents`, `device.group_id`).",
				ValueExample:     "`jsonencode(\"service\")`",
			}),
			"unless": schema.SingleNestedAttribute{
				MarkdownDescription: "An exception to a `deny`: the deny does not apply to a caller who " +
					"matches it. Decided at token issuance, so it can use identity attributes only (no " +
					"`resource.*`, time of day or day of week), and an AI agent condition is refused here " +
					"until devices carry verified agent evidence. Removing the block clears the exception.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"combinator": schema.StringAttribute{
						MarkdownDescription: "How the groups combine: `AND` (default) or `OR`.",
						Optional:            true, Computed: true,
						Default:    stringdefault.StaticString("AND"),
						Validators: []validator.String{stringvalidator.OneOf("AND", "OR")},
					},
					"groups": unlessGroups,
				},
			},

			"settings_in_force": schema.BoolAttribute{
				MarkdownDescription: "Read-only. Whether the policy's settings outside access (logging, " +
					"re-auth, protocol version targeting) apply while it is `report_only`. An enforcing " +
					"policy's settings always apply.",
				Computed: true,
			},
			"sibling_of": schema.StringAttribute{
				MarkdownDescription: "Read-only. Set on a `<name> · local` rehearsal policy the platform split " +
					"from the policy it names when local enforcement arrived. Null otherwise.",
				Computed: true,
			},

			"version": schema.Int64Attribute{
				MarkdownDescription: "Optimistic-concurrency version, sent as `If-Match` on update and " +
					"delete so a concurrent console edit is rejected with 412 rather than overwritten.",
				Computed: true,
			},
			"managed_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the original creator (`iac` for this provider). " +
					"Immutable after create.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"managed_by_client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth2 `client_id` of the creator, from the authenticated principal.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"managed_by_module": schema.StringAttribute{
				MarkdownDescription: "Module label this provider sent via `X-Ferentin-Managed-By-Module`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"last_modified_by": schema.StringAttribute{
				MarkdownDescription: "Provenance of the most recent writer. Divergence from `managed_by` " +
					"is the drift signal.",
				Computed: true,
			},

			"policy_id":  schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"created_at": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"updated_at": schema.StringAttribute{Computed: true},
		},
	}
}

// ValidateConfig moves the platform's cheap write checks to plan time, kept in
// lockstep with McpPolicyWriteCheck (targetsRefusal, appliesAtRefusal and
// targetValueRefusal's fixed-vocabulary cases). Canonical spelling, the
// local-only rules and catalog lookups stay server-side. Unknown values are
// skipped.
func (r *MCPPolicyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var targets, instances types.List
	var effect types.Object
	var appliesAt types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("targets"), &targets)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("provider_instances"), &instances)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("effect"), &effect)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("applies_at"), &appliesAt)...)
	if resp.Diagnostics.HasError() {
		return
	}
	effectType, effectKnown := "", false
	listsTools, toolsKnown := false, true
	if !effect.IsNull() && !effect.IsUnknown() {
		attrs := effect.Attributes()
		if t, ok := attrs["type"].(types.String); ok && !t.IsUnknown() && !t.IsNull() {
			effectType, effectKnown = t.ValueString(), true
		}
		for _, k := range []string{"allowed_tools", "denied_tools", "grant_toolsets", "deny_toolsets"} {
			l, ok := attrs[k].(types.List)
			if !ok {
				continue
			}
			if l.IsUnknown() {
				toolsKnown = false
				continue
			}
			if len(l.Elements()) > 0 {
				listsTools = true
			}
		}
	}

	if listsTools && !appliesAt.IsUnknown() && appliesAt.ValueString() == "admit" {
		resp.Diagnostics.AddAttributeError(path.Root("applies_at"),
			"A policy that applies only where a server starts cannot list tools",
			"applies_at \"admit\" decides when a server starts, before any tool is called. Make it apply at "+
				"\"call\" or \"both\", or remove the tool and toolset lists from effect.")
	}

	if targets.IsUnknown() {
		return
	}
	if len(targets.Elements()) == 0 {
		if !instances.IsUnknown() && len(instances.Elements()) == 0 {
			resp.Diagnostics.AddAttributeError(path.Root("provider_instances"),
				"An MCP policy must name provider_instances, targets, or both",
				"A policy naming neither selects no server anywhere and is refused by the platform.")
		}
		return
	}
	if listsTools && toolsKnown {
		resp.Diagnostics.AddAttributeError(path.Root("targets"),
			"A policy that names device targets cannot list tools or toolsets",
			"Only a server's instance narrows its tools on a call, so a release on a host or package would "+
				"admit every tool the policy withholds. Write a separate policy for the tools.")
	}
	identifies := !instances.IsUnknown() && len(instances.Elements()) > 0
	allKnown := !instances.IsUnknown()
	for i, el := range targets.Elements() {
		obj, ok := el.(types.Object)
		if !ok || obj.IsUnknown() {
			allKnown = false
			continue
		}
		kindV, _ := obj.Attributes()["kind"].(types.String)
		valuesV, _ := obj.Attributes()["values"].(types.List)
		if kindV.IsUnknown() || kindV.IsNull() {
			allKnown = false
			continue
		}
		kind := kindV.ValueString()
		at := path.Root("targets").AtListIndex(i)
		if kind != "app_bundle_id" {
			identifies = true
		}
		if effectKnown && effectType == "allow" && slices.Contains(mcpPolicyDenyOnlyKinds, kind) {
			resp.Diagnostics.AddAttributeError(at.AtName("kind"),
				fmt.Sprintf("An allow cannot name a %q target", kind),
				"argv_contains and class do not identify a server, so they may only deny.")
		}
		if valuesV.IsUnknown() || valuesV.IsNull() {
			continue
		}
		for _, v := range valuesV.Elements() {
			s, ok := v.(types.String)
			if !ok || s.IsUnknown() || s.IsNull() {
				continue
			}
			val := s.ValueString()
			switch kind {
			case "class":
				if !slices.Contains(mcpPolicyTargetClasses, val) {
					resp.Diagnostics.AddAttributeError(at.AtName("values"),
						fmt.Sprintf("%q is not a server class", val),
						"A class value is local, remote or unapproved.")
				}
			case "host":
				if strings.ContainsAny(val, "/?#@:") {
					resp.Diagnostics.AddAttributeError(at.AtName("values"),
						fmt.Sprintf("%q is not a bare hostname", val),
						"A host value has no scheme, port, path, query or userinfo. Use kind \"url_prefix\" "+
							"to match a URL.")
				}
			case "app_bundle_id":
				if strings.ContainsAny(val, " \t\r\n") {
					resp.Diagnostics.AddAttributeError(at.AtName("values"),
						fmt.Sprintf("%q is not one bundle id", val),
						"An app_bundle_id value is one bundle id, with no whitespace.")
				}
			}
		}
	}
	if allKnown && !identifies {
		resp.Diagnostics.AddAttributeError(path.Root("targets"),
			"app_bundle_id targets need a server to narrow",
			"App bundle ids narrow another target. Name a server target or a provider instance too.")
	}
}

// ModifyPlan plans applies_at the way the platform defaults it, so leaving it
// unset converges (the provider always sends it), and warns when an apply
// widens the policy.
func (r *MCPPolicyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var configured types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("applies_at"), &configured)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if configured.IsNull() {
		var effectType types.String
		var targets, instances types.List
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("effect").AtName("type"), &effectType)...)
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("targets"), &targets)...)
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("provider_instances"), &instances)...)
		if resp.Diagnostics.HasError() {
			return
		}
		planned := types.StringUnknown()
		if !effectType.IsUnknown() && !targets.IsUnknown() && !instances.IsUnknown() {
			planned = types.StringValue(defaultAppliesAt(effectType.ValueString(),
				len(targets.Elements()) > 0, len(instances.Elements()) > 0))
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("applies_at"), planned)...)
	}
	warnOnCriteriaRemoval(ctx, req, resp,
		"\n\nIf you meant to keep them, add the `criteria` block back to the config.")
	// applies_at planned above can be the only change (the console moved it and
	// the config leaves it unset).
	markComputedUnknownOnChange(ctx, req, resp, map[string]attr.Value{
		"version":           types.Int64Unknown(),
		"updated_at":        types.StringUnknown(),
		"last_modified_by":  types.StringUnknown(),
		"settings_in_force": types.BoolUnknown(),
	})
}

// defaultAppliesAt mirrors McpPolicyService's create default: an allow with
// targets and no instance applies where a server starts (it lifts the server
// default there, ADR 0029 decision 10), everything else at both points.
func defaultAppliesAt(effect string, hasTargets, hasInstances bool) string {
	if effect == "allow" && hasTargets && !hasInstances {
		return "admit"
	}
	return "both"
}

func (r *MCPPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MCPPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(plan.TenantID)

	w, diags := plan.toWrite(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := adminapi.MCPPolicyCreate{
		Name:               plan.Name.ValueString(),
		Priority:           int32(plan.Priority.ValueInt64()),
		Enabled:            w.enabled,
		ValidateArguments:  w.validateArguments,
		Effect:             w.effect,
		ProviderInstances:  w.instances,
		Targets:            w.targets,
		Mode:               (*gen.McpPolicyCreateRequestMode)(w.mode),
		AppliesAt:          (*gen.McpPolicyCreateRequestAppliesAt)(w.appliesAt),
		CriteriaCombinator: (*gen.McpPolicyCreateRequestCriteriaCombinator)(w.criteriaCombinator),
	}
	// Nothing to clear on a create: leave the empty forms off the body.
	if *w.description != "" {
		body.Description = w.description
	}
	if len(*w.criteria) > 0 {
		body.Criteria = w.criteria
	}
	if plan.Unless != nil {
		body.Unless = w.unless
	}

	pol, err := r.sdk.MCPPolicies().Create(ctx, tenantID, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to create MCP policy", err)
		return
	}
	state := mcpPolicyToModel(tenantID, pol)
	state.keepConfigured(plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *MCPPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MCPPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	pol, err := r.sdk.MCPPolicies().Get(ctx, tenantID, state.PolicyID.ValueString())
	if err != nil {
		if errors.Is(err, adminapi.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		addSDKError(&resp.Diagnostics, "Failed to read MCP policy", err)
		return
	}
	refreshed := mcpPolicyToModel(tenantID, pol)
	refreshed.Targets = reconcileTargets(state, refreshed)
	refreshed.keepEmptyForms(state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state MCPPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)

	w, diags := plan.toWrite(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()
	priority := int32(plan.Priority.ValueInt64())
	// Every owned field is restated, with its empty form where the config has
	// none: the update keeps whatever the request omits.
	body := adminapi.MCPPolicyUpdate{
		Name:               &name,
		Priority:           &priority,
		Description:        w.description,
		Enabled:            w.enabled,
		ValidateArguments:  w.validateArguments,
		Effect:             &w.effect,
		ProviderInstances:  w.instances,
		Targets:            w.targets,
		Criteria:           w.criteria,
		Unless:             w.unless,
		Mode:               (*gen.McpPolicyUpdateRequestMode)(w.mode),
		AppliesAt:          (*gen.McpPolicyUpdateRequestAppliesAt)(w.appliesAt),
		CriteriaCombinator: (*gen.McpPolicyUpdateRequestCriteriaCombinator)(w.criteriaCombinator),
	}

	v := strconv.FormatInt(state.Version.ValueInt64(), 10)
	pol, err := r.sdk.MCPPolicies().Update(ctx, tenantID, state.PolicyID.ValueString(), v, body)
	if err != nil {
		addSDKError(&resp.Diagnostics, "Failed to update MCP policy", err)
		return
	}
	refreshed := mcpPolicyToModel(tenantID, pol)
	refreshed.keepConfigured(plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &refreshed)...)
}

func (r *MCPPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MCPPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.resolveTenant(state.TenantID)
	v := strconv.FormatInt(state.Version.ValueInt64(), 10)
	err := r.sdk.MCPPolicies().Delete(ctx, tenantID, state.PolicyID.ValueString(), v)
	if err != nil && !errors.Is(err, adminapi.ErrNotFound) {
		addSDKError(&resp.Diagnostics, "Failed to delete MCP policy", err)
	}
}

func (r *MCPPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	tenantID, policyID, diags := parseTenantScopedImportID(req.ID, r.tenantID, "policy")
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_id"), policyID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+policyID)...)
}

func (r *MCPPolicyResource) resolveTenant(perResource types.String) string {
	if !perResource.IsNull() && !perResource.IsUnknown() && perResource.ValueString() != "" {
		return perResource.ValueString()
	}
	return r.tenantID
}

// mcpPolicyWrite holds the request fields Create and Update share, each in the
// form an update needs (empty forms rather than nil). Create drops the empty
// forms it has no use for.
type mcpPolicyWrite struct {
	description        *string
	enabled            *bool
	validateArguments  *bool
	effect             gen.McpEffectDto
	instances          *[]string
	targets            *[]gen.McpPolicyTarget
	criteria           *[]gen.PolicyCriteria
	unless             *gen.PolicyUnless
	mode               *string
	appliesAt          *string
	criteriaCombinator *string
}

func (m *MCPPolicyResourceModel) toWrite(ctx context.Context) (mcpPolicyWrite, diag.Diagnostics) {
	var diags diag.Diagnostics
	w := mcpPolicyWrite{effect: m.toEffect(ctx)}

	desc := ""
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		desc = m.Description.ValueString()
	}
	w.description = &desc
	setBoolPtr(m.Enabled, &w.enabled)
	setBoolPtr(m.ValidateArguments, &w.validateArguments)
	setStringPtr(m.Mode, &w.mode)
	setStringPtr(m.AppliesAt, &w.appliesAt)
	setStringPtr(m.CriteriaCombinator, &w.criteriaCombinator)

	instances := []string{}
	if !m.ProviderInstances.IsNull() && !m.ProviderInstances.IsUnknown() {
		instances = stringListToSDK(ctx, m.ProviderInstances)
	}
	w.instances = &instances

	targets := make([]gen.McpPolicyTarget, 0, len(m.Targets))
	for _, t := range m.Targets {
		kind := gen.McpPolicyTargetKind(t.Kind.ValueString())
		values := stringListToSDK(ctx, t.Values)
		targets = append(targets, gen.McpPolicyTarget{Kind: &kind, Values: &values})
	}
	w.targets = &targets

	crits, d := criteriaListToSDK(m.Criteria)
	diags.Append(d...)
	if crits == nil {
		crits = []gen.PolicyCriteria{}
	}
	w.criteria = &crits

	// An exception with no groups clears it (ADR 0029 decision 4).
	groups := []gen.PolicyCriteria{}
	unless := &gen.PolicyUnless{}
	if m.Unless != nil {
		g, d := criteriaListToSDK(m.Unless.Groups)
		diags.Append(d...)
		if g != nil {
			groups = g
		}
		if c := m.Unless.Combinator; !c.IsNull() && !c.IsUnknown() {
			comb := gen.PolicyUnlessCombinator(c.ValueString())
			unless.Combinator = &comb
		}
	}
	unless.Groups = &groups
	w.unless = unless
	return w, diags
}

func (m *MCPPolicyResourceModel) toEffect(ctx context.Context) gen.McpEffectDto {
	if m.Effect == nil {
		return gen.McpEffectDto{Effect: gen.McpEffectDtoEffect("allow")}
	}
	e := gen.McpEffectDto{
		Effect: gen.McpEffectDtoEffect(m.Effect.Type.ValueString()),
	}
	if !m.Effect.Message.IsNull() && !m.Effect.Message.IsUnknown() {
		v := m.Effect.Message.ValueString()
		e.Message = &v
	}
	if !m.Effect.RateLimitPerMinute.IsNull() && !m.Effect.RateLimitPerMinute.IsUnknown() {
		v := int32(m.Effect.RateLimitPerMinute.ValueInt64())
		e.RateLimitPerMinute = &v
	}
	if !m.Effect.AllowedTools.IsNull() && !m.Effect.AllowedTools.IsUnknown() {
		s := stringListToSDK(ctx, m.Effect.AllowedTools)
		e.AllowedTools = &s
	}
	if !m.Effect.DeniedTools.IsNull() && !m.Effect.DeniedTools.IsUnknown() {
		s := stringListToSDK(ctx, m.Effect.DeniedTools)
		e.DeniedTools = &s
	}
	if !m.Effect.GrantToolsets.IsNull() && !m.Effect.GrantToolsets.IsUnknown() {
		s := stringListToSDK(ctx, m.Effect.GrantToolsets)
		e.GrantToolsets = &s
	}
	if !m.Effect.DenyToolsets.IsNull() && !m.Effect.DenyToolsets.IsUnknown() {
		s := stringListToSDK(ctx, m.Effect.DenyToolsets)
		e.DenyToolsets = &s
	}
	return e
}

// keepConfigured carries the configured spelling of targets and the empty
// forms from the plan into state after a write. targets is Optional, so state
// must equal config; the platform's canonical form is in stored_targets.
func (m *MCPPolicyResourceModel) keepConfigured(plan MCPPolicyResourceModel) {
	m.Targets = plan.Targets
	m.keepEmptyForms(plan)
}

// keepEmptyForms restores an empty form the config wrote explicitly for the
// Optional, non-Computed attributes, which mcpPolicyToModel reads as null.
func (m *MCPPolicyResourceModel) keepEmptyForms(prior MCPPolicyResourceModel) {
	m.Description = keepEmptyString(prior.Description, m.Description)
	m.ProviderInstances = keepEmptyList(prior.ProviderInstances, m.ProviderInstances)
	if m.Targets == nil && prior.Targets != nil && len(prior.Targets) == 0 {
		m.Targets = prior.Targets
	}
	if m.Criteria == nil && prior.Criteria != nil && len(prior.Criteria) == 0 {
		m.Criteria = prior.Criteria
	}
}

// reconcileTargets decides what targets holds after a refresh, as
// reconcileMatchValues does for an endpoint MCP rule: the configured spelling
// is kept while the platform's stored form is unchanged since the last apply,
// and replaced by the stored form when someone edited the policy elsewhere. On
// import there is no prior stored form, so the stored form is taken.
func reconcileTargets(prior, refreshed MCPPolicyResourceModel) []MCPPolicyTargetModel {
	if prior.StoredTargets.IsNull() || prior.StoredTargets.IsUnknown() {
		return refreshed.Targets
	}
	if refreshed.StoredTargets.Equal(prior.StoredTargets) {
		return prior.Targets
	}
	return refreshed.Targets
}

func mcpPolicyToModel(tenantID string, pol *adminapi.MCPPolicy) MCPPolicyResourceModel {
	m := MCPPolicyResourceModel{TenantID: types.StringValue(tenantID)}
	if pol.Id != nil {
		m.PolicyID = types.StringValue(pol.Id.String())
	} else {
		m.PolicyID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.PolicyID.ValueString())
	m.Name = strPtrToTF(pol.Name)
	m.Description = emptyStringAsNull(pol.Description)
	m.Priority = int32PtrToTF(pol.Priority)
	m.Enabled = boolPtrOrDefault(pol.Enabled)
	m.ValidateArguments = boolPtrOrDefault(pol.ValidateArguments)
	m.Mode = types.StringValue("enforce")
	if pol.Mode != nil && *pol.Mode != "" {
		m.Mode = types.StringValue(string(*pol.Mode))
	}
	m.AppliesAt = types.StringValue("both")
	if pol.AppliesAt != nil && *pol.AppliesAt != "" {
		m.AppliesAt = types.StringValue(string(*pol.AppliesAt))
	}
	// The platform reads an unset combinator as OR.
	m.CriteriaCombinator = types.StringValue("OR")
	if pol.CriteriaCombinator != nil && *pol.CriteriaCombinator != "" {
		m.CriteriaCombinator = types.StringValue(*pol.CriteriaCombinator)
	}
	m.ProviderInstances = emptyListAsNull(stringSliceToList(pol.ProviderInstances))
	m.CreatedAt = timePtrToTF(pol.CreatedAt)
	m.UpdatedAt = timePtrToTF(pol.UpdatedAt)
	m.SettingsInForce = boolPtrOrDefault(pol.SettingsInForce)
	if pol.SiblingOf != nil {
		m.SiblingOf = types.StringValue(pol.SiblingOf.String())
	} else {
		m.SiblingOf = types.StringNull()
	}

	m.Version = int64PtrToTF(pol.Version)
	m.ManagedBy = enumPtrToTF(pol.ManagedBy)
	m.ManagedByClientID = strPtrToTF(pol.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(pol.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(pol.LastModifiedBy)

	m.Targets, m.StoredTargets = mcpPolicyTargetsFromSDK(pol.Targets)

	if pol.Effect != nil {
		m.Effect = &MCPPolicyEffectModel{
			Type:               types.StringValue(string(pol.Effect.Effect)),
			Message:            strPtrToTF(pol.Effect.Message),
			AllowedTools:       stringSliceToList(pol.Effect.AllowedTools),
			DeniedTools:        stringSliceToList(pol.Effect.DeniedTools),
			GrantToolsets:      stringSliceToList(pol.Effect.GrantToolsets),
			DenyToolsets:       stringSliceToList(pol.Effect.DenyToolsets),
			RateLimitPerMinute: int32PtrToTF(pol.Effect.RateLimitPerMinute),
		}
	}
	m.Criteria = criteriaListFromSDK(pol.Criteria)
	if pol.Unless != nil && pol.Unless.Groups != nil && len(*pol.Unless.Groups) > 0 {
		comb := types.StringValue("AND")
		if pol.Unless.Combinator != nil && *pol.Unless.Combinator != "" {
			comb = types.StringValue(string(*pol.Unless.Combinator))
		}
		m.Unless = &MCPPolicyUnlessModel{Combinator: comb, Groups: criteriaListFromSDK(pol.Unless.Groups)}
	}
	return m
}

// mcpPolicyTargetsFromSDK returns the stored targets twice: as the model's
// targets (replaced by the configured spelling where it still applies) and as
// stored_targets.
func mcpPolicyTargetsFromSDK(in *[]gen.McpPolicyTarget) ([]MCPPolicyTargetModel, types.List) {
	if in == nil || len(*in) == 0 {
		return nil, types.ListValueMust(mcpPolicyTargetObjectType, []attr.Value{})
	}
	models := make([]MCPPolicyTargetModel, 0, len(*in))
	objs := make([]attr.Value, 0, len(*in))
	for _, t := range *in {
		kind := enumPtrToTF(t.Kind)
		values := stringSliceToList(t.Values)
		if values.IsNull() {
			values = types.ListValueMust(types.StringType, []attr.Value{})
		}
		models = append(models, MCPPolicyTargetModel{Kind: kind, Values: values})
		objs = append(objs, types.ObjectValueMust(mcpPolicyTargetObjectType.AttrTypes, map[string]attr.Value{
			"kind":   kind,
			"values": values,
		}))
	}
	return models, types.ListValueMust(mcpPolicyTargetObjectType, objs)
}
