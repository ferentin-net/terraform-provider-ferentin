package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

// Every model field must have a schema attribute and vice versa. A mismatch is
// a "Value Conversion Error" on the first apply, and nothing else in the unit
// suite builds a model against the schema: example validation never does. A
// schema edit once dropped settings_id from ferentin_endpoint_policy_settings
// and only the acceptance suite noticed.
func TestSchemaMatchesModel(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		res   resource.Resource
		model any
	}{
		"endpoint_policy_settings":  {NewEndpointPolicySettingsResource(), &EndpointPolicySettingsResourceModel{}},
		"endpoint_destination_rule": {NewEndpointDestinationRuleResource(), &EndpointDestinationRuleResourceModel{}},
		"endpoint_mcp_rule":         {NewEndpointMCPRuleResource(), &EndpointMCPRuleResourceModel{}},
		"data_protection_policy":    {NewDataProtectionPolicyResource(), &DataProtectionPolicyResourceModel{}},
		"device_group":              {NewDeviceGroupResource(), &DeviceGroupResourceModel{}},
	} {
		resp := &resource.SchemaResponse{}
		tc.res.Schema(ctx, resource.SchemaRequest{}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: schema: %v", name, resp.Diagnostics)
		}
		// Every attribute null: reading it into the model fails if any field has
		// no attribute, or any attribute no field.
		state := tfsdk.State{Schema: resp.Schema, Raw: endpointRuleValue(t, resp.Schema, nil)}
		if d := state.Get(ctx, tc.model); d.HasError() {
			t.Errorf("%s: model and schema disagree: %v", name, d)
		}
	}
}
