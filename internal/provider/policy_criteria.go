package provider

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// Shared ABAC criteria authoring — schema, Terraform models, and both
// conversion directions — for every resource that carries policy criteria
// (platform#2040).
//
// Four resources use this: ferentin_llm_policy, ferentin_mcp_policy,
// ferentin_data_protection_policy, and ferentin_endpoint_destination_rule.
// The first three had near-identical private copies of all of it; the endpoint
// rule was the fourth caller that finally paid for the extraction.
//
// TWO WIRE SHAPES, ONE MODEL. The three policy resources send criteria as the
// typed gen.PolicyCriteria (see criteriaListToSDK / criteriaListFromSDK). The
// endpoint rule's column is untyped on the platform side — its DTO field is a
// `List<Object>` stored verbatim — so it goes through the opaque
// map[string]interface{} pair instead (criteriaListToOpaque /
// criteriaListFromOpaque). The Terraform-facing model and schema are identical
// either way, which is the point: an operator writes the same `criteria` block
// on all four resources.
//
// The two shapes differ only in Go typing, NOT in what goes on the wire: both
// emit the same JSON, `value` included. That is load-bearing — the platform
// stores criteria as an opaque document either way, the admin console writes
// the same shape, and shared-core's PolicyCriteriaEvaluator compares
// `condition.getValue()` directly. See criteriaValueToTF for what the provider
// used to send instead, and why rows written that way never matched.

// PolicyCriteriaModel is one ABAC criteria group: a set of conditions joined by
// `operator`. Mirrors the platform's PolicyCriteria (shared-core
// com.ferentin.auth.commons.model.PolicyCriteria).
type PolicyCriteriaModel struct {
	Operator    types.String                   `tfsdk:"operator"`
	Type        types.String                   `tfsdk:"type"`
	Description types.String                   `tfsdk:"description"`
	Conditions  []PolicyCriteriaConditionModel `tfsdk:"conditions"`
}

// PolicyCriteriaConditionModel mirrors the platform's CriteriaCondition.
//
// `value` is a JSON-encoded STRING in HCL — the operator writes
// `value = jsonencode("engineering")` / `jsonencode(["a","b"])` / `jsonencode(100)`.
// Terraform has no "any" primitive, so a string carrying JSON is how a condition
// compares against a non-string claim without a separate attribute per type.
type PolicyCriteriaConditionModel struct {
	Field         types.String `tfsdk:"field"`
	Operator      types.String `tfsdk:"operator"`
	Value         types.String `tfsdk:"value"`
	ValueType     types.String `tfsdk:"value_type"`
	CaseSensitive types.Bool   `tfsdk:"case_sensitive"`
	Description   types.String `tfsdk:"description"`
}

// criteriaSchemaOptions carries the per-resource wording (and the endpoint
// rule's `type` default) for criteriaSchemaAttribute. Everything structural —
// attribute names, types, optionality, validators — is deliberately NOT
// configurable: a `criteria` block must mean the same thing on every resource
// that has one.
type criteriaSchemaOptions struct {
	// Description is the top-level attribute description.
	Description string
	// TypeDescription documents the per-group `type`.
	TypeDescription string
	// TypeDefault, when non-empty, makes `type` default to it rather than
	// relying on a server-side default. Only the endpoint rule sets this: its
	// criteria column is opaque, so the platform defaults nothing.
	TypeDefault string
	// TypeOneOf, when non-empty, restricts `type` to these values at plan time.
	// Only the endpoint rule sets it, for the same reason as TypeDefault: its
	// column is stored opaquely and validated nowhere, so a typo survives the
	// write and reaches the agent, which DROPS a rule whose criteria will not
	// parse — fail-open for a `block` rule. The three policy resources go
	// through typed DTOs the platform checks, and their Java DTO enforces only
	// @NotBlank, so a validator there could reject configs the API accepts.
	TypeOneOf []string
	// FieldDescription documents `conditions[].field` — the useful field names
	// differ per surface.
	FieldDescription string
	// ValueExample is a single `jsonencode(...)` example for the resource.
	ValueExample string
}

