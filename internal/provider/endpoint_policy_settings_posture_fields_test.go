package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

func strSet(vals ...string) types.Set {
	elems := make([]attr.Value, len(vals))
	for i, v := range vals {
		elems[i] = types.StringValue(v)
	}
	return types.SetValueMust(types.StringType, elems)
}

func nullPostureSets(m *EndpointPolicySettingsResourceModel) {
	m.UnnamedFlowExemptCIDRs = types.SetNull(types.StringType)
	m.DohResolverHosts = types.SetNull(types.StringType)
	m.DohResolverIPs = types.SetNull(types.StringType)
	if m.LocalMcpApprovalConfig.IsNull() && m.LocalMcpApprovalConfig.ValueString() == "" {
		m.LocalMcpApprovalConfig = jsontypes.NewNormalizedNull()
	}
}

func TestEndpointSettings_PostureFieldsRoundTrip(t *testing.T) {
	m := endpointSettingsToModel(fixtureTenantID, &adminapi.EndpointPolicySettings{
		UnapprovedAgentAction:   strPtr("block"),
		LocalMcpBlockEnabled:    boolPtr(true),
		UnnamedFlowBlockEnabled: boolPtr(true),
		UnnamedFlowExemptCidrs:  &[]string{"10.0.0.0/8"},
		DohResolverHosts:        &[]string{"dns.example.com"},
		DohResolverIps:          &[]string{"2001:db8::1"},
	})
	if m.UnapprovedAgentAction.ValueString() != "block" || !m.LocalMcpBlockEnabled.ValueBool() ||
		!m.UnnamedFlowBlockEnabled.ValueBool() {
		t.Errorf("scalars = %v / %v / %v", m.UnapprovedAgentAction, m.LocalMcpBlockEnabled, m.UnnamedFlowBlockEnabled)
	}
	if !m.UnnamedFlowExemptCIDRs.Equal(strSet("10.0.0.0/8")) || !m.DohResolverHosts.Equal(strSet("dns.example.com")) ||
		!m.DohResolverIPs.Equal(strSet("2001:db8::1")) {
		t.Errorf("sets = %v / %v / %v", m.UnnamedFlowExemptCIDRs, m.DohResolverHosts, m.DohResolverIPs)
	}
	// The platform returns an empty list as absent, or as [].
	for _, empty := range []*[]string{nil, {}} {
		m = endpointSettingsToModel(fixtureTenantID, &adminapi.EndpointPolicySettings{DohResolverHosts: empty})
		if !m.DohResolverHosts.IsNull() {
			t.Errorf("empty list %v read as %v, want null", empty, m.DohResolverHosts)
		}
	}
}

// Each field is sent only when configured; a configured [] is sent as [] (the
// platform's clear), never dropped.
func TestEndpointSettings_PostureFieldsSentOnlyWhenConfigured(t *testing.T) {
	plan := EndpointPolicySettingsResourceModel{
		UnapprovedAgentAction:   types.StringValue("block"),
		LocalMcpBlockEnabled:    types.BoolValue(false),
		UnnamedFlowBlockEnabled: types.BoolValue(true),
		UnnamedFlowExemptCIDRs:  strSet(),
		DohResolverHosts:        strSet("DNS.Example.COM"),
		DohResolverIPs:          strSet("192.0.2.1"),
		LocalMcpApprovalConfig:  jsontypes.NewNormalizedNull(),
	}
	body, d := plan.toWrite(context.Background())
	if d.HasError() {
		t.Fatal(d)
	}
	if body.UnapprovedAgentAction == nil || *body.UnapprovedAgentAction != "block" ||
		body.LocalMcpBlockEnabled == nil || *body.LocalMcpBlockEnabled ||
		body.UnnamedFlowBlockEnabled == nil || !*body.UnnamedFlowBlockEnabled {
		t.Errorf("scalars not sent as configured: %+v", body)
	}
	if body.UnnamedFlowExemptCidrs == nil || len(*body.UnnamedFlowExemptCidrs) != 0 {
		t.Errorf("configured [] must be sent as [], got %v", body.UnnamedFlowExemptCidrs)
	}
	if body.DohResolverHosts == nil || (*body.DohResolverHosts)[0] != "DNS.Example.COM" {
		t.Errorf("hosts = %v, want the configured spelling", body.DohResolverHosts)
	}

	omitUnconfigured(&body, EndpointPolicySettingsResourceModel{
		HookRuleEnforcement:     types.StringNull(),
		LocalMcpRiskPolicy:      types.ObjectNull(localMcpRiskPolicyAttrTypes),
		UnapprovedAgentAction:   types.StringNull(),
		LocalMcpBlockEnabled:    types.BoolNull(),
		UnnamedFlowBlockEnabled: types.BoolNull(),
		UnnamedFlowExemptCIDRs:  types.SetNull(types.StringType),
		DohResolverHosts:        types.SetNull(types.StringType),
		DohResolverIPs:          types.SetNull(types.StringType),
		LocalMcpApprovalConfig:  jsontypes.NewNormalizedNull(),
	})
	if body.UnapprovedAgentAction != nil || body.LocalMcpBlockEnabled != nil || body.UnnamedFlowBlockEnabled != nil ||
		body.UnnamedFlowExemptCidrs != nil || body.DohResolverHosts != nil || body.DohResolverIps != nil {
		t.Errorf("config omits all six, but some were sent: %+v", body)
	}
}

