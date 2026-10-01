package provider

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// Round-trip test for the data-protection policy SDK-response -> model mapper,
// following the phase3 pattern. One fully-populated fixture; asserts the
// scalar fields plus the map / JSON conversions unique to this entity.
func TestDataProtectionPolicyToModel_RoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	polID := mustParseUUID(t, "33333333-3333-4333-8333-333333333333")

	pol := &adminapi.DataProtectionPolicy{
		Id:                 &polID,
		TenantId:           strPtr(fixtureTenantID),
		Name:               strPtr("prod-pii-and-exfil"),
		Description:        strPtr("Tokenize US PII, log exfil URLs"),
		Priority:           int32Ptr(100),
		Enabled:            boolPtr(true),
		EnabledProfiles:    &[]string{"US_PII", "EXFILTRATION_DEFENSE"},
		EnabledDetectors:   &map[string]bool{"DATABASE_URL": true},
		DisabledDetectors:  &map[string]bool{"EU_VAT": true},
		DetectorThresholds: &map[string]float64{"US_SSN": 0.95},
		DetectorConfigs:    &map[string]map[string]interface{}{"EXFILTRATION_URL": {"minConfidenceScore": 0.5}},
		Effects:            &map[string]string{"US_SSN": "tokenize", "EXFILTRATION_URL": "log"},
		DefaultEffect:      strPtr("redact"),
		BlockedMessage:     strPtr("blocked by policy"),
		FpeKeyId:           strPtr("dlp-fpe-2026"),
		TweakScope:         strPtr("conversation"),
		ApplyToLlmInput:    boolPtr(true),
		ApplyToLlmOutput:   boolPtr(true),
		ApplyToMcpInput:    boolPtr(false),
		ApplyToMcpOutput:   boolPtr(true),
		// Endpoint scopes (ferentin-platform#2872).
		ApplyToToolInput:          boolPtr(true),
		ApplyToPromptInput:        boolPtr(false),
		ApplyToToolOutput:         boolPtr(true),
		ToolOutputUnscannedAction: strPtr("withhold"),
		DeviceGroupIds:            &[]string{"44444444-4444-4444-8444-444444444444"},
		EndpointEffectMapping: &map[string]map[string]string{
			"tool_input":  {"tokenize": "redact"},
			"tool_output": {"tokenize": "redact"},
		},
		Criteria: &[]gen.PolicyCriteria{
			{
				Operator: gen.PolicyCriteriaOperator("AND"),
				Type:     gen.PolicyCriteriaType("claims"),
				Conditions: []gen.CriteriaCondition{
					{
						Field:    "department",
						Operator: gen.CriteriaConditionOperator("equals"),
						Value:    "legal",
					},
				},
			},
		},
		ProfileCount: int32Ptr(2),
		CreatedAt:    &now,
		CreatedBy:    strPtr("alice@example.com"),
		UpdatedAt:    &now,
		UpdatedBy:    strPtr("bob@example.com"),
	}

	var diags diag.Diagnostics
	m := dataProtectionPolicyToModel(fixtureTenantID, pol, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if m.PolicyID.ValueString() != "33333333-3333-4333-8333-333333333333" {
		t.Errorf("PolicyID = %q", m.PolicyID.ValueString())
	}
	if m.ID.ValueString() != fixtureTenantID+"/33333333-3333-4333-8333-333333333333" {
		t.Errorf("ID = %q", m.ID.ValueString())
	}
	if m.Name.ValueString() != "prod-pii-and-exfil" {
		t.Errorf("Name = %q", m.Name.ValueString())
	}
	if m.Priority.ValueInt64() != 100 {
		t.Errorf("Priority = %d", m.Priority.ValueInt64())
	}
	if !m.Enabled.ValueBool() {
		t.Errorf("Enabled = false; want true")
	}
	if m.DefaultEffect.ValueString() != "redact" {
		t.Errorf("DefaultEffect = %q", m.DefaultEffect.ValueString())
	}
	if m.FpeKeyID.ValueString() != "dlp-fpe-2026" {
		t.Errorf("FpeKeyID = %q", m.FpeKeyID.ValueString())
	}
	if m.TweakScope.ValueString() != "conversation" {
		t.Errorf("TweakScope = %q", m.TweakScope.ValueString())
	}
	if m.ProfileCount.ValueInt64() != 2 {
		t.Errorf("ProfileCount = %d", m.ProfileCount.ValueInt64())
	}
	// Scope flags — verify the false one isn't silently mapped from the wrong source.
	if m.ApplyToMcpInput.ValueBool() {
		t.Errorf("ApplyToMcpInput = true; want false")
	}
	if !m.ApplyToMcpOutput.ValueBool() {
		t.Errorf("ApplyToMcpOutput = false; want true")
	}

	ctx := context.Background()

	// Endpoint scopes — the false one proves the flags are not read from a neighbour.
	if !m.ApplyToToolInput.ValueBool() || m.ApplyToPromptInput.ValueBool() || !m.ApplyToToolOutput.ValueBool() {
		t.Errorf("endpoint scopes = %v/%v/%v; want true/false/true",
			m.ApplyToToolInput, m.ApplyToPromptInput, m.ApplyToToolOutput)
	}
	if m.ToolOutputUnscannedAction.ValueString() != "withhold" {
		t.Errorf("ToolOutputUnscannedAction = %q", m.ToolOutputUnscannedAction.ValueString())
	}
	var groups []string
	_ = m.DeviceGroupIDs.ElementsAs(ctx, &groups, false)
	if len(groups) != 1 || groups[0] != "44444444-4444-4444-8444-444444444444" {
		t.Errorf("device_group_ids = %v", groups)
	}
	var mapping map[string]map[string]string
	_ = m.EndpointEffectMapping.ElementsAs(ctx, &mapping, false)
	if mapping["tool_input"]["tokenize"] != "redact" || len(mapping) != 2 {
		t.Errorf("endpoint_effect_mapping = %v", mapping)
	}

	// enabled_profiles
	var profiles []string
	_ = m.EnabledProfiles.ElementsAs(ctx, &profiles, false)
	if len(profiles) != 2 || profiles[0] != "US_PII" {
		t.Errorf("enabled_profiles = %v", profiles)
	}

	// effects (string map)
	var effects map[string]string
	_ = m.Effects.ElementsAs(ctx, &effects, false)
	if effects["EXFILTRATION_URL"] != "log" || effects["US_SSN"] != "tokenize" {
		t.Errorf("effects = %v", effects)
	}

	// enabled_detectors (bool map)
	var enabled map[string]bool
	_ = m.EnabledDetectors.ElementsAs(ctx, &enabled, false)
	if !enabled["DATABASE_URL"] {
		t.Errorf("enabled_detectors = %v", enabled)
	}

	// detector_thresholds (float map)
	var thresholds map[string]float64
	_ = m.DetectorThresholds.ElementsAs(ctx, &thresholds, false)
	if thresholds["US_SSN"] != 0.95 {
		t.Errorf("detector_thresholds = %v", thresholds)
	}

	// detector_configs (JSON-string map) — value must be canonical JSON.
	var configs map[string]string
	_ = m.DetectorConfigs.ElementsAs(ctx, &configs, false)
	if configs["EXFILTRATION_URL"] != `{"minConfidenceScore":0.5}` {
		t.Errorf("detector_configs[EXFILTRATION_URL] = %q", configs["EXFILTRATION_URL"])
	}

	// criteria — nested ABAC. The platform stores and echoes the value RAW
	// (platform#2040); state carries it as the jsonencode-style string the
	// operator wrote.
	if len(m.Criteria) != 1 {
		t.Fatalf("criteria len = %d; want 1", len(m.Criteria))
	}
	cr := m.Criteria[0]
	if cr.Operator.ValueString() != "AND" || cr.Type.ValueString() != "claims" {
		t.Errorf("criteria[0] operator/type = %q/%q", cr.Operator.ValueString(), cr.Type.ValueString())
	}
	if len(cr.Conditions) != 1 {
		t.Fatalf("criteria[0].conditions len = %d; want 1", len(cr.Conditions))
	}
	cond := cr.Conditions[0]
	if cond.Field.ValueString() != "department" || cond.Operator.ValueString() != "equals" {
		t.Errorf("condition field/op = %q/%q", cond.Field.ValueString(), cond.Operator.ValueString())
	}
	if cond.Value.ValueString() != `"legal"` {
		t.Errorf("condition value = %q; want %q", cond.Value.ValueString(), `"legal"`)
	}
}

