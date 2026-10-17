package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

func strList(vals ...string) types.List {
	elems := make([]attr.Value, len(vals))
	for i, v := range vals {
		elems[i] = types.StringValue(v)
	}
	return types.ListValueMust(types.StringType, elems)
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
	if !m.UnnamedFlowExemptCIDRs.Equal(strList("10.0.0.0/8")) || !m.DohResolverHosts.Equal(strList("dns.example.com")) ||
		!m.DohResolverIPs.Equal(strList("2001:db8::1")) {
		t.Errorf("lists = %v / %v / %v", m.UnnamedFlowExemptCIDRs, m.DohResolverHosts, m.DohResolverIPs)
	}

	// The platform returns an empty list as absent.
	m = endpointSettingsToModel(fixtureTenantID, &adminapi.EndpointPolicySettings{})
	if !m.DohResolverHosts.IsNull() || !m.UnnamedFlowExemptCIDRs.IsNull() {
		t.Errorf("absent lists = %v / %v, want null", m.DohResolverHosts, m.UnnamedFlowExemptCIDRs)
	}
}

// Each field is sent only when configured; a configured [] is sent as [] (the
// platform's clear), never dropped.
func TestEndpointSettings_PostureFieldsSentOnlyWhenConfigured(t *testing.T) {
	plan := EndpointPolicySettingsResourceModel{
		UnapprovedAgentAction:   types.StringValue("block"),
		LocalMcpBlockEnabled:    types.BoolValue(false),
		UnnamedFlowBlockEnabled: types.BoolValue(true),
		UnnamedFlowExemptCIDRs:  strList(),
		DohResolverHosts:        strList("DNS.Example.COM"),
		DohResolverIPs:          strList("192.0.2.1"),
	}
	body := plan.toWrite()
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

	// On Update the plan carries state for an omitted attribute; omitUnconfigured
	// must drop all six when the config does not set them.
	omitUnconfigured(&body, EndpointPolicySettingsResourceModel{
		HookRuleEnforcement:     types.StringNull(),
		LocalMcpRiskPolicy:      types.ObjectNull(localMcpRiskPolicyAttrTypes),
		UnapprovedAgentAction:   types.StringNull(),
		LocalMcpBlockEnabled:    types.BoolNull(),
		UnnamedFlowBlockEnabled: types.BoolNull(),
		UnnamedFlowExemptCIDRs:  types.ListNull(types.StringType),
		DohResolverHosts:        types.ListNull(types.StringType),
		DohResolverIPs:          types.ListNull(types.StringType),
	})
	if body.UnapprovedAgentAction != nil || body.LocalMcpBlockEnabled != nil || body.UnnamedFlowBlockEnabled != nil ||
		body.UnnamedFlowExemptCidrs != nil || body.DohResolverHosts != nil || body.DohResolverIps != nil {
		t.Errorf("config omits all six, but some were sent: %+v", body)
	}
}

