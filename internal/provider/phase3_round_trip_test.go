package provider

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// Phase 3 round-trip tests — for each new resource, verify the
// SDK-response → Terraform-state mapping populates every field correctly.
// Lightweight per H4 in the review: one fully-populated fixture per
// entity, asserts on the most important fields. Catches the entire class
// of "I typo'd a field name in the mapper" bugs.

// strPtr / boolPtr / int32Ptr / int64Ptr — fixture helpers.
func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
func int32Ptr(i int32) *int32 { return &i }
func int64Ptr(i int64) *int64 { return &i }

// enumPtr is the typed-enum counterpart to strPtr. The regenerated SDK models
// any field the platform declares with allowableValues as its own ~string type,
// so fixtures cannot use strPtr for those.
func enumPtr[T ~string](v T) *T { return &v }

const fixtureTenantID = "e3a86afa-70aa-4879-9ab8-ea56f3eef48b"

func mustParseUUID(t *testing.T, s string) openapi_types.UUID {
	t.Helper()
	u, err := parseUUID(s)
	if err != nil {
		t.Fatalf("parseUUID(%q): %v", s, err)
	}
	return u
}

func TestMcpServerToModel_RoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	srvID := mustParseUUID(t, "11111111-1111-4111-8111-111111111111")
	provID := mustParseUUID(t, "22222222-2222-4222-8222-222222222222")
	transport := gen.McpServerResponseDtoDeploymentMode("public")
	workloadClientID := mustParseUUID(t, "44444444-4444-4444-8444-444444444444")
	srv := &adminapi.MCPServer{
		Id:                          &srvID,
		ProviderId:                  &provID,
		Name:                        strPtr("internal-search-us"),
		Endpoint:                    strPtr("https://search.example.com/mcp"),
		DisplayName:                 strPtr("Internal Search (US)"),
		Description:                 strPtr("US-region internal search"),
		Icon:                        strPtr("search"),
		Enabled:                     boolPtr(true),
		Priority:                    int32Ptr(100),
		Version:                     int64Ptr(7),
		Slug:                        strPtr("internal-search-us"),
		AvailableForRouting:         boolPtr(true),
		Healthy:                     boolPtr(true),
		HealthStatus:                strPtr("healthy"),
		CredentialsConfigured:       boolPtr(true),
		CreatedAt:                   &now,
		CreatedBy:                   strPtr("alice@example.com"),
		UpdatedAt:                   &now,
		UpdatedBy:                   strPtr("bob@example.com"),
		ManagedByClientId:           strPtr("fc_abc123"),
		ManagedByModule:             strPtr("terraform-prod"),
		DeploymentMode:              &transport,
		EnabledScopes:               &[]string{"search:read", "search:cite"},
		Tags:                        &map[string]string{"env": "prod"},
		CcFederatedWorkloadClientId: &workloadClientID,
		CcFederatedAudienceOverride: strPtr("https://search.example.com"),
		CcFederatedScopesOverride:   strPtr("search:read"),
	}

	m := mcpServerToModel(fixtureTenantID, srv)

	if m.ServerID.ValueString() != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("ServerID = %q", m.ServerID.ValueString())
	}
	if m.ID.ValueString() != fixtureTenantID+"/11111111-1111-4111-8111-111111111111" {
		t.Errorf("ID = %q", m.ID.ValueString())
	}
	if m.Name.ValueString() != "internal-search-us" {
		t.Errorf("Name = %q", m.Name.ValueString())
	}
	if m.Endpoint.ValueString() != "https://search.example.com/mcp" {
		t.Errorf("Endpoint = %q", m.Endpoint.ValueString())
	}
	if m.Version.ValueInt64() != 7 {
		t.Errorf("Version = %d", m.Version.ValueInt64())
	}
	if !m.Enabled.ValueBool() {
		t.Errorf("Enabled = false; want true")
	}
	if m.Priority.ValueInt64() != 100 {
		t.Errorf("Priority = %d", m.Priority.ValueInt64())
	}
	// enabled_scopes
	var scopes []string
	_ = m.EnabledScopes.ElementsAs(context.Background(), &scopes, false)
	if len(scopes) != 2 || scopes[0] != "search:read" {
		t.Errorf("enabled_scopes = %v", scopes)
	}
	// tags
	var tags map[string]string
	_ = m.Tags.ElementsAs(context.Background(), &tags, false)
	if tags["env"] != "prod" {
		t.Errorf("tags = %v", tags)
	}
	// cc_federated_* fields
	if got := m.CcFederatedWorkloadClientID.ValueString(); got != "44444444-4444-4444-8444-444444444444" {
		t.Errorf("CcFederatedWorkloadClientID = %q", got)
	}
	if m.CcFederatedAudienceOverride.ValueString() != "https://search.example.com" {
		t.Errorf("CcFederatedAudienceOverride = %q", m.CcFederatedAudienceOverride.ValueString())
	}
	if m.CcFederatedScopesOverride.ValueString() != "search:read" {
		t.Errorf("CcFederatedScopesOverride = %q", m.CcFederatedScopesOverride.ValueString())
	}
}

