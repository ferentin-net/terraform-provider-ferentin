package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

const scopeTestGroupID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func scopeDefaultsSchema(t *testing.T) rschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	(&MCPScopeDefaultsResource{}).Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func tfStr(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

var tfNullStr = tftypes.NewValue(tftypes.String, nil)

// ----------------------------------------------------------------- the retry

func TestRetryStalePostureVersion(t *testing.T) {
	stale := fmt.Errorf("put: %w", adminapi.ErrPreconditionFailed)
	for name, tc := range map[string]struct {
		version    string
		firstErr   error
		changed    []string
		rereadErr  error
		wantWrites []string
		wantErr    func(error) bool
	}{
		"success": {version: "7", wantWrites: []string{"7"}, wantErr: func(e error) bool { return e == nil }},
		"stale for another column: retried with the fresh version": {version: "7", firstErr: stale,
			wantWrites: []string{"7", "8"}, wantErr: func(e error) bool { return e == nil }},
		"owned field changed: conflict, no retry": {version: "7", firstErr: stale, changed: []string{"server_approval"},
			wantWrites: []string{"7"}, wantErr: func(e error) bool {
				var c *postureConflictError
				return errors.As(e, &c) && errors.Is(e, adminapi.ErrPreconditionFailed) &&
					strings.Contains(e.Error(), "server_approval")
			}},
		"no If-Match sent: a 412 is returned as is": {version: "", firstErr: stale, wantWrites: []string{""},
			wantErr: func(e error) bool { return errors.Is(e, adminapi.ErrPreconditionFailed) }},
		"another error: not retried": {version: "7", firstErr: adminapi.ErrConflict, wantWrites: []string{"7"},
			wantErr: func(e error) bool { return errors.Is(e, adminapi.ErrConflict) }},
		"re-read fails: the 412 is kept": {version: "7", firstErr: stale, rereadErr: errors.New("boom"),
			wantWrites: []string{"7"}, wantErr: func(e error) bool {
				return errors.Is(e, adminapi.ErrPreconditionFailed) && strings.Contains(e.Error(), "boom")
			}},
	} {
		var writes []string
		_, err := retryStalePostureVersion(tc.version,
			func(v string) (int, error) {
				writes = append(writes, v)
				if len(writes) == 1 && tc.firstErr != nil {
					return 0, tc.firstErr
				}
				return 1, nil
			},
			func() (string, []string, error) { return "8", tc.changed, tc.rereadErr })
		if !tc.wantErr(err) {
			t.Errorf("%s: err = %v", name, err)
		}
		if strings.Join(writes, ",") != strings.Join(tc.wantWrites, ",") {
			t.Errorf("%s: writes with versions %v, want %v", name, writes, tc.wantWrites)
		}
	}
}

// fakeScopeDefaultsServer answers GET with fresh and a PUT with 412 unless its
// If-Match names fresh's version.
func fakeScopeDefaultsServer(t *testing.T, fresh string) (*adminapi.SDKClient, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var puts []string
	var stored map[string]any
	_ = json.Unmarshal([]byte(fresh), &stored)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			puts = append(puts, r.Header.Get("If-Match"))
			if r.Header.Get("If-Match") != fmt.Sprintf(`W/"%v"`, stored["version"]) {
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = w.Write([]byte(`{"error":"precondition_failed"}`))
				return
			}
			_, _ = io.Copy(io.Discard, r.Body)
		}
		_, _ = w.Write([]byte(fresh))
	}))
	t.Cleanup(srv.Close)
	sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: srv.URL, Token: "test"})
	if err != nil {
		t.Fatalf("sdk: %v", err)
	}
	return sdk, &puts
}

func scopeState(serverApproval, mode string, version int64) MCPScopeDefaultsResourceModel {
	m := scopeDefaultsToModel(fixtureTenantID, types.StringNull(), &adminapi.MCPScopeDefaults{})
	m.ServerApproval, m.ServerApprovalMode = types.StringValue(serverApproval), types.StringValue(mode)
	m.Version = types.Int64Value(version)
	return m
}

func serverApprovalWrite(value, mode string) adminapi.MCPScopeDefaultsWrite {
	v, md := gen.McpScopeDefaultValue(value), gen.McpScopeDefaultMode(mode)
	return adminapi.MCPScopeDefaultsWrite{ServerApproval: &adminapi.MCPScopeDefault{Value: &v, Mode: &md}}
}