func TestReconcileNormalizedLists(t *testing.T) {
	configured := strList("DNS.Example.COM")
	normalized := strList("dns.example.com")
	sentHosts := adminapi.EndpointPolicySettingsWrite{DohResolverHosts: &[]string{"DNS.Example.COM"}}

	for name, tc := range map[string]struct {
		server   types.List
		prior    types.List
		sent     adminapi.EndpointPolicySettingsWrite
		recorded normalizedLists
		want     types.List
	}{
		"sent: keep the configured spelling": {
			normalized, configured, sentHosts, nil, configured},
		"not sent, unchanged since last time": {
			normalized, configured, adminapi.EndpointPolicySettingsWrite{},
			normalizedLists{"doh_resolver_hosts": {"dns.example.com"}}, configured},
		"not sent, changed elsewhere: drift": {
			strList("dns.example.com", "doh.example.net"), configured, adminapi.EndpointPolicySettingsWrite{},
			normalizedLists{"doh_resolver_hosts": {"dns.example.com"}}, strList("dns.example.com", "doh.example.net")},
		"nothing recorded (import): the platform's list": {
			normalized, configured, adminapi.EndpointPolicySettingsWrite{}, nil, normalized},
		"explicit [] stays [] while the platform has none": {
			types.ListNull(types.StringType), strList(), adminapi.EndpointPolicySettingsWrite{},
			normalizedLists{"doh_resolver_hosts": nil}, strList()},
		"prior null: the platform's list": {
			normalized, types.ListNull(types.StringType), adminapi.EndpointPolicySettingsWrite{},
			normalizedLists{"doh_resolver_hosts": {"dns.example.com"}}, normalized},
	} {
		m := EndpointPolicySettingsResourceModel{
			DohResolverHosts:       tc.server,
			DohResolverIPs:         types.ListNull(types.StringType),
			UnnamedFlowExemptCIDRs: types.ListNull(types.StringType),
		}
		prior := EndpointPolicySettingsResourceModel{
			DohResolverHosts:       tc.prior,
			DohResolverIPs:         types.ListNull(types.StringType),
			UnnamedFlowExemptCIDRs: types.ListNull(types.StringType),
		}
		stored := reconcileNormalizedLists(&m, prior, tc.sent, tc.recorded)
		if !m.DohResolverHosts.Equal(tc.want) {
			t.Errorf("%s: doh_resolver_hosts = %v, want %v", name, m.DohResolverHosts, tc.want)
		}
		// What is recorded is always the platform's list, never the configured one.
		if got, want := stored["doh_resolver_hosts"], listStrings(tc.server); len(got) != len(want) {
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

func TestNormalizedListsPrivateStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := fakePrivateState{}
	in := normalizedLists{"doh_resolver_hosts": {"dns.example.com"}, "doh_resolver_ips": nil}
	if d := storeNormalizedLists(ctx, p, in); d.HasError() {
		t.Fatal(d)
	}
	out, d := loadNormalizedLists(ctx, p)
	if d.HasError() || len(out["doh_resolver_hosts"]) != 1 || out["doh_resolver_hosts"][0] != "dns.example.com" {
		t.Errorf("round trip = %v, %v", out, d)
	}
	if _, ok := out["doh_resolver_ips"]; !ok {
		t.Error("a recorded empty list must stay present, so an explicit [] keeps reconciling")
	}
	// Absent or unreadable private state reads as nothing recorded.
	if out, _ := loadNormalizedLists(ctx, fakePrivateState{}); out != nil {
		t.Errorf("absent = %v, want nil", out)
	}
	if out, _ := loadNormalizedLists(ctx, fakePrivateState{normalizedListsKey: []byte("not json")}); out != nil {
		t.Errorf("unreadable = %v, want nil", out)
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
	for name, tc := range map[string]struct {
		value   string
		wantErr string
	}{
		"valid":            {`{"step_up_mode":"approve","channels":[{"type":"invocation_context"}]}`, ""},
		"not JSON":         {`approve`, "not a JSON object"},
		"empty object":     {`{}`, "empty approval config is refused"},
		"unknown key":      {`{"step_up_mode":"approve","channels":[{"type":"email"}],"chanels":[]}`, "does not match"},
		"unknown nested":   {`{"step_up_mode":"approve","channels":[{"type":"slack","chanel":"#x"}]}`, "does not match"},
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
		if !resp.Diagnostics.HasError() {
			t.Errorf("%s: want an error containing %q, got none", name, tc.wantErr)
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

// The platform's config reads back as compact JSON with sorted keys, which is
// what jsonencode writes, so an import of an unchanged config compares equal.
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
}

func TestEndpointSettings_ApprovalConfigWriteAndReconcile(t *testing.T) {
	configured := types.StringValue(`{"channels":[{"type":"invocation_context"}],"step_up_mode":"approve"}`)
	plan := EndpointPolicySettingsResourceModel{LocalMcpApprovalConfig: configured}
	body := plan.toWrite()
	if body.LocalMcpApprovalConfig == nil || body.LocalMcpApprovalConfig.StepUpMode != "approve" {
		t.Fatalf("config not sent: %+v", body.LocalMcpApprovalConfig)
	}
	omitUnconfigured(&body, EndpointPolicySettingsResourceModel{
		HookRuleEnforcement: types.StringNull(), LocalMcpRiskPolicy: types.ObjectNull(localMcpRiskPolicyAttrTypes),
		UnapprovedAgentAction: types.StringNull(), LocalMcpBlockEnabled: types.BoolNull(),
		UnnamedFlowBlockEnabled: types.BoolNull(), UnnamedFlowExemptCIDRs: types.ListNull(types.StringType),
		DohResolverHosts: types.ListNull(types.StringType), DohResolverIPs: types.ListNull(types.StringType),
		LocalMcpApprovalConfig: types.StringNull(),
	})
	if body.LocalMcpApprovalConfig != nil {
		t.Error("config omitted from config but still sent")
	}

	nulls := func(m *EndpointPolicySettingsResourceModel) {
		m.DohResolverHosts, m.DohResolverIPs, m.UnnamedFlowExemptCIDRs =
			types.ListNull(types.StringType), types.ListNull(types.StringType), types.ListNull(types.StringType)
	}
	serverForm := `{"channels":[{"type":"invocation_context"}],"step_up_mode":"approve","timeout_seconds":600}`
	prior := EndpointPolicySettingsResourceModel{LocalMcpApprovalConfig: configured}
	nulls(&prior)

	// Not sent, platform unchanged since last time: keep the configured JSON.
	m := EndpointPolicySettingsResourceModel{LocalMcpApprovalConfig: types.StringValue(serverForm)}
	nulls(&m)
	reconcileNormalizedLists(&m, prior, adminapi.EndpointPolicySettingsWrite{},
		normalizedLists{approvalConfigKey: {serverForm}})
	if !m.LocalMcpApprovalConfig.Equal(configured) {
		t.Errorf("unchanged: got %v, want the configured JSON", m.LocalMcpApprovalConfig)
	}
	// Changed elsewhere: the platform's JSON, so the plan shows drift.
	m = EndpointPolicySettingsResourceModel{LocalMcpApprovalConfig: types.StringValue(serverForm)}
	nulls(&m)
	reconcileNormalizedLists(&m, prior, adminapi.EndpointPolicySettingsWrite{},
		normalizedLists{approvalConfigKey: {`{"step_up_mode":"reauth"}`}})
	if m.LocalMcpApprovalConfig.ValueString() != serverForm {
		t.Errorf("changed elsewhere: got %v, want the platform's JSON", m.LocalMcpApprovalConfig)
	}
}
