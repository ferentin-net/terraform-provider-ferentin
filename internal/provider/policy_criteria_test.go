package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// The shared criteria mapper backs four resources (platform#2040), so a change
// here moves llm/mcp/data-protection policies and endpoint destination rules at
// once. These tests pin the two things that differ between its two wire shapes.

func sampleCriteria() []PolicyCriteriaModel {
	return []PolicyCriteriaModel{{
		Operator:    types.StringValue("AND"),
		Type:        types.StringValue("claims"),
		Description: types.StringValue("legal only"),
		Conditions: []PolicyCriteriaConditionModel{{
			Field:         types.StringValue("department"),
			Operator:      types.StringValue("equals"),
			Value:         types.StringValue(`"legal"`),
			ValueType:     types.StringValue("string"),
			CaseSensitive: types.BoolValue(false),
			Description:   types.StringValue("match the department claim"),
		}},
	}}
}

// The typed path (llm / mcp / data-protection policies) must send `value` RAW
// and read it back unchanged.
//
// This is the platform#2040 correction. The provider used to wrap every value
// in a `{"value": …}` envelope because the generated SDK field could only hold
// a map. Nothing on the platform ever unwrapped it: shared-core's
// PolicyCriteriaEvaluator compares `condition.getValue()` directly, so
// Objects.equals("legal", Map{value=legal}) is false and every criteria-scoped
// policy written by Terraform silently applied to nobody.
func TestCriteriaListToSDK_RoundTrip(t *testing.T) {
	out, diags := criteriaListToSDK(sampleCriteria())
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(out) != 1 || len(out[0].Conditions) != 1 {
		t.Fatalf("out = %+v, want one group with one condition", out)
	}
	cond := out[0].Conditions[0]
	if cond.Value != "legal" {
		t.Errorf("Value = %#v, want the RAW string \"legal\" — an envelope is compared "+
			"as a Map against a claim string and never matches", cond.Value)
	}

	back := criteriaListFromSDK(&out)
	if len(back) != 1 || len(back[0].Conditions) != 1 {
		t.Fatalf("back = %+v, want one group with one condition", back)
	}
	if got := back[0].Conditions[0].Value.ValueString(); got != `"legal"` {
		t.Errorf("Value = %q, want the jsonencode form back", got)
	}
	if back[0].Conditions[0].CaseSensitive.ValueBool() {
		t.Error("CaseSensitive = true, want the configured false")
	}
	if got := back[0].Description.ValueString(); got != "legal only" {
		t.Errorf("Description = %q, want it preserved", got)
	}
}