func TestMcpPolicyToModel_RoundTrip(t *testing.T) {
	polID := mustParseUUID(t, "33333333-3333-4333-8333-333333333333")
	pol := &adminapi.MCPPolicy{
		Id:                &polID,
		Name:              strPtr("engineering-search"),
		Description:       strPtr("Engineering can query internal search"),
		Priority:          int32Ptr(50),
		Enabled:           boolPtr(true),
		ValidateArguments: boolPtr(true),
		ProviderInstances: &[]string{"internal-search-us"},
		Effect: &gen.McpEffectDto{
			Effect:             gen.McpEffectDtoEffect("allow"),
			Message:            strPtr("Granted via engineering policy"),
			GrantToolsets:      &[]string{"search:read", "search:cite"},
			RateLimitPerMinute: int32Ptr(30),
		},
	}

	m := mcpPolicyToModel(fixtureTenantID, pol)
	if m.Name.ValueString() != "engineering-search" {
		t.Errorf("Name = %q", m.Name.ValueString())
	}
	if m.Effect == nil {
		t.Fatal("Effect is nil")
	}
	if m.Effect.Type.ValueString() != "allow" {
		t.Errorf("Effect.Type = %q", m.Effect.Type.ValueString())
	}
	if m.Effect.RateLimitPerMinute.ValueInt64() != 30 {
		t.Errorf("RateLimitPerMinute = %d", m.Effect.RateLimitPerMinute.ValueInt64())
	}
	var toolsets []string
	_ = m.Effect.GrantToolsets.ElementsAs(context.Background(), &toolsets, false)
	if len(toolsets) != 2 {
		t.Errorf("GrantToolsets = %v", toolsets)
	}
}

func TestOtelSinkToModel_RoundTrip(t *testing.T) {
	sinkID := mustParseUUID(t, "44444444-4444-4444-8444-444444444444")
	sink := &adminapi.OtelSink{
		Id:             &sinkID,
		Name:           strPtr("primary-otlp"),
		Endpoint:       strPtr("https://otel.example.com"),
		SinkType:       strPtr("otlp_http"),
		Description:    strPtr("Primary collector"),
		Protocol:       strPtr("http"),
		Compression:    strPtr("gzip"),
		AuthType:       strPtr("bearer"),
		Enabled:        boolPtr(true),
		HasCredentials: boolPtr(true),
		Headers:        &map[string]string{"X-Tenant": "acme"},
		Tags:           &map[string]string{"env": "prod"},
	}
	m := otelSinkToModel(fixtureTenantID, sink)
	if m.Name.ValueString() != "primary-otlp" {
		t.Errorf("Name = %q", m.Name.ValueString())
	}
	if m.SinkType.ValueString() != "otlp_http" {
		t.Errorf("SinkType = %q", m.SinkType.ValueString())
	}
	if !m.Enabled.ValueBool() {
		t.Errorf("Enabled = false")
	}
}

