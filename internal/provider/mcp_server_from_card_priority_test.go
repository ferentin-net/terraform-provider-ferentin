package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// Priority goes with the card import (ferentin-platform#3318). The follow-up
// update is needed for it only when the import did not apply it: a re-import
// adopting an existing instance, an older platform, or an Update that changed
// priority alone (no import result).
func TestMCPServerFromCardNeedsFollowUpUpdate_Priority(t *testing.T) {
	plan := MCPServerFromCardResourceModel{
		Priority:    types.Int64Value(100),
		Enabled:     types.BoolNull(),
		Env:         types.MapNull(types.StringType),
		BearerToken: types.StringNull(),
	}
	withPriority := func(p int32) *adminapi.MCPServerCardImportResponse {
		return &adminapi.MCPServerCardImportResponse{Instance: &gen.McpServerResponseDto{Priority: &p}}
	}
	r := &MCPServerFromCardResource{}
	if r.needsFollowUpUpdate(plan, withPriority(100)) {
		t.Error("the import applied the priority; no follow-up update is needed")
	}
	if !r.needsFollowUpUpdate(plan, withPriority(10)) {
		t.Error("the import left the default (re-import or older platform); the follow-up must set it")
	}
	if !r.needsFollowUpUpdate(plan, nil) {
		t.Error("no import happened; a planned priority needs the update")
	}
	plan.Priority = types.Int64Null()
	if r.needsFollowUpUpdate(plan, withPriority(10)) {
		t.Error("no overrides planned; no follow-up update")
	}
}
