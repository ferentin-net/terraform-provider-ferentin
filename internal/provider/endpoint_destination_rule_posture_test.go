package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// posture_predicate (ferentin-net/terraform-provider-ferentin#20). The
// platform's PUT stores an absent predicate as NULL, so a provider that does
// not send it erases a condition set in the console on every apply, and a
// provider that does not read it hides that erasure from the plan.

func predicate(requireCompliant bool) *EndpointPosturePredicateModel {
	return &EndpointPosturePredicateModel{RequireCompliant: types.BoolValue(requireCompliant)}
}

func bareEndpointRule() EndpointDestinationRuleResourceModel {
	null := types.ListNull(types.StringType)
	return EndpointDestinationRuleResourceModel{
		Name:             types.StringValue("allow-claude-compliant"),
		Action:           types.StringValue("allow"),
		DestinationKind:  types.StringValue("ai_provider"),
		CatalogSlug:      types.StringValue("anthropic"),
		DestinationHosts: null,
		AppBundleIDs:     null,
		AppSigningIDs:    null,
		AppTeamIDs:       null,
		DeviceGroupIDs:   null,
	}
}

// The predicate is on the wire whenever the config declares it, under the key
// the platform reads, and absent otherwise.
func TestEndpointRuleToWrite_PosturePredicate(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		in   *EndpointPosturePredicateModel
		want string
	}{
		"unset":                   {in: nil, want: `{"action":"allow"`},
		"require_compliant true":  {in: predicate(true), want: `"posturePredicate":{"requireCompliant":true}`},
		"require_compliant false": {in: predicate(false), want: `"posturePredicate":{"requireCompliant":false}`},
	} {
		t.Run(name, func(t *testing.T) {
			m := bareEndpointRule()
			m.PosturePredicate = tc.in
			body, _, diags := m.toWrite(ctx)
			if diags.HasError() {
				t.Fatalf("toWrite: %v", diags)
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(raw), tc.want) {
				t.Errorf("body = %s, want it to contain %s", raw, tc.want)
			}
			if tc.in == nil && strings.Contains(string(raw), "posturePredicate") {
				t.Errorf("body = %s, want no posturePredicate for an unset attribute", raw)
			}
		})
	}
}

// Only a predicate that constrains something reads into state. Every form the
// platform treats as "no condition" reads as null, and the config's explicit
// `require_compliant = false` is kept so it does not plan a perpetual diff.
func TestEndpointRule_PosturePredicateReadBack(t *testing.T) {
	var diags diag.Diagnostics
	read := func(p *gen.EndpointPosturePredicateDto) EndpointDestinationRuleResourceModel {
		return endpointRuleToModel(fixtureTenantID, &adminapi.EndpointDestinationRule{PosturePredicate: p}, &diags)
	}

	for name, p := range map[string]*gen.EndpointPosturePredicateDto{
		"absent":                  nil,
		"empty object":            {},
		"require_compliant false": {RequireCompliant: boolPtr(false)},
	} {
		if got := read(p).PosturePredicate; got != nil {
			t.Errorf("%s: posture_predicate = %+v, want null", name, got)
		}
	}
	got := read(&gen.EndpointPosturePredicateDto{RequireCompliant: boolPtr(true)}).PosturePredicate
	if got == nil || !got.RequireCompliant.ValueBool() {
		t.Fatalf("posture_predicate = %+v, want require_compliant = true", got)
	}

	m := read(nil)
	m.keepEmptyForms(EndpointDestinationRuleResourceModel{PosturePredicate: predicate(false)})
	if m.PosturePredicate == nil || m.PosturePredicate.RequireCompliant.ValueBool() {
		t.Errorf("config wrote require_compliant = false: got %+v, want it kept", m.PosturePredicate)
	}

	// A condition removed in the console must read as removed, not be papered
	// over by the prior true: that is the drift the plan has to show.
	m = read(nil)
	m.keepEmptyForms(EndpointDestinationRuleResourceModel{PosturePredicate: predicate(true)})
	if m.PosturePredicate != nil {
		t.Errorf("prior true, platform none: got %+v, want null", m.PosturePredicate)
	}

	// And a server value always wins over a prior empty form.
	m = read(&gen.EndpointPosturePredicateDto{RequireCompliant: boolPtr(true)})
	m.keepEmptyForms(EndpointDestinationRuleResourceModel{PosturePredicate: predicate(false)})
	if m.PosturePredicate == nil || !m.PosturePredicate.RequireCompliant.ValueBool() {
		t.Errorf("platform true, prior false: got %+v, want true", m.PosturePredicate)
	}
}