// An approval config that cannot be decoded at apply is an error, not a field
// silently left out of the request.
func TestEndpointSettings_ToWriteRejectsUndecodableApprovalConfig(t *testing.T) {
	plan := EndpointPolicySettingsResourceModel{LocalMcpApprovalConfig: jsontypes.NewNormalizedValue(`{"step_up_mode":1}`)}
	nullPostureSets(&plan)
	body, d := plan.toWrite(context.Background())
	if !d.HasError() {
		t.Fatal("want an error for an undecodable approval config")
	}
	if body.LocalMcpApprovalConfig != nil {
		t.Error("an undecodable config must not be sent")
	}
}

func TestReconcilePlatformForms_Sets(t *testing.T) {
	configured := strSet("DNS.Example.COM")
	normalized := strSet("dns.example.com")
	sentHosts := adminapi.EndpointPolicySettingsWrite{DohResolverHosts: &[]string{"DNS.Example.COM"}}
	recorded := func(v []string) platformForms {
		return platformForms{Recorded: true, Lists: map[string][]string{"doh_resolver_hosts": v}}
	}

	for name, tc := range map[string]struct {
		server   types.Set
		prior    types.Set
		sent     adminapi.EndpointPolicySettingsWrite
		recorded platformForms
		want     types.Set
		wantErr  string
	}{
		"sent: keep the configured spelling": {normalized, configured, sentHosts, platformForms{}, configured, ""},
		"sent but not stored: error": {types.SetNull(types.StringType), configured, sentHosts, platformForms{},
			types.SetNull(types.StringType), "did not store"},
		"sent, platform stored more: error": {strSet("a.example", "b.example"), configured, sentHosts,
			platformForms{}, strSet("a.example", "b.example"), "stored more"},
		"sent [] and cleared": {types.SetNull(types.StringType), strSet(),
			adminapi.EndpointPolicySettingsWrite{DohResolverHosts: &[]string{}}, platformForms{}, strSet(), ""},
		"not sent, unchanged": {normalized, configured, adminapi.EndpointPolicySettingsWrite{},
			recorded([]string{"dns.example.com"}), configured, ""},
		"not sent, changed elsewhere: drift": {strSet("dns.example.com", "doh.example.net"), configured,
			adminapi.EndpointPolicySettingsWrite{}, recorded([]string{"dns.example.com"}),
			strSet("dns.example.com", "doh.example.net"), ""},
		"nothing recorded (import)": {normalized, configured, adminapi.EndpointPolicySettingsWrite{},
			platformForms{}, normalized, ""},
		"prior null": {normalized, types.SetNull(types.StringType), adminapi.EndpointPolicySettingsWrite{},
			recorded([]string{"dns.example.com"}), normalized, ""},
	} {
		m := EndpointPolicySettingsResourceModel{LocalMcpApprovalConfig: jsontypes.NewNormalizedNull()}
		nullPostureSets(&m)
		m.DohResolverHosts = tc.server
		prior := EndpointPolicySettingsResourceModel{LocalMcpApprovalConfig: jsontypes.NewNormalizedNull()}
		nullPostureSets(&prior)
		prior.DohResolverHosts = tc.prior
		forms, d := reconcilePlatformForms(&m, prior, tc.sent, tc.recorded)
		if tc.wantErr == "" && d.HasError() {
			t.Errorf("%s: unexpected error %v", name, d)
		}
		if tc.wantErr != "" && (!d.HasError() || !strings.Contains(d.Errors()[0].Summary(), tc.wantErr)) {
			t.Errorf("%s: diags = %v, want an error containing %q", name, d, tc.wantErr)
		}
		if !m.DohResolverHosts.Equal(tc.want) {
			t.Errorf("%s: doh_resolver_hosts = %v, want %v", name, m.DohResolverHosts, tc.want)
		}
		if got, want := forms.Lists["doh_resolver_hosts"], setStrings(tc.server); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: recorded %v, want the platform's %v", name, got, want)
		}
	}
}