// criteriaSchemaAttribute builds the shared `criteria` attribute.
func criteriaSchemaAttribute(o criteriaSchemaOptions) schema.ListNestedAttribute {
	typeAttr := schema.StringAttribute{
		MarkdownDescription: o.TypeDescription,
		Optional:            true,
		Computed:            true,
	}
	if o.TypeDefault != "" {
		typeAttr.Default = stringdefault.StaticString(o.TypeDefault)
	}
	if len(o.TypeOneOf) > 0 {
		typeAttr.Validators = []validator.String{stringvalidator.OneOf(o.TypeOneOf...)}
	}

	return schema.ListNestedAttribute{
		MarkdownDescription: o.Description,
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"operator": schema.StringAttribute{
					MarkdownDescription: "Logical operator joining the conditions: `AND` or `OR`.",
					Required:            true,
					Validators:          []validator.String{stringvalidator.OneOf("AND", "OR")},
				},
				"type": typeAttr,
				"description": schema.StringAttribute{
					MarkdownDescription: "Optional human-readable description.",
					Optional:            true,
				},
				"conditions": schema.ListNestedAttribute{
					MarkdownDescription: "Conditions evaluated under the parent `operator`. At least one is required.",
					Required:            true,
					// Mirrors @NotEmpty on the platform's PolicyCriteria.conditions.
					// A group with no conditions has no defined truth value; the
					// three policy resources would 400 on it at apply, and the
					// endpoint rule (whose criteria column is opaque and therefore
					// unvalidated server-side) would store it and hand the agent a
					// group it cannot evaluate.
					Validators: []validator.List{listvalidator.SizeAtLeast(1)},
					NestedObject: schema.NestedAttributeObject{
						Attributes: map[string]schema.Attribute{
							"field": schema.StringAttribute{
								MarkdownDescription: o.FieldDescription,
								Required:            true,
							},
							"operator": schema.StringAttribute{
								MarkdownDescription: "Comparison operator (`equals`, `in`, `lt`, `gt`, `ends_with`, …).",
								Required:            true,
							},
							"value": schema.StringAttribute{
								MarkdownDescription: "JSON-encoded value to compare against. Examples: " +
									o.ValueExample + ", `jsonencode([\"a\",\"b\"])`, `jsonencode(100)`.",
								Optional: true,
							},
							"value_type": schema.StringAttribute{
								MarkdownDescription: "Optional type hint for the value (`string`, `int`, `list`, …). " +
									"The platform defaults it to `string` when unset.",
								Optional: true,
								Computed: true,
							},
							"case_sensitive": schema.BoolAttribute{
								MarkdownDescription: "For string operations. Platform defaults to `true` when omitted.",
								Optional:            true,
								Computed:            true,
							},
							"description": schema.StringAttribute{
								MarkdownDescription: "Optional description.",
								Optional:            true,
							},
						},
					},
				},
			},
		},
	}
}

// -------------------------------------------------------------------------
// Typed wire shape (gen.PolicyCriteria) — llm / mcp / data-protection policies
// -------------------------------------------------------------------------

// criteriaListToSDK converts the Terraform models into the typed wire form.
// Returns nil (not an empty slice) when there are none, so callers can leave
// the field off the request body entirely.
func criteriaListToSDK(in []PolicyCriteriaModel) ([]gen.PolicyCriteria, diag.Diagnostics) {
	var diags diag.Diagnostics
	if len(in) == 0 {
		return nil, diags
	}
	out := make([]gen.PolicyCriteria, 0, len(in))
	for i, c := range in {
		conv, d := c.toSDK(fmt.Sprintf("criteria[%d]", i))
		diags.Append(d...)
		if !d.HasError() {
			out = append(out, conv)
		}
	}
	return out, diags
}

