package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

const postureMovedBody = `{"fieldErrors":{"unattributedAgentAction":"Set this under MCP Policies, When no ` +
	`policy applies. posture_field_moved","localMcpVettingMode":"Set this under MCP Policies, When no ` +
	`policy applies. posture_field_moved"},"message":"Validation failed"}`

func TestPostureFieldsMoved(t *testing.T) {
	err := fmt.Errorf("delete: %w", &adminapi.HTTPError{Status: 400, Body: postureMovedBody})
	fields, ok := postureFieldsMoved(err)
	if !ok || strings.Join(fields, ",") != "localMcpVettingMode,unattributedAgentAction" {
		t.Errorf("got %v, %v", fields, ok)
	}
	if _, ok := postureFieldsMoved(&adminapi.HTTPError{Status: 400, Body: `{"message":"other"}`}); ok {
		t.Error("another 400 is not the moved-field refusal")
	}
	if _, ok := postureFieldsMoved(&adminapi.HTTPError{Status: 409, Body: postureMovedBody}); ok {
		t.Error("only a 400 is the refusal")
	}
	if _, ok := postureFieldsMoved(nil); ok {
		t.Error("nil is not a refusal")
	}
	if fields, ok := postureFieldsMoved(&adminapi.HTTPError{Status: 400, Body: "x posture_field_moved"}); !ok || len(fields) != 0 {
		t.Errorf("an unparseable body still counts, naming no field: %v, %v", fields, ok)
	}
}

// A group posture DELETE refused for moved fields warns and leaves state, so a
// destroy that also deletes the device group can finish.
func TestEndpointSettingsDelete_MovedFieldsWarns(t *testing.T) {
	ctx := context.Background()
	var deletes int
	sdk := sandboxSDK(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes++
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(postureMovedBody))
	})
	res := &EndpointPolicySettingsResource{sdk: sdk, tenantID: fixtureTenantID}
	sresp := &resource.SchemaResponse{}
	res.Schema(ctx, resource.SchemaRequest{}, sresp)

	m := EndpointPolicySettingsResourceModel{
		DeviceGroupID: types.StringValue(scopeTestGroupID),
		TenantID:      types.StringValue(fixtureTenantID),
		Version:       types.Int64Value(3),
	}
	nullPostureSets(&m)
	st := tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(sresp.Schema.Type().TerraformType(ctx), nil)}
	if d := st.Set(ctx, &m); d.HasError() {
		t.Fatalf("state: %v", d)
	}
	resp := &resource.DeleteResponse{State: st}
	res.Delete(ctx, resource.DeleteRequest{State: st}, resp)
	if deletes != 1 {
		t.Errorf("deletes = %d, want 1", deletes)
	}
	if resp.Diagnostics.HasError() {
		t.Fatalf("want a warning, got errors %v", resp.Diagnostics)
	}
	w := resp.Diagnostics.Warnings()
	if len(w) != 1 || !strings.Contains(w[0].Summary(), "STILL IN FORCE") ||
		!strings.Contains(w[0].Detail(), "localMcpVettingMode, unattributedAgentAction") ||
		!strings.Contains(w[0].Detail(), "deleting the group removes its posture") {
		t.Errorf("warning = %v", w)
	}
}
