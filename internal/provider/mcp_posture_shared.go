package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// Helpers for the local MCP tool vetting columns of the endpoint posture row
// (risk policy and approval routing), which ferentin_mcp_scope_defaults
// writes. They lived on ferentin_endpoint_policy_settings until the unified MCP
// policy moved those columns to the scope defaults API (ferentin-platform#3256).

// localMcpRiskActions is what an unvetted local MCP tool at one risk rating gets.
var localMcpRiskActions = []string{"allow", "ask_once", "ask_every_call", "deny"}

var localMcpRiskPolicyAttrTypes = map[string]attr.Type{
	"low":    types.StringType,
	"medium": types.StringType,
	"high":   types.StringType,
}

// localMcpRiskPolicyToSDK sends the policy only when it is configured. All three
// tiers are Required in the schema, so a known object always carries all three.
func localMcpRiskPolicyToSDK(o types.Object) *gen.LocalMcpRiskPolicy {
	if o.IsNull() || o.IsUnknown() {
		return nil
	}
	tier := func(name string) string {
		v, ok := o.Attributes()[name].(types.String)
		if !ok {
			return ""
		}
		return v.ValueString()
	}
	low := gen.LocalMcpRiskPolicyLow(tier("low"))
	medium := gen.LocalMcpRiskPolicyMedium(tier("medium"))
	high := gen.LocalMcpRiskPolicyHigh(tier("high"))
	return &gen.LocalMcpRiskPolicy{Low: &low, Medium: &medium, High: &high}
}

// localMcpRiskPolicyToTF reads an absent policy, or one with no tier set, as null:
// that is what the platform stores for "no policy" (it maps {} to null). A stored
// policy always has all three tiers, because the platform refuses a write that
// sends fewer, so a partially set object does not arise.
func localMcpRiskPolicyToTF(p *gen.LocalMcpRiskPolicy) types.Object {
	if p == nil || (p.Low == nil && p.Medium == nil && p.High == nil) {
		return types.ObjectNull(localMcpRiskPolicyAttrTypes)
	}
	return types.ObjectValueMust(localMcpRiskPolicyAttrTypes, map[string]attr.Value{
		"low":    enumPtrToTF(p.Low),
		"medium": enumPtrToTF(p.Medium),
		"high":   enumPtrToTF(p.High),
	})
}

// localMcpRiskPolicyTiers requires all three tiers of tool_vetting_risk_policy and
// checks each against localMcpRiskActions — what Required + OneOf expressed when
// the attribute was a nested schema. The platform refuses a policy with fewer
// tiers (400), so this moves that to plan time.
type localMcpRiskPolicyTiers struct{}

func (localMcpRiskPolicyTiers) Description(context.Context) string {
	return "low, medium and high are each required and one of " + strings.Join(localMcpRiskActions, ", ")
}

func (v localMcpRiskPolicyTiers) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (localMcpRiskPolicyTiers) ValidateObject(_ context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	attrs := req.ConfigValue.Attributes()
	for _, tier := range []string{"low", "medium", "high"} {
		v, ok := attrs[tier].(types.String)
		if !ok || v.IsUnknown() {
			continue
		}
		if v.IsNull() {
			resp.Diagnostics.AddAttributeError(req.Path.AtName(tier),
				fmt.Sprintf("tool_vetting_risk_policy.%s is required", tier),
				"Set all three tiers (low, medium, high); the platform refuses a policy with fewer.")
			continue
		}
		if !slices.Contains(localMcpRiskActions, v.ValueString()) {
			resp.Diagnostics.AddAttributeError(req.Path.AtName(tier),
				fmt.Sprintf("tool_vetting_risk_policy.%s must be one of %s", tier, strings.Join(localMcpRiskActions, ", ")),
				fmt.Sprintf("Got %q.", v.ValueString()))
		}
	}
}

// decodeApprovalConfig parses the attribute's JSON strictly into the SDK's
// ApprovalConfig: an unknown key (a typo, or a field newer than this
// provider's SDK) is an error, because the platform would silently drop it.
// Keys must also match exactly: Go matches JSON keys case-insensitively, so a
// key in the wrong case would otherwise pass.
func decodeApprovalConfig(raw string) (*gen.ApprovalConfig, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg gen.ApprovalConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("unexpected data after the JSON object")
	}
	var written, canonical any
	if err := json.Unmarshal([]byte(raw), &written); err != nil {
		return nil, err
	}
	reencoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(reencoded, &canonical); err != nil {
		return nil, err
	}
	if key := firstMiscasedKey(written, canonical, ""); key != "" {
		return nil, fmt.Errorf("json: unknown field %q (keys are case-sensitive)", key)
	}
	return &cfg, nil
}

