package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// Round-trip tests for the platform#2038 endpoint-policy resources. Same
// contract as the phase-3 suite: one fully-populated fixture per entity so a
// typo'd field name in the mapper fails here rather than in a customer's plan.
//
// The provenance fields get explicit assertions because they are the entire
// point of the IaC-readiness work — if `last_modified_by` silently fails to
// land in state, drift detection stops working and nobody notices.

func TestDeviceGroupToModel_RoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	groupID := mustParseUUID(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")

	// Plain *string, not a typed enum: DeviceGroupResponse is a Java record whose
	// components do not (yet) declare @Schema allowableValues, unlike the
	// endpoint-policy DTOs. enumPtrToTF is generic over ~string so it handles both.
	g := &adminapi.DeviceGroup{
		GroupId:           &groupID,
		Name:              strPtr("contractors"),
		Description:       strPtr("Third-party contractors"),
		Source:            strPtr("scim"),
		ExternalId:        strPtr("grp-42"),
		CreatedAt:         &now,
		UpdatedAt:         &now,
		Version:           int64Ptr(4),
		ManagedBy:         strPtr("iac"),
		ManagedByClientId: strPtr("ferentin-iac-prod"),
		LastModifiedBy:    strPtr("console"),
	}

	m := deviceGroupToModel(fixtureTenantID, g)

	if got, want := m.ID.ValueString(), fixtureTenantID+"/"+groupID.String(); got != want {
		t.Errorf("ID = %q, want %q", got, want)
	}
	if got, want := m.GroupID.ValueString(), groupID.String(); got != want {
		t.Errorf("GroupID = %q, want %q", got, want)
	}
	if got, want := m.Name.ValueString(), "contractors"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := m.Source.ValueString(), "scim"; got != want {
		t.Errorf("Source = %q, want %q", got, want)
	}
	if got, want := m.ExternalID.ValueString(), "grp-42"; got != want {
		t.Errorf("ExternalID = %q, want %q", got, want)
	}

	// Provenance (platform#2040 item 2). device_groups had NO concurrency control
	// until migration 1217; a group silently renamed under a concurrent console
	// edit re-scopes whatever endpoint policy targets it, so the version must land
	// in state for If-Match to work at all.
	if got, want := m.Version.ValueInt64(), int64(4); got != want {
		t.Errorf("Version = %d, want %d", got, want)
	}
	if got, want := m.ManagedBy.ValueString(), "iac"; got != want {
		t.Errorf("ManagedBy = %q, want %q", got, want)
	}
	if got, want := m.LastModifiedBy.ValueString(), "console"; got != want {
		t.Errorf("LastModifiedBy = %q, want %q — divergence is the drift signal", got, want)
	}
	if got, want := m.ManagedByClientID.ValueString(), "ferentin-iac-prod"; got != want {
		t.Errorf("ManagedByClientID = %q, want %q", got, want)
	}
}