// The scenario from #20: a predicate set in the console, state written by a
// provider that did not model it. Refresh must bring it into state, so a config
// without it plans the removal (and warns) instead of erasing it silently.
func TestEndpointRuleRead_PicksUpConsoleSetPosturePredicate(t *testing.T) {
	ctx := context.Background()
	ruleID := mustParseUUID(t, "11111111-1111-4111-8111-111111111111")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + ruleID.String() + `","name":"allow-claude","action":"allow",` +
			`"version":4,"posturePredicate":{"requireCompliant":true}}`))
	}))
	defer srv.Close()
	sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: srv.URL, Token: "test"})
	if err != nil {
		t.Fatalf("sdk: %v", err)
	}

	s := endpointRuleSchema(t)
	prior := tfsdk.State{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
		"tenant_id": tftypes.NewValue(tftypes.String, fixtureTenantID),
		"rule_id":   tftypes.NewValue(tftypes.String, ruleID.String()),
		"name":      tftypes.NewValue(tftypes.String, "allow-claude"),
		"action":    tftypes.NewValue(tftypes.String, "allow"),
	})}

	r := &EndpointDestinationRuleResource{sdk: sdk, tenantID: fixtureTenantID}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: prior.Raw}}
	r.Read(ctx, resource.ReadRequest{State: prior}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var got EndpointDestinationRuleResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	if got.PosturePredicate == nil || !got.PosturePredicate.RequireCompliant.ValueBool() {
		t.Errorf("posture_predicate = %+v, want require_compliant = true from the platform", got.PosturePredicate)
	}
}