// An admin-api that predates the endpoint scopes omits them. The model must read them as the
// schema defaults (false / "pass" / [] / {}): a null against a defaulted plan value is a
// "provider produced inconsistent result" error on every apply.
func TestDataProtectionPolicyToModel_EndpointFieldsAbsent(t *testing.T) {
	polID := mustParseUUID(t, "33333333-3333-4333-8333-333333333334")
	var diags diag.Diagnostics
	m := dataProtectionPolicyToModel(fixtureTenantID, &adminapi.DataProtectionPolicy{Id: &polID}, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if m.ApplyToToolInput.IsNull() || m.ApplyToToolInput.ValueBool() ||
		m.ApplyToPromptInput.ValueBool() || m.ApplyToToolOutput.ValueBool() {
		t.Errorf("absent endpoint scopes must read as false, got %v/%v/%v",
			m.ApplyToToolInput, m.ApplyToPromptInput, m.ApplyToToolOutput)
	}
	if m.ToolOutputUnscannedAction.ValueString() != "pass" {
		t.Errorf("ToolOutputUnscannedAction = %q; want pass", m.ToolOutputUnscannedAction.ValueString())
	}
	if m.DeviceGroupIDs.IsNull() || len(m.DeviceGroupIDs.Elements()) != 0 {
		t.Errorf("device_group_ids = %v; want an empty list", m.DeviceGroupIDs)
	}
	if m.EndpointEffectMapping.IsNull() || len(m.EndpointEffectMapping.Elements()) != 0 {
		t.Errorf("endpoint_effect_mapping = %v; want an empty map", m.EndpointEffectMapping)
	}
}

// A malformed device group id is returned rather than skipped (skipping could leave an empty
// list, which targets every device). An empty set is still SENT: that is how an update clears
// the targeting.
func TestDeviceGroupIDsToSDK(t *testing.T) {
	ctx := context.Background()
	var out *[]adminapi.UUID
	bad := types.SetValueMust(types.StringType, []attr.Value{
		types.StringValue("44444444-4444-4444-8444-444444444444"), types.StringValue("not-a-uuid")})
	if invalid := uuidSetToSDK(ctx, bad, &out); len(invalid) != 1 || out != nil {
		t.Fatalf("invalid = %v, out = %v; want one invalid id and nothing sent", invalid, out)
	}
	empty := types.SetValueMust(types.StringType, []attr.Value{})
	if invalid := uuidSetToSDK(ctx, empty, &out); invalid != nil || out == nil || len(*out) != 0 {
		t.Fatalf("an empty set must be sent as [] to clear targeting; out = %v", out)
	}
}

// The attribute error for invalid ids, anchored on device_group_ids.
func TestAddInvalidDeviceGroups(t *testing.T) {
	var diags diag.Diagnostics
	addInvalidDeviceGroups(&diags, nil)
	if diags.HasError() {
		t.Fatalf("no invalid ids must add no error")
	}
	addInvalidDeviceGroups(&diags, []string{"not-a-uuid"})
	if !diags.HasError() || len(diags.Errors()) != 1 {
		t.Fatalf("want one error, got %v", diags)
	}
}
