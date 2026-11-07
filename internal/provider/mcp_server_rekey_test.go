package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// acknowledge_subject_rekey is write-only: it reaches the update body from
// config, and is never written to state.
func TestMCPServerUpdate_SendsAcknowledgeSubjectRekeyFromConfig(t *testing.T) {
	const serverID = "5b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e"
	for _, ack := range []bool{true, false} {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut || r.Method == http.MethodPatch {
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + serverID + `","name":"srv","version":3}`))
		}))
		sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: srv.URL, Token: "test"})
		if err != nil {
			t.Fatalf("sdk: %v", err)
		}

		sr := &resource.SchemaResponse{}
		NewMCPServerResource().Schema(context.Background(), resource.SchemaRequest{}, sr)
		s := sr.Schema
		if a, ok := s.Attributes["acknowledge_subject_rekey"]; !ok || !a.IsWriteOnly() {
			t.Fatal("acknowledge_subject_rekey must be a write-only attribute")
		}
		base := map[string]tftypes.Value{
			"tenant_id": tftypes.NewValue(tftypes.String, fixtureTenantID),
			"server_id": tftypes.NewValue(tftypes.String, serverID),
			"name":      tftypes.NewValue(tftypes.String, "srv"),
			"version":   tftypes.NewValue(tftypes.Number, 2),
		}
		withAck := map[string]tftypes.Value{"acknowledge_subject_rekey": tftypes.NewValue(tftypes.Bool, ack)}
		for k, v := range base {
			withAck[k] = v
		}
		plan := tfsdk.Plan{Schema: s, Raw: endpointRuleValue(t, s, base)}
		state := tfsdk.State{Schema: s, Raw: endpointRuleValue(t, s, base)}
		cfg := tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, withAck)}
		resp := &resource.UpdateResponse{State: state}
		(&MCPServerResource{sdk: sdk, tenantID: fixtureTenantID}).Update(context.Background(),
			resource.UpdateRequest{Plan: plan, State: state, Config: cfg}, resp)
		srv.Close()
		if resp.Diagnostics.HasError() {
			t.Fatalf("update: %v", resp.Diagnostics)
		}
		got, sent := body["acknowledge_subject_rekey"]
		if ack && got != true {
			t.Errorf("ack=true: body field = %v, want true", got)
		}
		if !ack && sent {
			t.Errorf("ack=false: field sent as %v, want absent", got)
		}
		var m MCPServerResourceModel
		if d := resp.State.Get(context.Background(), &m); d.HasError() {
			t.Fatalf("state: %v", d)
		}
		if !m.AcknowledgeSubjectRekey.IsNull() {
			t.Errorf("state holds acknowledge_subject_rekey = %v; write-only must stay null", m.AcknowledgeSubjectRekey)
		}
	}
}