type fakePrivateState map[string][]byte

func (f fakePrivateState) GetKey(_ context.Context, k string) ([]byte, diag.Diagnostics) {
	return f[k], nil
}
func (f fakePrivateState) SetKey(_ context.Context, k string, v []byte) diag.Diagnostics {
	f[k] = v
	return nil
}

func TestPlatformFormsPrivateStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := fakePrivateState{}
	cfg := `{"step_up_mode":"approve"}`
	in := platformForms{Recorded: true, Lists: map[string][]string{"doh_resolver_hosts": {"dns.example.com"},
		"doh_resolver_ips": nil}, ApprovalConfig: &cfg}
	if d := storePlatformForms(ctx, p, in); d.HasError() {
		t.Fatal(d)
	}
	out, d := loadPlatformForms(ctx, p)
	if d.HasError() || !out.Recorded || out.Lists["doh_resolver_hosts"][0] != "dns.example.com" ||
		out.ApprovalConfig == nil || *out.ApprovalConfig != cfg {
		t.Errorf("round trip = %+v, %v", out, d)
	}
	if _, ok := out.Lists["doh_resolver_ips"]; !ok {
		t.Error("a recorded empty set must stay present, so an explicit [] keeps reconciling")
	}
	if out, _ := loadPlatformForms(ctx, fakePrivateState{}); out.Recorded {
		t.Error("absent private state must read as nothing recorded")
	}
	if out, _ := loadPlatformForms(ctx, fakePrivateState{platformFormsKey: []byte("not json")}); out.Recorded {
		t.Error("unreadable private state must read as nothing recorded")
	}
}

func TestNoZeroPrefix(t *testing.T) {
	for v, wantErr := range map[string]bool{"10.0.0.0/8": false, "192.0.2.1": false, "0.0.0.0/0": true, "::/0": true} {
		resp := &validator.StringResponse{}
		noZeroPrefix{}.ValidateString(context.Background(), validator.StringRequest{
			Path: path.Root("unnamed_flow_exempt_cidrs"), ConfigValue: types.StringValue(v),
		}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("%q: error = %v, want %v", v, resp.Diagnostics.HasError(), wantErr)
		}
	}
}

func TestApprovalConfigJSONValidator(t *testing.T) {
	sixteen := strings.Repeat(`{"type":"invocation_context"},`, 16)
	seventeen := sixteen + `{"type":"invocation_context"}`
	for name, tc := range map[string]struct {
		value   string
		wantErr string
	}{
		"valid":            {`{"step_up_mode":"approve","channels":[{"type":"invocation_context"}]}`, ""},
		"reauth too":       {`{"step_up_mode":"reauth","channels":[{"type":"email","addresses":["a@example.com"]}]}`, ""},
		"16 channels":      {`{"step_up_mode":"approve","channels":[` + strings.TrimSuffix(sixteen, ",") + `]}`, ""},
		"17 channels":      {`{"step_up_mode":"approve","channels":[` + seventeen + `]}`, "too many channels"},
		"not JSON":         {`approve`, "not a JSON object"},
		"empty object":     {`{}`, "empty approval config is refused"},
		"unknown key":      {`{"step_up_mode":"approve","channels":[{"type":"email"}],"chanels":[]}`, "does not match"},
		"unknown nested":   {`{"step_up_mode":"approve","channels":[{"type":"slack","chanel":"#x"}]}`, "does not match"},
		"miscased key":     {`{"Step_Up_Mode":"approve","channels":[{"type":"invocation_context"}]}`, "does not match"},
		"miscased nested":  {`{"step_up_mode":"approve","channels":[{"Type":"invocation_context"}]}`, "does not match"},
		"no step_up_mode":  {`{"channels":[{"type":"invocation_context"}]}`, "step_up_mode is required"},
		"bad step_up_mode": {`{"step_up_mode":"maybe","channels":[{"type":"invocation_context"}]}`, "step_up_mode is invalid"},
		"no channels":      {`{"step_up_mode":"approve"}`, "at least one channel"},
		"empty channels":   {`{"step_up_mode":"reauth","channels":[]}`, "at least one channel"},
	} {
		resp := &validator.StringResponse{}
		approvalConfigJSON{}.ValidateString(context.Background(), validator.StringRequest{
			Path: path.Root("local_mcp_approval_config"), ConfigValue: types.StringValue(tc.value),
		}, resp)
		if tc.wantErr == "" {
			if resp.Diagnostics.HasError() {
				t.Errorf("%s: unexpected error %v", name, resp.Diagnostics)
			}
			continue
		}
		found := false
		for _, e := range resp.Diagnostics.Errors() {
			if strings.Contains(e.Summary(), tc.wantErr) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: errors %v do not contain %q", name, resp.Diagnostics.Errors(), tc.wantErr)
		}
	}
}