func TestEndpointRuleModifyPlan_PosturePredicateWarnings(t *testing.T) {
	ctx := context.Background()
	s := endpointRuleSchema(t)
	objType := s.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes["posture_predicate"]
	pred := func(v interface{}) tftypes.Value {
		return tftypes.NewValue(objType, map[string]tftypes.Value{
			"require_compliant": tftypes.NewValue(tftypes.Bool, v),
		})
	}
	null := tftypes.NewValue(objType, nil)
	unknown := tftypes.NewValue(objType, tftypes.UnknownValue)

	const (
		none    = ""
		removes = "removes the rule's device posture condition"
		adds    = "puts a device posture condition into force"
	)
	// stateEnabled / planEnabled: nil is a null `enabled` (an old state, so on).
	for name, tc := range map[string]struct {
		state, plan               *tftypes.Value // nil state = create
		stateEnabled, planEnabled interface{}
		want                      string
	}{
		"removed":             {state: ptr(pred(true)), plan: ptr(null), want: removes},
		"set to false":        {state: ptr(pred(true)), plan: ptr(pred(false)), want: removes},
		"unchanged":           {state: ptr(pred(true)), plan: ptr(pred(true)), want: none},
		"added":               {state: ptr(null), plan: ptr(pred(true)), want: adds},
		"false to true":       {state: ptr(pred(false)), plan: ptr(pred(true)), want: adds},
		"created with one":    {plan: ptr(pred(true)), want: adds},
		"created without one": {plan: ptr(null), want: none},
		"never set":           {state: ptr(null), plan: ptr(null), want: none},
		"false to null":       {state: ptr(pred(false)), plan: ptr(null), want: none},
		"unknown object":      {state: ptr(pred(true)), plan: ptr(unknown), want: none},
		"unknown attribute":   {state: ptr(pred(true)), plan: ptr(pred(tftypes.UnknownValue)), want: none},
		"unknown on create":   {plan: ptr(unknown), want: none},
		"destroyed with one":  {state: ptr(pred(true)), want: none},

		// cp-server projects only enabled rules, so a condition takes effect
		// (and moves devices to 1.4.0) when the rule is on.
		"added to a disabled rule": {state: ptr(null), plan: ptr(pred(true)), stateEnabled: false, planEnabled: false, want: none},
		"created disabled":         {plan: ptr(pred(true)), planEnabled: false, want: none},
		"enabled with one":         {state: ptr(pred(true)), plan: ptr(pred(true)), stateEnabled: false, planEnabled: true, want: adds},
		"disabled with one":        {state: ptr(pred(true)), plan: ptr(pred(true)), stateEnabled: true, planEnabled: false, want: none},
		"enabled unknown":          {state: ptr(null), plan: ptr(pred(true)), planEnabled: tftypes.UnknownValue, want: none},
		"removed from disabled":    {state: ptr(pred(true)), plan: ptr(null), stateEnabled: false, planEnabled: false, want: removes},
	} {
		t.Run(name, func(t *testing.T) {
			rule := func(p *tftypes.Value, enabled interface{}) tftypes.Value {
				if p == nil {
					return tftypes.NewValue(s.Type().TerraformType(ctx), nil)
				}
				return endpointRuleValue(t, s, map[string]tftypes.Value{
					"name":              tftypes.NewValue(tftypes.String, "allow-claude-compliant"),
					"enabled":           tftypes.NewValue(tftypes.Bool, enabled),
					"posture_predicate": *p,
				})
			}
			state := tfsdk.State{Schema: s, Raw: rule(tc.state, tc.stateEnabled)}
			plan := tfsdk.Plan{Schema: s, Raw: rule(tc.plan, tc.planEnabled)}

			r := &EndpointDestinationRuleResource{}
			resp := &resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan}, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
			}
			warnings := resp.Diagnostics.Warnings()
			if tc.want == none {
				if len(warnings) != 0 {
					t.Errorf("unexpected warning: %v", warnings)
				}
				return
			}
			if len(warnings) != 1 {
				t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
			}
			if got := warnings[0].Summary(); !strings.Contains(got, tc.want) {
				t.Errorf("warning summary = %q, want %q", got, tc.want)
			}
			if got := warnings[0].Detail(); !strings.Contains(got, "allow-claude-compliant") {
				t.Errorf("warning detail = %q, want it to name the rule", got)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// A platform that ignores posturePredicate saves a rule matching every device.
// The apply must fail saying so, not with the framework's generic error.
func TestCheckPosturePredicateStored(t *testing.T) {
	for name, tc := range map[string]struct {
		plan, got *EndpointPosturePredicateModel
		wantError bool
	}{
		"stored":            {plan: predicate(true), got: predicate(true)},
		"dropped":           {plan: predicate(true), got: nil, wantError: true},
		"none configured":   {plan: nil, got: nil},
		"false, kept empty": {plan: predicate(false), got: predicate(false)},
		"false, read null":  {plan: predicate(false), got: nil},
	} {
		t.Run(name, func(t *testing.T) {
			plan := bareEndpointRule()
			plan.PosturePredicate = tc.plan
			got := bareEndpointRule()
			got.PosturePredicate = tc.got
			var diags diag.Diagnostics
			checkPosturePredicateStored(plan, got, &diags)
			if diags.HasError() != tc.wantError {
				t.Fatalf("errors = %v, want error %v", diags.Errors(), tc.wantError)
			}
			if tc.wantError && !strings.Contains(diags.Errors()[0].Detail(), "allow-claude-compliant") {
				t.Errorf("detail = %q, want it to name the rule", diags.Errors()[0].Detail())
			}
		})
	}
}