func TestEndpointRuleToModel_RoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	ruleID := mustParseUUID(t, "11111111-1111-4111-8111-111111111111")
	groupA := mustParseUUID(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	groupB := mustParseUUID(t, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	managedBy := gen.EndpointDestinationRuleResponseManagedBy("iac")
	lastModifiedBy := gen.EndpointDestinationRuleResponseLastModifiedBy("console")

	rule := &adminapi.EndpointDestinationRule{
		Id:                 &ruleID,
		Name:               strPtr("block-chatgpt"),
		Description:        strPtr("Contractors may not use ChatGPT"),
		Priority:           int32Ptr(10),
		Enabled:            boolPtr(true),
		DestinationKind:    strPtr("ai_provider"),
		CatalogSlug:        strPtr("openai"),
		DestinationHosts:   &[]string{"chat.openai.com"},
		Action:             strPtr("steer"),
		SteerToUrl:         strPtr("https://edge.example.com/v1"),
		AppBundleIds:       &[]string{"com.openai.chat"},
		AppSigningIds:      &[]string{"com.openai.chat"},
		AppTeamIds:         &[]string{"TEAMID1234"},
		DeviceGroupIds:     &[]adminapi.UUID{groupA, groupB},
		CriteriaCombinator: strPtr("AND"),
		CreatedAt:          &now,
		UpdatedAt:          &now,
		CreatedBy:          strPtr("sub-hash-abc"),
		UpdatedBy:          strPtr("sub-hash-def"),
		Version:            int64Ptr(7),
		ManagedBy:          &managedBy,
		ManagedByClientId:  strPtr("ferentin-iac-prod"),
		ManagedByModule:    strPtr("terraform-provider-ferentin/0.2.0"),
		LastModifiedBy:     &lastModifiedBy,
	}

	m := endpointRuleToModel(fixtureTenantID, rule)

	if got, want := m.ID.ValueString(), fixtureTenantID+"/"+ruleID.String(); got != want {
		t.Errorf("ID = %q, want %q", got, want)
	}
	if got, want := m.Name.ValueString(), "block-chatgpt"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := m.Priority.ValueInt64(), int64(10); got != want {
		t.Errorf("Priority = %d, want %d", got, want)
	}
	if !m.Enabled.ValueBool() {
		t.Error("Enabled = false, want true")
	}
	if got, want := m.Action.ValueString(), "steer"; got != want {
		t.Errorf("Action = %q, want %q", got, want)
	}
	if got, want := m.SteerToURL.ValueString(), "https://edge.example.com/v1"; got != want {
		t.Errorf("SteerToURL = %q, want %q", got, want)
	}

	// Device group ids must render as canonical UUID strings so state matches
	// what the user wrote in HCL — anything else is perpetual drift.
	gotGroups := m.DeviceGroupIDs.Elements()
	if len(gotGroups) != 2 {
		t.Fatalf("DeviceGroupIDs has %d elements, want 2", len(gotGroups))
	}
	if got, want := gotGroups[0].(types.String).ValueString(), groupA.String(); got != want {
		t.Errorf("DeviceGroupIDs[0] = %q, want %q", got, want)
	}

	// Provenance — the drift signal.
	if got, want := m.Version.ValueInt64(), int64(7); got != want {
		t.Errorf("Version = %d, want %d", got, want)
	}
	if got, want := m.ManagedBy.ValueString(), "iac"; got != want {
		t.Errorf("ManagedBy = %q, want %q", got, want)
	}
	if got, want := m.LastModifiedBy.ValueString(), "console"; got != want {
		t.Errorf("LastModifiedBy = %q, want %q — divergence from ManagedBy is the drift signal", got, want)
	}
	if got, want := m.ManagedByClientID.ValueString(), "ferentin-iac-prod"; got != want {
		t.Errorf("ManagedByClientID = %q, want %q", got, want)
	}
}

func TestEndpointSettingsToModel_TenantDefaultVsGroupOverride(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	rowID := mustParseUUID(t, "33333333-3333-4333-8333-333333333333")
	groupID := mustParseUUID(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	managedBy := gen.EndpointPolicySettingsResponseManagedBy("iac")

	// The tenant-default row has a nil DeviceGroupId, and its Terraform id must
	// be the bare tenant id. Getting this wrong makes the default row and an
	// override collide in state.
	def := &adminapi.EndpointPolicySettings{
		Id:                       &rowID,
		UnapprovedMcpAction:      strPtr("report_only"),
		DefaultDestinationAction: strPtr("allow"),
		EchStripEnabled:          boolPtr(false),
		DohBlockEnabled:          boolPtr(false),
		QuicBlockEnabled:         boolPtr(false),
		CreatedAt:                &now,
		UpdatedAt:                &now,
		Version:                  int64Ptr(2),
		ManagedBy:                &managedBy,
	}
	m := endpointSettingsToModel(fixtureTenantID, def)
	if !m.DeviceGroupID.IsNull() {
		t.Errorf("DeviceGroupID = %v, want null for the tenant-default row", m.DeviceGroupID)
	}
	if got, want := m.ID.ValueString(), fixtureTenantID; got != want {
		t.Errorf("ID = %q, want the bare tenant id %q", got, want)
	}
	if got, want := m.UnapprovedMcpAction.ValueString(), "report_only"; got != want {
		t.Errorf("UnapprovedMcpAction = %q, want %q", got, want)
	}
	if got, want := m.Version.ValueInt64(), int64(2); got != want {
		t.Errorf("Version = %d, want %d", got, want)
	}

	override := &adminapi.EndpointPolicySettings{
		Id:                       &rowID,
		DeviceGroupId:            &groupID,
		UnapprovedMcpAction:      strPtr("block"),
		McpGatewayUrl:            strPtr("https://mcp.example.com"),
		DefaultDestinationAction: strPtr("block"),
		EchStripEnabled:          boolPtr(true),
		DohBlockEnabled:          boolPtr(true),
		QuicBlockEnabled:         boolPtr(true),
		Version:                  int64Ptr(5),
	}
	m = endpointSettingsToModel(fixtureTenantID, override)
	if got, want := m.DeviceGroupID.ValueString(), groupID.String(); got != want {
		t.Errorf("DeviceGroupID = %q, want %q", got, want)
	}
	if got, want := m.ID.ValueString(), fixtureTenantID+"/"+groupID.String(); got != want {
		t.Errorf("ID = %q, want %q", got, want)
	}
	if !m.QuicBlockEnabled.ValueBool() {
		t.Error("QuicBlockEnabled = false, want true")
	}
}

// toWrite must actively clear mcp_gateway_url when the config omits it. The
// platform's upsert preserves omitted fields, so sending nothing would leave a
// stale gateway URL rewriting every approved MCP config on the fleet after an
// operator believed they had removed it.
func TestEndpointSettingsToWrite_ClearsGatewayURLWhenUnset(t *testing.T) {
	m := EndpointPolicySettingsResourceModel{
		UnapprovedMcpAction:      types.StringValue("report_only"),
		DefaultDestinationAction: types.StringValue("allow"),
		McpGatewayURL:            types.StringNull(),
	}
	body := m.toWrite()
	if body.McpGatewayUrl == nil {
		t.Fatal("McpGatewayUrl = nil; an omitted config must send an explicit empty string to clear it")
	}
	if *body.McpGatewayUrl != "" {
		t.Errorf("McpGatewayUrl = %q, want empty string", *body.McpGatewayUrl)
	}

	m.McpGatewayURL = types.StringValue("https://mcp.example.com")
	body = m.toWrite()
	if body.McpGatewayUrl == nil || *body.McpGatewayUrl != "https://mcp.example.com" {
		t.Errorf("McpGatewayUrl = %v, want the configured URL", body.McpGatewayUrl)
	}
}

// A rule that omits every optional list must not send empty arrays that the
// platform would read as "no constraint" in a way that differs from absent —
// and conversely a configured list must survive the conversion.
func TestEndpointRuleToWrite_OptionalListsAndDefaults(t *testing.T) {
	ctx := context.Background()

	bare := EndpointDestinationRuleResourceModel{
		Name:             types.StringValue("block-chatgpt"),
		Action:           types.StringValue("block"),
		DestinationKind:  types.StringValue("ai_provider"),
		CatalogSlug:      types.StringValue("openai"),
		DestinationHosts: types.ListNull(types.StringType),
		AppBundleIDs:     types.ListNull(types.StringType),
		AppSigningIDs:    types.ListNull(types.StringType),
		AppTeamIDs:       types.ListNull(types.StringType),
		DeviceGroupIDs:   types.ListNull(types.StringType),
	}
	body, invalid := bare.toWrite(ctx)
	if len(invalid) != 0 {
		t.Fatalf("unexpected invalid group ids: %v", invalid)
	}
	if body.Name != "block-chatgpt" || body.Action != "block" {
		t.Fatalf("Name/Action = %q/%q, want block-chatgpt/block", body.Name, body.Action)
	}
	if body.DeviceGroupIds != nil {
		t.Errorf("DeviceGroupIds = %v, want nil for an untargeted rule (applies to every device)", body.DeviceGroupIds)
	}
	if body.AppBundleIds != nil {
		t.Errorf("AppBundleIds = %v, want nil when unset", body.AppBundleIds)
	}

	groupID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	groups, diags := types.ListValue(types.StringType, []attr.Value{types.StringValue(groupID)})
	if diags.HasError() {
		t.Fatalf("ListValue: %v", diags)
	}
	targeted := bare
	targeted.DeviceGroupIDs = groups
	body, invalid = targeted.toWrite(ctx)
	if len(invalid) != 0 {
		t.Fatalf("unexpected invalid group ids: %v", invalid)
	}
	if body.DeviceGroupIds == nil || len(*body.DeviceGroupIds) != 1 {
		t.Fatalf("DeviceGroupIds = %v, want one parsed UUID", body.DeviceGroupIds)
	}
	if (*body.DeviceGroupIds)[0].String() != groupID {
		t.Errorf("DeviceGroupIds[0] = %s, want %s", (*body.DeviceGroupIds)[0], groupID)
	}
}

// A malformed device_group_ids entry must be REPORTED, not skipped. Skipping is
// fail-open: an empty target list means "every device in the tenant", so
// dropping the one bad id in `device_group_ids = ["typo"]` would silently widen
// a rule scoped to one group into a fleet-wide rule.
func TestEndpointRuleToWrite_MalformedGroupIDIsReportedNotDropped(t *testing.T) {
	ctx := context.Background()
	bad, diags := types.ListValue(types.StringType, []attr.Value{
		types.StringValue("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		types.StringValue("not-a-uuid"),
	})
	if diags.HasError() {
		t.Fatalf("ListValue: %v", diags)
	}
	m := EndpointDestinationRuleResourceModel{
		Name:             types.StringValue("block-chatgpt"),
		Action:           types.StringValue("block"),
		DestinationHosts: types.ListNull(types.StringType),
		AppBundleIDs:     types.ListNull(types.StringType),
		AppSigningIDs:    types.ListNull(types.StringType),
		AppTeamIDs:       types.ListNull(types.StringType),
		DeviceGroupIDs:   bad,
	}
	body, invalid := m.toWrite(ctx)
	if len(invalid) != 1 || invalid[0] != "not-a-uuid" {
		t.Fatalf("invalid = %v, want exactly [not-a-uuid]", invalid)
	}
	if body.DeviceGroupIds != nil {
		t.Errorf("DeviceGroupIds = %v, want nil — a partially-parsed list must never be sent, "+
			"because the valid subset would narrow (or an empty result would widen) the rule silently",
			body.DeviceGroupIds)
	}
}

// Criteria must SURVIVE an update. The platform's PUT sets `criteria` from the
// request unconditionally, so a provider that omits the field deletes whatever
// the console authored. That is a fail-open, not merely lossy: criteria narrow a
// rule to a population, and the endpoint agent refuses to match a rule that has
// them — so wiping them flips an inert rule into one that is active for everyone
// it targets.
func TestEndpointRuleToWrite_PreservesCriteriaItCannotAuthor(t *testing.T) {
	ctx := context.Background()

	consoleAuthored := `[{"operator":"AND","conditions":[{"field":"department","operator":"equals","value":"legal"}]}]`
	m := EndpointDestinationRuleResourceModel{
		Name:             types.StringValue("allow-chatgpt-for-legal"),
		Action:           types.StringValue("allow"),
		DestinationHosts: types.ListNull(types.StringType),
		AppBundleIDs:     types.ListNull(types.StringType),
		AppSigningIDs:    types.ListNull(types.StringType),
		AppTeamIDs:       types.ListNull(types.StringType),
		DeviceGroupIDs:   types.ListNull(types.StringType),
		CriteriaJSON:     types.StringValue(consoleAuthored),
	}
	body, invalid := m.toWrite(ctx)
	if len(invalid) != 0 {
		t.Fatalf("unexpected invalid group ids: %v", invalid)
	}
	if body.Criteria == nil {
		t.Fatal("Criteria = nil — the PUT would DELETE the console-authored criteria, " +
			"widening the rule from one department to everyone it targets")
	}
	if len(*body.Criteria) != 1 {
		t.Fatalf("Criteria has %d groups, want 1", len(*body.Criteria))
	}
	if (*body.Criteria)[0]["operator"] != "AND" {
		t.Errorf("Criteria[0].operator = %v, want AND", (*body.Criteria)[0]["operator"])
	}

	// A rule with no criteria must send nothing, not an empty array.
	m.CriteriaJSON = types.StringNull()
	body, _ = m.toWrite(ctx)
	if body.Criteria != nil {
		t.Errorf("Criteria = %v, want nil when the rule has none", body.Criteria)
	}
}

// State must render criteria as JSON, and collapse "none" to null so an empty
// array on the wire does not show a permanent diff.
func TestCriteriaToJSONString(t *testing.T) {
	if got := criteriaToJSONString(nil); !got.IsNull() {
		t.Errorf("nil criteria => %v, want null", got)
	}
	empty := []map[string]interface{}{}
	if got := criteriaToJSONString(&empty); !got.IsNull() {
		t.Errorf("empty criteria => %v, want null (absent and [] both mean 'matches everyone')", got)
	}
	one := []map[string]interface{}{{"operator": "OR"}}
	got := criteriaToJSONString(&one)
	if got.IsNull() || !strings.Contains(got.ValueString(), `"operator":"OR"`) {
		t.Errorf("criteria => %q, want JSON containing the operator", got.ValueString())
	}
}