// The platform's config reads back compact with sorted keys, as jsonencode
// writes it; jsontypes.Normalized then also ignores formatting differences.
func TestApprovalConfigToTF_MatchesJsonencodeForm(t *testing.T) {
	cfg, err := decodeApprovalConfig(`{"timeout_seconds":900,"step_up_mode":"approve",` +
		`"channels":[{"type":"email","addresses":["sec@example.com"]}],"approver_role":"Approver"}`)
	if err != nil {
		t.Fatal(err)
	}
	got := approvalConfigToTF(cfg).ValueString()
	want := `{"approver_role":"Approver","channels":[{"addresses":["sec@example.com"],"type":"email"}],` +
		`"step_up_mode":"approve","timeout_seconds":900}`
	if got != want {
		t.Errorf("approvalConfigToTF = %s\nwant              %s", got, want)
	}
	if !approvalConfigToTF(nil).IsNull() {
		t.Error("absent config must read as null")
	}
	pretty := jsontypes.NewNormalizedValue("{\n  \"step_up_mode\": \"approve\",\n  \"timeout_seconds\": 900\n}")
	compact := jsontypes.NewNormalizedValue(`{"timeout_seconds":900,"step_up_mode":"approve"}`)
	if eq, d := pretty.StringSemanticEquals(context.Background(), compact); d.HasError() || !eq {
		t.Errorf("reformatted JSON must be semantically equal: %v %v", eq, d)
	}
}

func TestReconcilePlatformForms_ApprovalConfig(t *testing.T) {
	configured := jsontypes.NewNormalizedValue(`{"channels":[{"type":"invocation_context"}],"step_up_mode":"approve"}`)
	serverForm := `{"channels":[{"type":"invocation_context"}],"step_up_mode":"approve","timeout_seconds":600}`
	cfg, _ := decodeApprovalConfig(configured.ValueString())
	sent := adminapi.EndpointPolicySettingsWrite{LocalMcpApprovalConfig: cfg}
	prior := EndpointPolicySettingsResourceModel{LocalMcpApprovalConfig: configured}
	nullPostureSets(&prior)
	run := func(server jsontypes.Normalized, sent adminapi.EndpointPolicySettingsWrite, rec platformForms) (EndpointPolicySettingsResourceModel, diag.Diagnostics) {
		m := EndpointPolicySettingsResourceModel{LocalMcpApprovalConfig: server}
		nullPostureSets(&m)
		_, d := reconcilePlatformForms(&m, prior, sent, rec)
		return m, d
	}

	if m, d := run(jsontypes.NewNormalizedValue(serverForm), sent, platformForms{}); d.HasError() || !m.LocalMcpApprovalConfig.Equal(configured) {
		t.Errorf("sent and stored: %v %v, want the configured JSON", m.LocalMcpApprovalConfig, d)
	}
	if _, d := run(jsontypes.NewNormalizedNull(), sent, platformForms{}); !d.HasError() {
		t.Error("sent but the platform has none: want an error")
	}
	rec := platformForms{Recorded: true, ApprovalConfig: &serverForm}
	if m, _ := run(jsontypes.NewNormalizedValue(serverForm), adminapi.EndpointPolicySettingsWrite{}, rec); !m.LocalMcpApprovalConfig.Equal(configured) {
		t.Errorf("unchanged: got %v, want the configured JSON", m.LocalMcpApprovalConfig)
	}
	other := `{"step_up_mode":"reauth"}`
	if m, _ := run(jsontypes.NewNormalizedValue(serverForm), adminapi.EndpointPolicySettingsWrite{},
		platformForms{Recorded: true, ApprovalConfig: &other}); m.LocalMcpApprovalConfig.ValueString() != serverForm {
		t.Errorf("changed elsewhere: got %v, want the platform's JSON", m.LocalMcpApprovalConfig)
	}
}