// What the SDK puts on the wire has to be the console's shape, byte for byte —
// this is the assertion that would have caught the envelope.
func TestCriteriaListToSDK_WireJSONMatchesConsoleShape(t *testing.T) {
	out, _ := criteriaListToSDK(sampleCriteria())
	b, err := json.Marshal(out[0].Conditions[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["value"] != "legal" {
		t.Errorf("wire value = %#v, want \"legal\"; full body: %s", got["value"], b)
	}
}

// Criteria authored in the admin console — raw scalars and arrays — must
// DECODE. The old SDK type made this a hard error, so `terraform plan` failed
// outright on any policy someone had edited in the console.
func TestCriteriaCondition_DecodesConsoleAuthoredValues(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"scalar", `{"field":"department","operator":"equals","value":"legal"}`, `"legal"`},
		{"array", `{"field":"department","operator":"in","value":["legal","compliance"]}`, `["legal","compliance"]`},
		{"number", `{"field":"seats","operator":"greater_than","value":100}`, `100`},
		{"bool", `{"field":"admin","operator":"equals","value":false}`, `false`},
		{"object", `{"field":"range","operator":"equals","value":{"lower":1}}`, `{"lower":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c gen.CriteriaCondition
			if err := json.Unmarshal([]byte(tc.body), &c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := criteriaValueToTF(c.Value).ValueString(); got != tc.want {
				t.Errorf("value = %q, want %q", got, tc.want)
			}
		})
	}
}

// A pre-#2040 row still carries the envelope. It must surface AS the envelope,
// not be silently unwrapped: unwrapping makes state equal config, produces no
// diff, and leaves a row that never matches in the database forever. The ugly
// diff is the migration.
func TestCriteriaValueToTF_LegacyEnvelopeSurfacesAsDrift(t *testing.T) {
	legacy := map[string]interface{}{"value": "legal"}
	got := criteriaValueToTF(legacy).ValueString()
	if got != `{"value":"legal"}` {
		t.Errorf("value = %q, want the stored envelope verbatim so `plan` shows the repair", got)
	}
	if criteriaValueToTF(nil).IsNull() != true {
		t.Error("nil value => want null")
	}
}

// The opaque path (endpoint destination rules) writes `value` RAW — the same
// shape the typed path now sends.
func TestCriteriaListToOpaque_WritesRawValues(t *testing.T) {
	out, diags := criteriaListToOpaque(sampleCriteria())
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	cond := out[0]["conditions"].([]interface{})[0].(map[string]interface{})
	if cond["value"] != "legal" {
		t.Errorf("value = %#v, want the raw decoded string", cond["value"])
	}
	if cond["case_sensitive"] != false {
		t.Errorf("case_sensitive = %#v, want false carried through", cond["case_sensitive"])
	}
	if out[0]["type"] != "claims" {
		t.Errorf("type = %#v, want claims", out[0]["type"])
	}
}

// Both directions must agree on "none": nil in, nil out, so an absent block and
// an empty one cannot differ in state.
func TestCriteriaConversions_EmptyIsNil(t *testing.T) {
	if out, _ := criteriaListToSDK(nil); out != nil {
		t.Errorf("toSDK(nil) = %v, want nil", out)
	}
	if out, _ := criteriaListToOpaque([]PolicyCriteriaModel{}); out != nil {
		t.Errorf("toOpaque(empty) = %v, want nil", out)
	}
	if out := criteriaListFromSDK(nil); out != nil {
		t.Errorf("fromSDK(nil) = %v, want nil", out)
	}
	empty := []gen.PolicyCriteria{}
	if out := criteriaListFromSDK(&empty); out != nil {
		t.Errorf("fromSDK(empty) = %v, want nil", out)
	}
	if out, unmodelled := criteriaListFromOpaque(nil); out != nil || unmodelled != nil {
		t.Errorf("fromOpaque(nil) = %v / %v, want nil / nil", out, unmodelled)
	}
}

// A KNOWN key carrying an unexpected JSON type is as lossy as an unknown key:
// it lands as a null attribute and the next full-replace write drops it. The
// detector must catch both, or the warning it exists to raise never fires for
// the sneakier half.
func TestCriteriaListFromOpaque_ReportsWrongTypedKeys(t *testing.T) {
	in := []map[string]interface{}{{
		"operator": "AND",
		"conditions": []interface{}{
			map[string]interface{}{
				"field": "department", "operator": "equals", "value": "legal",
				"case_sensitive": "false", // string, not bool — silently dropped before
			},
		},
	}, {
		"operator":   "OR",
		"conditions": "not-an-array", // whole group's conditions unparseable
	}}

	models, unmodelled := criteriaListFromOpaque(&in)
	if len(models) != 2 {
		t.Fatalf("models = %d, want both groups kept", len(models))
	}
	joined := strings.Join(unmodelled, " ")
	if !strings.Contains(joined, "criteria[0].conditions[0].case_sensitive") {
		t.Errorf("unmodelled = %v, want the wrong-typed case_sensitive reported", unmodelled)
	}
	if !strings.Contains(joined, "criteria[1].conditions") {
		t.Errorf("unmodelled = %v, want the unparseable conditions list reported", unmodelled)
	}
	// An explicit JSON null is not lossy — absent and null both mean "unset".
	withNull := []map[string]interface{}{{
		"operator": "AND", "description": nil,
		"conditions": []interface{}{
			map[string]interface{}{"field": "d", "operator": "equals", "value": "x", "value_type": nil},
		},
	}}
	if _, unmodelled := criteriaListFromOpaque(&withNull); unmodelled != nil {
		t.Errorf("unmodelled = %v, want none for explicit nulls", unmodelled)
	}
}