func (c PolicyCriteriaModel) toSDK(pathHint string) (gen.PolicyCriteria, diag.Diagnostics) {
	var diags diag.Diagnostics
	out := gen.PolicyCriteria{
		Operator: gen.PolicyCriteriaOperator(c.Operator.ValueString()),
	}
	if !c.Type.IsNull() && !c.Type.IsUnknown() {
		out.Type = gen.PolicyCriteriaType(c.Type.ValueString())
	}
	if !c.Description.IsNull() && !c.Description.IsUnknown() {
		v := c.Description.ValueString()
		out.Description = &v
	}
	for i, cond := range c.Conditions {
		conv, d := cond.toSDK(fmt.Sprintf("%s.conditions[%d]", pathHint, i))
		diags.Append(d...)
		if !d.HasError() {
			out.Conditions = append(out.Conditions, conv)
		}
	}
	return out, diags
}

func (c PolicyCriteriaConditionModel) toSDK(pathHint string) (gen.CriteriaCondition, diag.Diagnostics) {
	var diags diag.Diagnostics
	out := gen.CriteriaCondition{
		Field:    c.Field.ValueString(),
		Operator: gen.CriteriaConditionOperator(c.Operator.ValueString()),
	}
	if !c.CaseSensitive.IsNull() && !c.CaseSensitive.IsUnknown() {
		v := c.CaseSensitive.ValueBool()
		out.CaseSensitive = &v
	}
	if !c.Description.IsNull() && !c.Description.IsUnknown() {
		v := c.Description.ValueString()
		out.Description = &v
	}
	if !c.ValueType.IsNull() && !c.ValueType.IsUnknown() {
		v := gen.CriteriaConditionValueType(c.ValueType.ValueString())
		out.ValueType = &v
	}
	decoded, ok, d := decodeConditionValue(c.Value, pathHint)
	diags.Append(d...)
	if ok {
		// Sent RAW — the same shape the opaque endpoint path writes, the admin
		// console writes, and shared-core's PolicyCriteriaEvaluator compares
		// against (its CriteriaCondition.value is a plain Object).
		out.Value = decoded
	}
	return out, diags
}

// criteriaListFromSDK maps the typed wire form back into Terraform state.
func criteriaListFromSDK(in *[]gen.PolicyCriteria) []PolicyCriteriaModel {
	if in == nil || len(*in) == 0 {
		return nil
	}
	out := make([]PolicyCriteriaModel, 0, len(*in))
	for _, c := range *in {
		cm := PolicyCriteriaModel{
			Operator:    types.StringValue(string(c.Operator)),
			Type:        types.StringValue(string(c.Type)),
			Description: strPtrToTF(c.Description),
		}
		for _, cond := range c.Conditions {
			cm.Conditions = append(cm.Conditions, PolicyCriteriaConditionModel{
				Field:         types.StringValue(cond.Field),
				Operator:      types.StringValue(string(cond.Operator)),
				CaseSensitive: boolPtrOrDefault(cond.CaseSensitive),
				Description:   strPtrToTF(cond.Description),
				ValueType:     enumPtrToTF(cond.ValueType),
				Value:         criteriaValueToTF(cond.Value),
			})
		}
		out = append(out, cm)
	}
	return out
}

// criteriaValueToTF re-encodes a decoded condition value as the JSON string the
// operator wrote with jsonencode. Both wire shapes share it — after
// platform#2040 the typed and opaque paths carry the same raw value.
//
// Go's encoding/json and Terraform's jsonencode both emit compact, sorted-key
// JSON, so the round trip is stable and shows no spurious diff.
//
// NO LEGACY UNWRAP, deliberately. Provider versions before platform#2040 sent
// every value inside a `{"value": <actual>}` envelope, forced by an SDK type
// that could only hold a map (see the Patch E note in the SDK's
// scripts/refresh-openapi.sh). Rows written that way are still in the database
// and never matched anything — the evaluator compares a Map against a claim
// string. Silently unwrapping them here would make state equal config, produce
// NO diff, and leave the broken row in place forever. Surfacing the stored
// envelope instead means the first plan after upgrading shows
// `{"value":"legal"}` → `"legal"` and the apply repairs the row. The ugly diff
// IS the migration.
func criteriaValueToTF(v interface{}) types.String {
	if v == nil {
		return types.StringNull()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return types.StringNull()
	}
	return types.StringValue(string(b))
}

