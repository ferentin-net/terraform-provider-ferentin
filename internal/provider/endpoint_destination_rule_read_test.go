package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// Read on the upgrade path: state written by a provider version where these
// attributes were Computed holds the platform's empty forms ([] / ""). Read
// must keep them, so that a config which omits the attribute plans [] -> null
// once rather than Read silently rewriting state; and an attribute that was
// null in state must stay null when the platform returns [].
func TestEndpointRuleRead_KeepsEmptyFormsFromPriorState(t *testing.T) {
	ctx := context.Background()
	ruleID := mustParseUUID(t, "11111111-1111-4111-8111-111111111111")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(adminapi.EndpointDestinationRule{
			Id:               &ruleID,
			Name:             strPtr("block-chatgpt"),
			Action:           strPtr("block"),
			Description:      strPtr(""),
			DestinationHosts: &[]string{},
			AppBundleIds:     &[]string{},
			AppSigningIds:    &[]string{},
			AppTeamIds:       &[]string{},
			DeviceGroupIds:   &[]adminapi.UUID{},
			Version:          int64Ptr(3),
		})
	}))
	defer srv.Close()
	sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: srv.URL, Token: "test"})
	if err != nil {
		t.Fatalf("sdk: %v", err)
	}

	s := endpointRuleSchema(t)
	listType := s.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes["device_group_ids"]
	prior := tfsdk.State{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
		"tenant_id":        tftypes.NewValue(tftypes.String, fixtureTenantID),
		"rule_id":          tftypes.NewValue(tftypes.String, ruleID.String()),
		"name":             tftypes.NewValue(tftypes.String, "block-chatgpt"),
		"action":           tftypes.NewValue(tftypes.String, "block"),
		"description":      tftypes.NewValue(tftypes.String, ""),
		"device_group_ids": tftypes.NewValue(listType, []tftypes.Value{}),
		"app_bundle_ids":   tftypes.NewValue(listType, []tftypes.Value{}),
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
	if got.Description.IsNull() || got.Description.ValueString() != "" {
		t.Errorf(`description = %v, want "" kept from prior state`, got.Description)
	}
	if got.DeviceGroupIDs.IsNull() || len(got.DeviceGroupIDs.Elements()) != 0 {
		t.Errorf("device_group_ids = %v, want [] kept from prior state", got.DeviceGroupIDs)
	}
	if got.AppBundleIDs.IsNull() || len(got.AppBundleIDs.Elements()) != 0 {
		t.Errorf("app_bundle_ids = %v, want [] kept from prior state", got.AppBundleIDs)
	}
	for name, l := range map[string]interface{ IsNull() bool }{
		"destination_hosts": got.DestinationHosts,
		"app_signing_ids":   got.AppSigningIDs,
		"app_team_ids":      got.AppTeamIDs,
	} {
		if !l.IsNull() {
			t.Errorf("%s = %v, want null (null in prior state, [] from the platform)", name, l)
		}
	}
	if got.Version.ValueInt64() != 3 {
		t.Errorf("version = %v, want 3 from the platform", got.Version)
	}
}