func TestOtelPolicyToModel_RoundTrip(t *testing.T) {
	polID := mustParseUUID(t, "55555555-5555-4555-8555-555555555555")
	pol := &adminapi.OtelPolicy{
		Id:          &polID,
		Name:        strPtr("default-traces"),
		Description: strPtr("All traces to OTLP"),
		Priority:    int32Ptr(100),
		Enabled:     boolPtr(true),
		Signals:     &[]string{"traces", "metrics"},
		SinkIds:     &[]string{"44444444-4444-4444-8444-444444444444"},
		SinkCount:   int32Ptr(1),
	}
	m := otelPolicyToModel(fixtureTenantID, pol)
	if m.Name.ValueString() != "default-traces" {
		t.Errorf("Name = %q", m.Name.ValueString())
	}
	var signals []string
	_ = m.Signals.ElementsAs(context.Background(), &signals, false)
	if len(signals) != 2 {
		t.Errorf("Signals = %v", signals)
	}
}

func TestMcpProviderToModel_RoundTrip(t *testing.T) {
	provID := mustParseUUID(t, "66666666-6666-4666-8666-666666666666")
	transport := gen.McpProviderResponseDtoTransport("sse")
	prov := &adminapi.MCPProvider{
		Id:                    &provID,
		DisplayName:           strPtr("Internal Search"),
		McpSlug:               strPtr("internal-search"),
		Description:           strPtr("Tenant-private MCP"),
		Icon:                  strPtr("search"),
		Owner:                 strPtr("platform"),
		Contact:               strPtr("platform@example.com"),
		DefaultUrl:            strPtr("https://search.example.com"),
		Transport:             &transport,
		AllowEndpointOverride: boolPtr(false),
		EnabledScopes:         &[]string{"search:read"},
	}
	m := mcpProviderToModel(fixtureTenantID, prov)
	if m.DisplayName.ValueString() != "Internal Search" {
		t.Errorf("DisplayName = %q", m.DisplayName.ValueString())
	}
	if m.Slug.ValueString() != "internal-search" {
		t.Errorf("Slug = %q", m.Slug.ValueString())
	}
	if m.Transport.ValueString() != "sse" {
		t.Errorf("Transport = %q", m.Transport.ValueString())
	}
}

func TestAgentToModel_RoundTrip(t *testing.T) {
	agentID := mustParseUUID(t, "77777777-7777-4777-8777-777777777777")
	agentPlatform := gen.OidcClientAgentPlatform("claude")
	aiClientType := gen.OidcClientAiClientType("agent")
	a := &adminapi.OIDCClientRow{
		Id:                      &agentID,
		Name:                    "claude-desktop-prod",
		ApplicationType:         gen.OidcClientApplicationType("NATIVE"),
		ClientType:              gen.OidcClientClientType("PUBLIC"),
		ClientId:                strPtr("fc_abc123"),
		AgentPlatform:           &agentPlatform,
		AiClientType:            &aiClientType,
		TokenEndpointAuthMethod: strPtr("private_key_jwt"),
		ClientJwksUri:           strPtr("https://agents.example.com/jwks.json"),
		AccessTokenLifetime:     int32Ptr(900),
		Active:                  boolPtr(true),
		Scopes:                  &[]string{"llm", "mcp", "summarizer"},
	}
	m := agentToModel(fixtureTenantID, a)
	if m.Name.ValueString() != "claude-desktop-prod" {
		t.Errorf("Name = %q", m.Name.ValueString())
	}
	if m.ApplicationType.ValueString() != "NATIVE" {
		t.Errorf("ApplicationType = %q", m.ApplicationType.ValueString())
	}
	if m.AgentPlatform.ValueString() != "claude" {
		t.Errorf("AgentPlatform = %q", m.AgentPlatform.ValueString())
	}
	if m.ClientID.ValueString() != "fc_abc123" {
		t.Errorf("ClientID = %q", m.ClientID.ValueString())
	}
	if m.AccessTokenLifetime.ValueInt64() != 900 {
		t.Errorf("AccessTokenLifetime = %d", m.AccessTokenLifetime.ValueInt64())
	}
}