// ferentin_endpoint_policy_settings bumped the row's version in the same apply;
// the defaults this write sends are as last read, so it retries.
func TestMCPScopeDefaultsUpdate_RetriesAVersionMovedByTheSettingsResource(t *testing.T) {
	sdk, puts := fakeScopeDefaultsServer(t, `{"source":"tenant","version":8,
	  "server_approval":{"value":"quarantine","mode":"enforce"},"tool_vetting":{"mode":"enforce","mode_source":"tenant"}}`)
	r := &MCPScopeDefaultsResource{sdk: sdk, tenantID: fixtureTenantID}
	_, err := r.update(context.Background(), fixtureTenantID, scopeState("quarantine", "enforce", 7),
		serverApprovalWrite("block", "enforce"), nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if strings.Join(*puts, ",") != `W/"7",W/"8"` {
		t.Errorf("PUTs with If-Match %v, want the stale version then the fresh one", *puts)
	}
}

// Someone changed server_approval since the last refresh: overwriting it is
// exactly what If-Match is for, so the 412 stands.
func TestMCPScopeDefaultsUpdate_ConflictWhenAnOwnedDefaultChanged(t *testing.T) {
	sdk, puts := fakeScopeDefaultsServer(t, `{"source":"tenant","version":8,
	  "server_approval":{"value":"report_only","mode":"report_only"}}`)
	r := &MCPScopeDefaultsResource{sdk: sdk, tenantID: fixtureTenantID}
	_, err := r.update(context.Background(), fixtureTenantID, scopeState("quarantine", "enforce", 7),
		serverApprovalWrite("block", "enforce"), nil)
	var conflict *postureConflictError
	if !errors.As(err, &conflict) || !strings.Contains(err.Error(), "server_approval") {
		t.Fatalf("err = %v, want a conflict naming server_approval", err)
	}
	if len(*puts) != 1 {
		t.Errorf("PUTs = %v, want no retry", *puts)
	}
}

// The other direction: ferentin_mcp_scope_defaults moved the version, and the
// settings resource's own fields are as last read.
func TestEndpointSettingsUpdate_RetriesAVersionMovedByScopeDefaults(t *testing.T) {
	var puts []string
	row := `{"id":"33333333-3333-4333-8333-333333333333","defaultDestinationAction":"allow",
	  "echStripEnabled":false,"dohBlockEnabled":false,"quicBlockEnabled":true,"version":8}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			puts = append(puts, r.Header.Get("If-Match"))
			if r.Header.Get("If-Match") != `W/"8"` {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			_, _ = w.Write([]byte(row))
			return
		}
		_, _ = w.Write([]byte("[" + row + "]"))
	}))
	defer srv.Close()
	sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: srv.URL, Token: "test"})
	if err != nil {
		t.Fatalf("sdk: %v", err)
	}
	r := &EndpointPolicySettingsResource{sdk: sdk, tenantID: fixtureTenantID}

	state := EndpointPolicySettingsResourceModel{
		DeviceGroupID:            types.StringNull(),
		DefaultDestinationAction: types.StringValue("allow"),
		EchStripEnabled:          types.BoolValue(false),
		DohBlockEnabled:          types.BoolValue(false),
		QuicBlockEnabled:         types.BoolValue(true),
		McpGatewayURL:            types.StringNull(),
		Version:                  types.Int64Value(7),
	}
	nullPostureSets(&state)
	plan := state
	plan.QuicBlockEnabled = types.BoolValue(false)
	body, _ := plan.toWrite(context.Background())

	run := func(st EndpointPolicySettingsResourceModel) error {
		puts = nil
		_, err := retryStalePostureVersion("7",
			func(v string) (*adminapi.EndpointPolicySettings, error) {
				return r.upsert(context.Background(), fixtureTenantID, st.DeviceGroupID, v, body)
			},
			r.rereadOwned(context.Background(), fixtureTenantID, st, platformForms{}, settingsSentFields(body)))
		return err
	}
	if err := run(state); err != nil {
		t.Fatalf("update: %v", err)
	}
	if strings.Join(puts, ",") != `W/"7",W/"8"` {
		t.Errorf("PUTs with If-Match %v, want the stale version then the fresh one", puts)
	}

	// default_destination_action was changed elsewhere (state says block).
	changed := state
	changed.DefaultDestinationAction = types.StringValue("block")
	var conflict *postureConflictError
	if err := run(changed); !errors.As(err, &conflict) || !strings.Contains(err.Error(), "default_destination_action") {
		t.Errorf("err = %v, want a conflict naming default_destination_action", err)
	}
}

func TestSettingsSentFields_NamesWhatTheBodyWrites(t *testing.T) {
	agent := "block"
	got := settingsSentFields(adminapi.EndpointPolicySettingsWrite{UnapprovedAgentAction: &agent, DohResolverHosts: &[]string{}})
	if strings.Join(got, ",") != "unapproved_agent_action,doh_resolver_hosts" {
		t.Errorf("sent fields = %v", got)
	}
	for _, name := range settingsOwnedAttrs {
		if settingsAttrValue(EndpointPolicySettingsResourceModel{}, name) == nil {
			t.Errorf("settingsAttrValue has no case for %s", name)
		}
	}
}

// ----------------------------------------------------------------- reading

func TestScopeDefaultsToModel_TenantAndGroup(t *testing.T) {
	low, med, high := gen.LocalMcpRiskPolicyLow("allow"), gen.LocalMcpRiskPolicyMedium("ask_once"), gen.LocalMcpRiskPolicyHigh("deny")
	risk := &gen.LocalMcpRiskPolicy{Low: &low, Medium: &med, High: &high}
	src := func(s string) *gen.McpScopeToolVettingModeSource {
		v := gen.McpScopeToolVettingModeSource(s)
		return &v
	}
	rsrc := func(s string) *gen.McpScopeToolVettingRiskPolicySource {
		v := gen.McpScopeToolVettingRiskPolicySource(s)
		return &v
	}
	mode := gen.McpScopeToolVettingMode("report_only")
	value, md := gen.McpScopeDefaultValue("block"), gen.McpScopeDefaultMode("report_only")
	resp := func(modeSource, riskSource string) *adminapi.MCPScopeDefaults {
		return &adminapi.MCPScopeDefaults{
			ServerApproval: &adminapi.MCPScopeDefault{Value: &value, Mode: &md},
			RemoteEntry:    &adminapi.MCPScopeDefault{Value: &value, Mode: &md},
			Version:        int64Ptr(4),
			ToolVetting: &adminapi.MCPScopeToolVetting{Mode: &mode, ModeSource: src(modeSource),
				RiskPolicy: risk, RiskPolicySource: rsrc(riskSource)},
		}
	}

	m := scopeDefaultsToModel(fixtureTenantID, types.StringNull(), resp("tenant", "tenant"))
	if m.ID.ValueString() != fixtureTenantID || !m.DeviceGroupID.IsNull() {
		t.Errorf("tenant id = %v / %v", m.ID, m.DeviceGroupID)
	}
	if m.ServerApproval.ValueString() != "block" || m.ServerApprovalMode.ValueString() != "report_only" ||
		m.RemoteEntry.ValueString() != "block" || m.Version.ValueInt64() != 4 {
		t.Errorf("pairs = %v/%v remote %v version %v", m.ServerApproval, m.ServerApprovalMode, m.RemoteEntry, m.Version)
	}
	if m.ToolVettingMode.ValueString() != "report_only" || m.ToolVettingRiskPolicy.IsNull() {
		t.Errorf("tenant vetting = %v / %v, want its own values", m.ToolVettingMode, m.ToolVettingRiskPolicy)
	}

	group := types.StringValue(scopeTestGroupID)
	m = scopeDefaultsToModel(fixtureTenantID, group, resp("tenant", "tenant"))
	if m.ID.ValueString() != fixtureTenantID+"/"+scopeTestGroupID {
		t.Errorf("group id = %v", m.ID)
	}
	if m.ToolVettingMode.ValueString() != "inherit" || !m.ToolVettingRiskPolicy.IsNull() {
		t.Errorf("group following the tenant: vetting = %v / %v, want inherit / null", m.ToolVettingMode, m.ToolVettingRiskPolicy)
	}
	if m.ToolVettingModeSource.ValueString() != "tenant" {
		t.Errorf("mode source = %v", m.ToolVettingModeSource)
	}
	m = scopeDefaultsToModel(fixtureTenantID, group, resp("device_group", "device_group"))
	if m.ToolVettingMode.ValueString() != "report_only" || m.ToolVettingRiskPolicy.IsNull() {
		t.Errorf("group's own: vetting = %v / %v", m.ToolVettingMode, m.ToolVettingRiskPolicy)
	}
	if !scopeDefaultsToModel(fixtureTenantID, types.StringNull(), resp("built_in", "built_in")).ToolVettingRiskPolicy.IsNull() {
		t.Error("a built-in risk policy is not the tenant's own")
	}
}

func TestScopeDefaultsToModel_AgentEvidenceFloor(t *testing.T) {
	resp := func(value, source string) *adminapi.MCPScopeDefaults {
		v := gen.McpScopeAgentEvidenceFloorValue(value)
		s := gen.McpScopeAgentEvidenceFloorSource(source)
		return &adminapi.MCPScopeDefaults{AgentEvidenceFloor: &gen.McpScopeAgentEvidenceFloor{Value: &v, Source: &s}}
	}
	group := types.StringValue(scopeTestGroupID)
	for name, tc := range map[string]struct {
		groupID             types.String
		value, source, want string
	}{
		"tenant, its own":                {types.StringNull(), "lineage_signature", "tenant", "lineage_signature"},
		"tenant, none set reads off":     {types.StringNull(), "off", "built_in", "off"},
		"group, its own":                 {group, "lineage_name", "device_group", "lineage_name"},
		"group's own off is not inherit": {group, "off", "device_group", "off"},
		"group following the tenant":     {group, "lineage_signature", "tenant", "inherit"},
		"group, nothing set anywhere":    {group, "off", "built_in", "inherit"},
	} {
		m := scopeDefaultsToModel(fixtureTenantID, tc.groupID, resp(tc.value, tc.source))
		if m.AgentEvidenceFloor.ValueString() != tc.want || m.AgentEvidenceFloorSource.ValueString() != tc.source {
			t.Errorf("%s: floor = %v (source %v), want %s (%s)", name, m.AgentEvidenceFloor,
				m.AgentEvidenceFloorSource, tc.want, tc.source)
		}
	}
	if m := scopeDefaultsToModel(fixtureTenantID, types.StringNull(), &adminapi.MCPScopeDefaults{}); !m.AgentEvidenceFloor.IsNull() ||
		!m.AgentEvidenceFloorSource.IsNull() {
		t.Errorf("a platform that predates the floor: %v / %v, want null", m.AgentEvidenceFloor, m.AgentEvidenceFloorSource)
	}
}

// ----------------------------------------------------------------- writing

func TestScopeDefaultsToWrite_SendsOnlyConfigured(t *testing.T) {
	risk := types.ObjectValueMust(localMcpRiskPolicyAttrTypes, map[string]attr.Value{
		"low": types.StringValue("allow"), "medium": types.StringValue("deny"), "high": types.StringValue("deny"),
	})
	plan := MCPScopeDefaultsResourceModel{
		ServerApproval: types.StringValue("block"), ServerApprovalMode: types.StringValue("report_only"),
		Unattributed: types.StringValue("block"), UnattributedMode: types.StringValue("enforce"),
		ToolVettingMode: types.StringValue("inherit"), ToolVettingRiskPolicy: risk,
		ToolVettingApprovalConfig: jsontypes.NewNormalizedValue(`{"step_up_mode":"approve","channels":[{"type":"invocation_context"}]}`),
		AgentEvidenceFloor:        types.StringValue("lineage_signature"),
	}

	// Nothing configured: the plan carries state values (UseStateForUnknown),
	// and none is sent.
	nothing := MCPScopeDefaultsResourceModel{
		ServerApproval: types.StringNull(), Unattributed: types.StringNull(), ToolVettingMode: types.StringNull(),
		ToolVettingRiskPolicy:     types.ObjectNull(localMcpRiskPolicyAttrTypes),
		ToolVettingApprovalConfig: jsontypes.NewNormalizedNull(),
	}
	body, d := scopeDefaultsToWrite(plan, nothing)
	if d.HasError() || body.ServerApproval != nil || body.Unattributed != nil || body.ToolVetting != nil || body.RemoteEntry != nil ||
		body.AgentEvidenceFloor != nil {
		t.Errorf("config sets nothing, sent %+v (%v)", body, d)
	}

	body, d = scopeDefaultsToWrite(plan, plan)
	if d.HasError() {
		t.Fatal(d)
	}
	if body.ServerApproval == nil || *body.ServerApproval.Value != "block" || *body.ServerApproval.Mode != "report_only" {
		t.Errorf("server_approval = %+v", body.ServerApproval)
	}
	if body.RemoteEntry != nil {
		t.Error("remote_entry is read-only and must never be sent")
	}
	if body.Unattributed == nil || *body.Unattributed.Mode != "enforce" {
		t.Errorf("unattributed = %+v", body.Unattributed)
	}
	tv := body.ToolVetting
	if tv == nil || tv.Mode == nil || *tv.Mode != "inherit" || tv.RiskPolicy == nil || *tv.RiskPolicy.Medium != "deny" ||
		tv.ApprovalConfig == nil || tv.ApprovalConfig.StepUpMode != "approve" {
		t.Errorf("tool_vetting = %+v", tv)
	}
	if body.AgentEvidenceFloor == nil || *body.AgentEvidenceFloor != "lineage_signature" {
		t.Errorf("agent_evidence_floor = %v", body.AgentEvidenceFloor)
	}

	// A configured value with an unknown planned mode still sends the
	// platform's rule, never an empty mode.
	plan.ServerApprovalMode = types.StringUnknown()
	body, _ = scopeDefaultsToWrite(plan, plan)
	if *body.ServerApproval.Mode != "enforce" {
		t.Errorf("derived mode = %s, want enforce", *body.ServerApproval.Mode)
	}
}

func TestScopeDefaultsToWrite_RejectsUndecodableApprovalConfig(t *testing.T) {
	plan := MCPScopeDefaultsResourceModel{
		ServerApproval: types.StringNull(), Unattributed: types.StringNull(), ToolVettingMode: types.StringNull(),
		ToolVettingRiskPolicy:     types.ObjectNull(localMcpRiskPolicyAttrTypes),
		ToolVettingApprovalConfig: jsontypes.NewNormalizedValue(`{"step_up_mode":1}`),
	}
	body, d := scopeDefaultsToWrite(plan, plan)
	if !d.HasError() {
		t.Fatal("want an error for an undecodable approval config")
	}
	if body.ToolVetting != nil {
		t.Error("an undecodable config must not be sent")
	}
}

// ----------------------------------------------------------------- planning

func TestKeepOrDerivedMode(t *testing.T) {
	ctx := context.Background()
	s := scopeDefaultsSchema(t)
	for name, tc := range map[string]struct {
		configValue, configMode, stateMode tftypes.Value
		noState                            bool
		want                               types.String
	}{
		"mode configured: as written":           {tfStr("block"), tfStr("report_only"), tfStr("enforce"), false, types.StringValue("report_only")},
		"refusing value alone: enforce":         {tfStr("block"), tfNullStr, tfStr("report_only"), false, types.StringValue("enforce")},
		"report_only alone: report_only":        {tfStr("report_only"), tfNullStr, tfStr("enforce"), false, types.StringValue("report_only")},
		"value not configured: state kept":      {tfNullStr, tfNullStr, tfStr("report_only"), false, types.StringValue("report_only")},
		"value not configured, create: unknown": {tfNullStr, tfNullStr, tfNullStr, true, types.StringUnknown()},
		"value unknown: unknown":                {tftypes.NewValue(tftypes.String, tftypes.UnknownValue), tfNullStr, tfStr("enforce"), false, types.StringUnknown()},
	} {
		cfg := tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
			"server_approval": tc.configValue, "server_approval_mode": tc.configMode,
		})}
		plan := tfsdk.Plan{Schema: s, Raw: endpointRuleValue(t, s, nil)}
		state := tfsdk.State{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{"server_approval_mode": tc.stateMode})}
		stateValue := types.StringNull()
		if tc.noState {
			state = tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
		} else if !tc.stateMode.IsNull() {
			var v string
			_ = tc.stateMode.As(&v)
			stateValue = types.StringValue(v)
		}
		var configMode types.String
		_ = cfg.GetAttribute(ctx, path.Root("server_approval_mode"), &configMode)
		req := planmodifier.StringRequest{
			Path: path.Root("server_approval_mode"), Config: cfg, Plan: plan, State: state,
			ConfigValue: configMode, StateValue: stateValue, PlanValue: types.StringUnknown(),
		}
		if !configMode.IsNull() {
			req.PlanValue = configMode
		}
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		keepOrDerivedMode{valueAttr: "server_approval"}.PlanModifyString(ctx, req, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", name, resp.Diagnostics)
		}
		if !resp.PlanValue.Equal(tc.want) {
			t.Errorf("%s: planned mode = %v, want %v", name, resp.PlanValue, tc.want)
		}
	}
}

func TestMCPScopeDefaults_ValidateConfig(t *testing.T) {
	ctx := context.Background()
	s := scopeDefaultsSchema(t)
	for name, tc := range map[string]struct {
		attrs   map[string]tftypes.Value
		wantErr string
	}{
		"rehearse block":               {map[string]tftypes.Value{"server_approval": tfStr("block"), "server_approval_mode": tfStr("report_only")}, ""},
		"report_only enforced":         {map[string]tftypes.Value{"unattributed": tfStr("report_only"), "unattributed_mode": tfStr("enforce")}, "cannot be enforced"},
		"inherit on a group":           {map[string]tftypes.Value{"device_group_id": tfStr(scopeTestGroupID), "tool_vetting_mode": tfStr("inherit")}, ""},
		"inherit on the tenant":        {map[string]tftypes.Value{"tool_vetting_mode": tfStr("inherit")}, "device group only"},
		"inherit, group not yet known": {map[string]tftypes.Value{"device_group_id": tftypes.NewValue(tftypes.String, tftypes.UnknownValue), "tool_vetting_mode": tfStr("inherit")}, ""},
		"floor inherit on a group":     {map[string]tftypes.Value{"device_group_id": tfStr(scopeTestGroupID), "agent_evidence_floor": tfStr("inherit")}, ""},
		"floor inherit on the tenant":  {map[string]tftypes.Value{"agent_evidence_floor": tfStr("inherit")}, "device group only"},
		"floor level on the tenant":    {map[string]tftypes.Value{"agent_evidence_floor": tfStr("lineage_signature")}, ""},
	} {
		resp := &resource.ValidateConfigResponse{}
		(&MCPScopeDefaultsResource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{
			Config: tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, tc.attrs)},
		}, resp)
		if tc.wantErr == "" {
			if resp.Diagnostics.HasError() {
				t.Errorf("%s: unexpected error %v", name, resp.Diagnostics)
			}
			continue
		}
		if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), tc.wantErr) {
			t.Errorf("%s: diags = %v, want an error containing %q", name, resp.Diagnostics, tc.wantErr)
		}
	}
}

// ----------------------------------------------------------------- approval config

func TestReconcileScopeApprovalConfig(t *testing.T) {
	configured := jsontypes.NewNormalizedValue(`{"channels":[{"type":"invocation_context"}],"step_up_mode":"approve"}`)
	serverForm := `{"channels":[{"type":"invocation_context"}],"step_up_mode":"approve","timeout_seconds":600}`
	prior := MCPScopeDefaultsResourceModel{ToolVettingApprovalConfig: configured}
	run := func(server jsontypes.Normalized, sent bool, rec *string) (MCPScopeDefaultsResourceModel, *string) {
		m := MCPScopeDefaultsResourceModel{ToolVettingApprovalConfig: server}
		form := reconcileScopeApprovalConfig(&m, prior, sent, rec)
		return m, form
	}
	if m, form := run(jsontypes.NewNormalizedValue(serverForm), true, nil); !m.ToolVettingApprovalConfig.Equal(configured) ||
		form == nil || *form != serverForm {
		t.Errorf("sent and stored: %v (form %v), want the configured JSON and the platform's form", m.ToolVettingApprovalConfig, form)
	}
	if m, _ := run(jsontypes.NewNormalizedValue(serverForm), false, &serverForm); !m.ToolVettingApprovalConfig.Equal(configured) {
		t.Errorf("unchanged: got %v, want the configured JSON", m.ToolVettingApprovalConfig)
	}
	other := `{"step_up_mode":"reauth"}`
	if m, _ := run(jsontypes.NewNormalizedValue(serverForm), false, &other); m.ToolVettingApprovalConfig.ValueString() != serverForm {
		t.Errorf("changed elsewhere: got %v, want the platform's JSON", m.ToolVettingApprovalConfig)
	}
	if m, _ := run(jsontypes.NewNormalizedValue(serverForm), false, nil); m.ToolVettingApprovalConfig.ValueString() != serverForm {
		t.Errorf("nothing recorded (import): got %v, want the platform's JSON", m.ToolVettingApprovalConfig)
	}
	if m, form := run(jsontypes.NewNormalizedNull(), false, &serverForm); !m.ToolVettingApprovalConfig.IsNull() || form != nil {
		t.Errorf("cleared in the console: got %v (form %v), want null", m.ToolVettingApprovalConfig, form)
	}
}

func TestApprovalFormPrivateStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := fakePrivateState{}
	cfg := `{"step_up_mode":"approve"}`
	if d := storeApprovalForm(ctx, p, &cfg); d.HasError() {
		t.Fatal(d)
	}
	if got, d := loadApprovalForm(ctx, p); d.HasError() || got == nil || *got != cfg {
		t.Errorf("round trip = %v, %v", got, d)
	}
	if got, _ := loadApprovalForm(ctx, fakePrivateState{scopeApprovalFormKey: []byte("not json")}); got != nil {
		t.Error("unreadable private state must read as nothing recorded")
	}
}

func TestScopeDefaultsOwnedChanges(t *testing.T) {
	prior := scopeState("quarantine", "enforce", 7)
	fresh := prior
	fresh.Unattributed = types.StringValue("block")
	if got := scopeDefaultsOwnedChanges(prior, fresh, serverApprovalWrite("block", "enforce")); len(got) != 0 {
		t.Errorf("a default this write does not send changed: got %v, want nothing", got)
	}
	fresh.ServerApprovalMode = types.StringValue("report_only")
	if got := scopeDefaultsOwnedChanges(prior, fresh, serverApprovalWrite("block", "enforce")); strings.Join(got, ",") != "server_approval_mode" {
		t.Errorf("changed = %v, want server_approval_mode", got)
	}

	floor := gen.McpScopeDefaultsWriteAgentEvidenceFloor("lineage_signature")
	prior.AgentEvidenceFloor = types.StringValue("off")
	fresh = prior
	fresh.AgentEvidenceFloor = types.StringValue("lineage_name")
	if got := scopeDefaultsOwnedChanges(prior, fresh, serverApprovalWrite("block", "enforce")); len(got) != 0 {
		t.Errorf("a floor this write does not send changed: got %v, want nothing", got)
	}
	if got := scopeDefaultsOwnedChanges(prior, fresh, adminapi.MCPScopeDefaultsWrite{AgentEvidenceFloor: &floor}); strings.Join(got, ",") != "agent_evidence_floor" {
		t.Errorf("changed = %v, want agent_evidence_floor", got)
	}
}

func TestMCPScopeDefaults_ImportState(t *testing.T) {
	ctx := context.Background()
	s := scopeDefaultsSchema(t)
	for id, wantErr := range map[string]bool{
		fixtureTenantID:                          false,
		fixtureTenantID + "/" + scopeTestGroupID: false,
		fixtureTenantID + "/not-a-uuid":          true,
		fixtureTenantID + "/a/b":                 true,
		"/" + scopeTestGroupID:                   true,
	} {
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
		(&MCPScopeDefaultsResource{}).ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("%q: diags = %v, want error = %v", id, resp.Diagnostics, wantErr)
		}
	}
}

// A 412 whose re-read finds the row gone must still read as ErrNotFound, or a
// destroy of a group row deleted elsewhere fails instead of succeeding.
func TestRetryStalePostureVersion_RereadNotFound(t *testing.T) {
	_, err := retryStalePostureVersion("3",
		func(string) (struct{}, error) { return struct{}{}, adminapi.ErrPreconditionFailed },
		func() (string, []string, error) { return "", nil, adminapi.ErrNotFound })
	if !errors.Is(err, adminapi.ErrNotFound) || !errors.Is(err, adminapi.ErrPreconditionFailed) {
		t.Errorf("err = %v, want both ErrNotFound and ErrPreconditionFailed", err)
	}
}
