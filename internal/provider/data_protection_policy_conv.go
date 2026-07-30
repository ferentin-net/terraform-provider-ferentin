package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// Conversion helpers for the data-protection policy resource. The entity
// carries several map-typed fields the other policy resources don't, so the
// map<->Terraform helpers live here rather than in the shared helpers.go.

// -------------------------------------------------------------------------
// Map helpers (Terraform config <-> SDK input)
// -------------------------------------------------------------------------

// stringMapToSDK converts a Terraform map(string) into a *map[string]string,
// nil for Null/Unknown so the field is omitted from the request body.
func stringMapToSDK(ctx context.Context, m types.Map) *map[string]string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	_ = m.ElementsAs(ctx, &out, false)
	return &out
}

func boolMapToSDK(ctx context.Context, m types.Map) *map[string]bool {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := map[string]bool{}
	_ = m.ElementsAs(ctx, &out, false)
	return &out
}

func float64MapToSDK(ctx context.Context, m types.Map) *map[string]float64 {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := map[string]float64{}
	_ = m.ElementsAs(ctx, &out, false)
	return &out
}

// detectorConfigsToSDK decodes a Terraform map(string) — each value a JSON
// object produced by `jsonencode(...)` — into the platform's
// map[detectorID]map[string]any override shape. Invalid JSON is a config
// error, not a silent drop.
func detectorConfigsToSDK(ctx context.Context, m types.Map, diags *diag.Diagnostics) *map[string]map[string]interface{} {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	raw := map[string]string{}
	_ = m.ElementsAs(ctx, &raw, false)
	out := map[string]map[string]interface{}{}
	for detectorID, jsonStr := range raw {
		var inner map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &inner); err != nil {
			diags.AddError(
				"Invalid detector_configs JSON",
				fmt.Sprintf("detector_configs[%q] is not a JSON object: %v. "+
					"Wrap the value in jsonencode({...}).", detectorID, err),
			)
			continue
		}
		out[detectorID] = inner
	}
	return &out
}

// -------------------------------------------------------------------------
// Map helpers (SDK response -> Terraform state)
// -------------------------------------------------------------------------

func stringMapToTF(p *map[string]string) types.Map {
	if p == nil {
		return types.MapNull(types.StringType)
	}
	elems := make(map[string]attr.Value, len(*p))
	for k, v := range *p {
		elems[k] = types.StringValue(v)
	}
	mv, _ := types.MapValue(types.StringType, elems)
	return mv
}

func boolMapToTF(p *map[string]bool) types.Map {
	if p == nil {
		return types.MapNull(types.BoolType)
	}
	elems := make(map[string]attr.Value, len(*p))
	for k, v := range *p {
		elems[k] = types.BoolValue(v)
	}
	mv, _ := types.MapValue(types.BoolType, elems)
	return mv
}

func float64MapToTF(p *map[string]float64) types.Map {
	if p == nil {
		return types.MapNull(types.Float64Type)
	}
	elems := make(map[string]attr.Value, len(*p))
	for k, v := range *p {
		elems[k] = types.Float64Value(v)
	}
	mv, _ := types.MapValue(types.Float64Type, elems)
	return mv
}

// detectorConfigsToTF re-marshals each per-detector override back to a JSON
// string. Go's encoding/json and Terraform's jsonencode both emit
// sorted-key compact JSON, so for typical configs the round-trip is stable
// and produces no spurious diff.
func detectorConfigsToTF(p *map[string]map[string]interface{}, diags *diag.Diagnostics) types.Map {
	if p == nil {
		return types.MapNull(types.StringType)
	}
	elems := make(map[string]attr.Value, len(*p))
	for detectorID, inner := range *p {
		b, err := json.Marshal(inner)
		if err != nil {
			diags.AddError(
				"Failed to encode detector_configs",
				fmt.Sprintf("detector_configs[%q] could not be re-encoded: %v", detectorID, err),
			)
			continue
		}
		elems[detectorID] = types.StringValue(string(b))
	}
	mv, _ := types.MapValue(types.StringType, elems)
	return mv
}

// -------------------------------------------------------------------------
// Full SDK response -> resource model
// -------------------------------------------------------------------------

func dataProtectionPolicyToModel(tenantID string, pol *adminapi.DataProtectionPolicy, diags *diag.Diagnostics) DataProtectionPolicyResourceModel {
	m := DataProtectionPolicyResourceModel{TenantID: types.StringValue(tenantID)}
	if pol.Id != nil {
		m.PolicyID = types.StringValue(pol.Id.String())
	} else {
		m.PolicyID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.PolicyID.ValueString())

	m.Name = strPtrToTF(pol.Name)
	m.Description = strPtrToTF(pol.Description)
	m.Priority = int32PtrToTF(pol.Priority)
	m.Enabled = boolPtrOrDefault(pol.Enabled)

	m.EnabledProfiles = stringSliceToList(pol.EnabledProfiles)
	m.EnabledDetectors = boolMapToTF(pol.EnabledDetectors)
	m.DisabledDetectors = boolMapToTF(pol.DisabledDetectors)
	m.DetectorThresholds = float64MapToTF(pol.DetectorThresholds)
	m.DetectorConfigs = detectorConfigsToTF(pol.DetectorConfigs, diags)
	m.Effects = stringMapToTF(pol.Effects)

	m.DefaultEffect = strPtrToTF(pol.DefaultEffect)
	m.BlockedMessage = strPtrToTF(pol.BlockedMessage)
	m.FpeKeyID = strPtrToTF(pol.FpeKeyId)
	m.TweakScope = strPtrToTF(pol.TweakScope)

	m.ApplyToLlmInput = boolPtrOrDefault(pol.ApplyToLlmInput)
	m.ApplyToLlmOutput = boolPtrOrDefault(pol.ApplyToLlmOutput)
	m.ApplyToMcpInput = boolPtrOrDefault(pol.ApplyToMcpInput)
	m.ApplyToMcpOutput = boolPtrOrDefault(pol.ApplyToMcpOutput)

	m.Criteria = criteriaListFromSDK(pol.Criteria)

	m.ProfileCount = int32PtrToTF(pol.ProfileCount)
	m.CreatedAt = timePtrToTF(pol.CreatedAt)
	m.CreatedBy = strPtrToTF(pol.CreatedBy)
	m.UpdatedAt = timePtrToTF(pol.UpdatedAt)
	m.UpdatedBy = strPtrToTF(pol.UpdatedBy)
	return m
}