// -------------------------------------------------------------------------
// Opaque wire shape ([]map[string]interface{}) — endpoint destination rules
// -------------------------------------------------------------------------

// criteriaGroupKeys / criteriaConditionKeys are the JSON keys this provider
// models, mapped to the JSON type each must have. Anything else — an unknown
// key, or a known key carrying a type the model cannot hold — is reported by
// criteriaListFromOpaque so a write-back warns instead of silently dropping it.
//
// The type check matters as much as the key check: `case_sensitive: "false"`
// (a string) parses as a valid JSON document but lands as a null Bool in the
// model, and the next full-replace PUT would drop it — flipping the condition
// back to the platform's case-sensitive default and changing who matches.
type criteriaJSONKind int

const (
	criteriaJSONString criteriaJSONKind = iota
	criteriaJSONBool
	criteriaJSONArray
	criteriaJSONAny // `value` — any JSON document is legitimate
)

var (
	criteriaGroupKeys = map[string]criteriaJSONKind{
		"type": criteriaJSONString, "operator": criteriaJSONString,
		"description": criteriaJSONString, "conditions": criteriaJSONArray,
	}
	criteriaConditionKeys = map[string]criteriaJSONKind{
		"field": criteriaJSONString, "operator": criteriaJSONString,
		"value": criteriaJSONAny, "value_type": criteriaJSONString,
		"case_sensitive": criteriaJSONBool, "description": criteriaJSONString,
	}
)

// criteriaKeyIsModelled reports whether a wire key is one this provider both
// knows and can hold without loss. A null value counts as modelled: absent and
// explicitly-null both map to a null attribute, which round-trips.
func criteriaKeyIsModelled(known map[string]criteriaJSONKind, key string, v interface{}) bool {
	kind, ok := known[key]
	if !ok {
		return false
	}
	if v == nil {
		return true
	}
	switch kind {
	case criteriaJSONString:
		_, ok := v.(string)
		return ok
	case criteriaJSONBool:
		_, ok := v.(bool)
		return ok
	case criteriaJSONArray:
		_, ok := v.([]interface{})
		return ok
	default: // criteriaJSONAny
		return true
	}
}

// criteriaListToOpaque renders the shared models into the untyped JSON shape
// the endpoint rule's `criteria` column stores.
//
// Unlike the typed path this writes `value` RAW, with no `{"value": …}`
// envelope — matching what the admin console writes and what shared-core's
// PolicyCriteriaEvaluator compares against (its CriteriaCondition.value is a
// plain Object). Wrapping it here would produce a condition that can never
// match, which under an allowlist posture reads as a silent deny.
func criteriaListToOpaque(in []PolicyCriteriaModel) ([]map[string]interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics
	if len(in) == 0 {
		return nil, diags
	}
	out := make([]map[string]interface{}, 0, len(in))
	for i, c := range in {
		pathHint := fmt.Sprintf("criteria[%d]", i)
		group := map[string]interface{}{"operator": c.Operator.ValueString()}
		if v, ok := knownString(c.Type); ok {
			group["type"] = v
		}
		if v, ok := knownString(c.Description); ok {
			group["description"] = v
		}

		conds := make([]interface{}, 0, len(c.Conditions))
		for j, cond := range c.Conditions {
			condHint := fmt.Sprintf("%s.conditions[%d]", pathHint, j)
			m := map[string]interface{}{
				"field":    cond.Field.ValueString(),
				"operator": cond.Operator.ValueString(),
			}
			if v, ok := knownString(cond.ValueType); ok {
				m["value_type"] = v
			}
			if v, ok := knownString(cond.Description); ok {
				m["description"] = v
			}
			if !cond.CaseSensitive.IsNull() && !cond.CaseSensitive.IsUnknown() {
				m["case_sensitive"] = cond.CaseSensitive.ValueBool()
			}
			decoded, ok, d := decodeConditionValue(cond.Value, condHint)
			diags.Append(d...)
			if ok {
				m["value"] = decoded
			}
			conds = append(conds, m)
		}
		group["conditions"] = conds
		out = append(out, group)
	}
	return out, diags
}