func TestClientTypeForApplicationType(t *testing.T) {
	cases := map[string]string{
		"NATIVE":  "PUBLIC",
		"WEB":     "PUBLIC",
		"SERVICE": "CONFIDENTIAL",
		"":        "CONFIDENTIAL",
		"native":  "PUBLIC",
	}
	for in, want := range cases {
		got := string(clientTypeForApplicationType(in))
		if got != want {
			t.Errorf("clientTypeForApplicationType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseUUID(t *testing.T) {
	if _, err := parseUUID("not-a-uuid"); err == nil {
		t.Errorf("expected error on bad UUID")
	}
	if _, err := parseUUID("11111111-1111-4111-8111-111111111111"); err != nil {
		t.Errorf("expected no error on valid UUID: %v", err)
	}
}

func TestStringConversions_Helpers(t *testing.T) {
	// stringSliceToList ↔ stringListToSDK round-trip
	in := []string{"a", "b", "c"}
	lv := stringSliceToList(&in)
	out := stringListToSDK(context.Background(), lv)
	if len(out) != 3 || out[0] != "a" {
		t.Errorf("round-trip = %v", out)
	}

	// nil input → null list
	null := stringSliceToList(nil)
	if !null.IsNull() {
		t.Errorf("nil slice should produce null list")
	}

	// setStringPtr
	var s *string
	setStringPtr(types.StringValue("hi"), &s)
	if s == nil || *s != "hi" {
		t.Errorf("setStringPtr = %v", s)
	}
	var s2 *string
	setStringPtr(types.StringNull(), &s2)
	if s2 != nil {
		t.Errorf("setStringPtr null = %v, want nil", s2)
	}

	// setInt32Ptr
	var i *int32
	setInt32Ptr(types.Int64Value(42), &i)
	if i == nil || *i != 42 {
		t.Errorf("setInt32Ptr = %v", i)
	}
}

// TestEdgeSiteToModel_Tags covers the `tags` mapping added for issue #6. The
// acceptance test asserted on `tags.tier` against a schema that never had the
// attribute, so the platform's tag support was unreachable from Terraform and
// the test could only ever fail at plan.
func TestEdgeSiteToModel_Tags(t *testing.T) {
	ctx := context.Background()

	t.Run("tags round-trip", func(t *testing.T) {
		site := &adminapi.EdgeSite{
			SiteId:   strPtr("prod-us-east-1a"),
			SiteName: strPtr("US East 1A"),
			Tags:     &map[string]string{"tier": "primary", "team": "platform"},
		}
		m := edgeSiteToModel(fixtureTenantID, site)
		if m.Tags.IsNull() || m.Tags.IsUnknown() {
			t.Fatalf("Tags = %v, want a known map", m.Tags)
		}
		var out map[string]string
		if diags := m.Tags.ElementsAs(ctx, &out, false); diags.HasError() {
			t.Fatalf("ElementsAs: %v", diags)
		}
		if out["tier"] != "primary" || out["team"] != "platform" {
			t.Errorf("Tags = %v, want tier=primary team=platform", out)
		}
	})

	// A site with no tags must map to a NULL map, not an empty one: with an
	// Optional+Computed attribute, an empty map in state against a config that
	// never mentions tags is a permanent non-empty plan.
	t.Run("absent tags stay null", func(t *testing.T) {
		m := edgeSiteToModel(fixtureTenantID, &adminapi.EdgeSite{SiteId: strPtr("s")})
		if !m.Tags.IsNull() {
			t.Errorf("Tags = %v, want null", m.Tags)
		}
	})

	// Null config must omit the field entirely rather than sending `{}`, which
	// the platform would read as "clear the tags".
	t.Run("null map is omitted from the request", func(t *testing.T) {
		if got := stringMapToSDK(ctx, types.MapNull(types.StringType)); got != nil {
			t.Errorf("stringMapToSDK(null) = %v, want nil", got)
		}
	})
}
