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

// Removing the condition widens the rule. Omitting the attribute is an error
// (the config must choose), an explicit `require_compliant = false` warns, and
// nothing else is a removal.
func TestEndpointRuleModifyPlan_PosturePredicateRemoval(t *testing.T) {
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
		none     = ""
		omits    = "omits the rule's device posture condition"
		removes  = "removes the rule's device posture condition"
		ruleName = "allow-claude-compliant"
	)
	for name, tc := range map[string]struct {
		state, plan *tftypes.Value // nil state = create, nil plan = destroy
		planName    tftypes.Value
		wantError   string
		wantWarning string
	}{
		"omitted": {state: ptr(pred(true)), plan: ptr(null), wantError: omits},
		"omitted, name unknown": {state: ptr(pred(true)), plan: ptr(null),
			planName: tftypes.NewValue(tftypes.String, tftypes.UnknownValue), wantError: omits},
		"set to false":        {state: ptr(pred(true)), plan: ptr(pred(false)), wantWarning: removes},
		"unchanged":           {state: ptr(pred(true)), plan: ptr(pred(true))},
		"added":               {state: ptr(null), plan: ptr(pred(true))},
		"false to true":       {state: ptr(pred(false)), plan: ptr(pred(true))},
		"false, then omitted": {state: ptr(pred(false)), plan: ptr(null)},
		"never set":           {state: ptr(null), plan: ptr(null)},
		"created with one":    {plan: ptr(pred(true))},
		"unknown object":      {state: ptr(pred(true)), plan: ptr(unknown)},
		"unknown attribute":   {state: ptr(pred(true)), plan: ptr(pred(tftypes.UnknownValue))},
		"destroyed with one":  {state: ptr(pred(true))},
	} {
		t.Run(name, func(t *testing.T) {
			planName := tc.planName
			if planName.Type() == nil {
				planName = tftypes.NewValue(tftypes.String, ruleName)
			}
			rule := func(p *tftypes.Value, name tftypes.Value) tftypes.Value {
				if p == nil {
					return tftypes.NewValue(s.Type().TerraformType(ctx), nil)
				}
				return endpointRuleValue(t, s, map[string]tftypes.Value{
					"name":              name,
					"posture_predicate": *p,
				})
			}
			state := tfsdk.State{Schema: s, Raw: rule(tc.state, tftypes.NewValue(tftypes.String, ruleName))}
			plan := tfsdk.Plan{Schema: s, Raw: rule(tc.plan, planName)}

			r := &EndpointDestinationRuleResource{}
			resp := &resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan}, resp)

			for kind, tcase := range map[string]struct {
				want string
				got  diag.Diagnostics
			}{
				"error":   {tc.wantError, resp.Diagnostics.Errors()},
				"warning": {tc.wantWarning, resp.Diagnostics.Warnings()},
			} {
				if tcase.want == none {
					if len(tcase.got) != 0 {
						t.Errorf("unexpected %s: %v", kind, tcase.got)
					}
					continue
				}
				if len(tcase.got) != 1 {
					t.Fatalf("got %d %ss, want 1: %v", len(tcase.got), kind, tcase.got)
				}
				if got := tcase.got[0].Summary(); !strings.Contains(got, tcase.want) {
					t.Errorf("%s summary = %q, want %q", kind, got, tcase.want)
				}
				detail := tcase.got[0].Detail()
				if !strings.Contains(detail, `"`+ruleName+`"`) {
					t.Errorf("%s detail = %q, want it to name the rule", kind, detail)
				}
				if kind == "error" && !strings.Contains(detail, "require_compliant = false") {
					t.Errorf("error detail = %q, want it to say how to remove the condition", detail)
				}
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