// criteriaListFromOpaque maps the stored JSON back into the shared models.
//
// The second return value lists JSON keys present on the wire that this
// provider does not model (e.g. a condition's `stage`). They are NOT part of
// state, so a subsequent write drops them — callers surface that as a warning
// rather than letting a criteria group quietly change meaning.
func criteriaListFromOpaque(in *[]map[string]interface{}) ([]PolicyCriteriaModel, []string) {
	if in == nil || len(*in) == 0 {
		return nil, nil
	}
	unmodelled := map[string]bool{}
	out := make([]PolicyCriteriaModel, 0, len(*in))

	for i, raw := range *in {
		for k, v := range raw {
			if !criteriaKeyIsModelled(criteriaGroupKeys, k, v) {
				unmodelled[fmt.Sprintf("criteria[%d].%s", i, k)] = true
			}
		}
		cm := PolicyCriteriaModel{
			Operator:    opaqueString(raw["operator"]),
			Type:        opaqueString(raw["type"]),
			Description: opaqueString(raw["description"]),
		}
		conds, _ := raw["conditions"].([]interface{})
		for j, rawCond := range conds {
			cond, ok := rawCond.(map[string]interface{})
			if !ok {
				unmodelled[fmt.Sprintf("criteria[%d].conditions[%d]", i, j)] = true
				continue
			}
			for k, v := range cond {
				if !criteriaKeyIsModelled(criteriaConditionKeys, k, v) {
					unmodelled[fmt.Sprintf("criteria[%d].conditions[%d].%s", i, j, k)] = true
				}
			}
			cm.Conditions = append(cm.Conditions, PolicyCriteriaConditionModel{
				Field:         opaqueString(cond["field"]),
				Operator:      opaqueString(cond["operator"]),
				ValueType:     opaqueString(cond["value_type"]),
				Description:   opaqueString(cond["description"]),
				CaseSensitive: opaqueBool(cond["case_sensitive"]),
				Value:         criteriaValueToTF(cond["value"]),
			})
		}
		out = append(out, cm)
	}

	if len(unmodelled) == 0 {
		return out, nil
	}
	keys := make([]string, 0, len(unmodelled))
	for k := range unmodelled {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return out, keys
}

// -------------------------------------------------------------------------
// Small shared bits
// -------------------------------------------------------------------------

// decodeConditionValue turns the HCL `value` string into the decoded JSON it
// carries. ok is false when the attribute is unset — an invalid-JSON value is a
// config error rather than a silent omission, because a condition that lost its
// value compares against nothing.
func decodeConditionValue(v types.String, pathHint string) (interface{}, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil, false, diags
	}
	var decoded interface{}
	if err := json.Unmarshal([]byte(v.ValueString()), &decoded); err != nil {
		diags.AddError(
			"Invalid JSON in criteria condition value",
			fmt.Sprintf("%s.value must be valid JSON (e.g. `jsonencode(\"engineering\")`): %v", pathHint, err),
		)
		return nil, false, diags
	}
	return decoded, true, diags
}

// knownString reports a config-set string, skipping null/unknown/empty so the
// key is omitted from the wire form rather than sent as "".
func knownString(v types.String) (string, bool) {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return "", false
	}
	return v.ValueString(), true
}

func opaqueString(v interface{}) types.String {
	s, ok := v.(string)
	if !ok {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func opaqueBool(v interface{}) types.Bool {
	b, ok := v.(bool)
	if !ok {
		return types.BoolNull()
	}
	return types.BoolValue(b)
}