// firstMiscasedKey returns the path of a key in written that canonical, the
// same value re-encoded through the SDK type, does not have under the same
// spelling. A key re-encoding dropped because its value is empty is not
// reported unless another spelling of it exists.
func firstMiscasedKey(written, canonical any, at string) string {
	switch w := written.(type) {
	case map[string]any:
		c, _ := canonical.(map[string]any)
		for k, wv := range w {
			cv, ok := c[k]
			if !ok {
				for ck := range c {
					if strings.EqualFold(ck, k) {
						return at + k
					}
				}
				continue
			}
			if p := firstMiscasedKey(wv, cv, at+k+"."); p != "" {
				return p
			}
		}
	case []any:
		c, _ := canonical.([]any)
		for i, wv := range w {
			if i < len(c) {
				if p := firstMiscasedKey(wv, c[i], fmt.Sprintf("%s%d.", at, i)); p != "" {
					return p
				}
			}
		}
	}
	return ""
}

// approvalConfigToTF renders the platform's config as normalized JSON (keys
// sorted, compact, as jsonencode writes it).
func approvalConfigToTF(cfg *gen.ApprovalConfig) jsontypes.Normalized {
	if cfg == nil {
		return jsontypes.NewNormalizedNull()
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return jsontypes.NewNormalizedNull()
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return jsontypes.NewNormalizedNull()
	}
	normalized, err := json.Marshal(generic)
	if err != nil {
		return jsontypes.NewNormalizedNull()
	}
	return jsontypes.NewNormalizedValue(string(normalized))
}

// approvalConfigMaxChannels is the platform's limit (McpEffect.ApprovalConfig).
const approvalConfigMaxChannels = 16

// approvalConfigJSON validates tool_vetting_approval_config at plan time: a JSON
// object that decodes strictly into the SDK's ApprovalConfig, with a
// step_up_mode and 1-16 channels (the platform refuses a channel-less config
// on this endpoint whatever the mode), and not {} (the platform's clear,
// which the SDK cannot send; clearing is a console action).
type approvalConfigJSON struct{}

func (approvalConfigJSON) Description(context.Context) string {
	return "a JSON approval config with step_up_mode and 1-16 channels"
}

func (v approvalConfigJSON) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (approvalConfigJSON) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	raw := strings.TrimSpace(req.ConfigValue.ValueString())
	var generic map[string]any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "tool_vetting_approval_config is not a JSON object",
			"Write it with jsonencode({...}). "+err.Error())
		return
	}
	if len(generic) == 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "An empty approval config is refused",
			"{} is the platform's way to clear approval routing, which this provider cannot send. "+
				"Clear it in the admin console, or leave the attribute out to keep the current config.")
		return
	}
	cfg, err := decodeApprovalConfig(raw)
	if err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "tool_vetting_approval_config does not match the approval config",
			err.Error()+"\n\nAn unknown key fails here rather than being dropped: the platform ignores "+
				"keys it does not know.")
		return
	}
	if cfg.StepUpMode == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "tool_vetting_approval_config.step_up_mode is required",
			"Set step_up_mode to \"approve\" or \"reauth\".")
	} else if cfg.StepUpMode != "approve" && cfg.StepUpMode != "reauth" {
		resp.Diagnostics.AddAttributeError(req.Path, "tool_vetting_approval_config.step_up_mode is invalid",
			fmt.Sprintf("Got %q; use \"approve\" or \"reauth\".", cfg.StepUpMode))
	}
	switch {
	case cfg.Channels == nil || len(*cfg.Channels) == 0:
		resp.Diagnostics.AddAttributeError(req.Path, "tool_vetting_approval_config needs at least one channel",
			"The platform refuses an approval config without channels on this endpoint, whatever the "+
				"mode: it would read as set up while every unvetted local tool was denied.")
	case len(*cfg.Channels) > approvalConfigMaxChannels:
		resp.Diagnostics.AddAttributeError(req.Path, "tool_vetting_approval_config has too many channels",
			fmt.Sprintf("At most %d; got %d.", approvalConfigMaxChannels, len(*cfg.Channels)))
	}
}
