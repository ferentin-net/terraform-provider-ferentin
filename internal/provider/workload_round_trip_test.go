package provider

import (
	"testing"
	"time"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// Round-trip tests for workload OAuth clients and workload identity providers
// — same pattern as phase3_round_trip_test.go. Fully populated fixture, asserts
// the mapper fills every field; catches a typo'd field name immediately.

func TestWorkloadOAuthClientToModel_RoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	id := mustParseUUID(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	ssoID := mustParseUUID(t, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")

	c := &adminapi.WorkloadOAuthClient{
		Id:                    &id,
		Name:                  strPtr("salesforce-prod-cc"),
		Description:           strPtr("Salesforce upstream"),
		Issuer:                strPtr("https://login.salesforce.com"),
		JwksUri:               strPtr("https://login.salesforce.com/.well-known/jwks.json"),
		TokenEndpoint:         strPtr("https://login.salesforce.com/services/oauth2/token"),
		ClientId:              strPtr("3MVG9_kFoxRl"),
		IdpType:               strPtr("generic_oidc"),
		AuthMethod:            strPtr("private_key_jwt"),
		AudienceParamStrategy: strPtr("audience_param"),
		DefaultAudience:       strPtr("https://login.salesforce.com"),
		DefaultResource:       strPtr("https://example.com/mcp"),
		DefaultScopes:         strPtr("api refresh_token"),
		PrivateKeyJwtAlg:      strPtr("RS256"),
		PrivateKeyJwtJwksUrl:  strPtr("https://example.com/.well-known/jwks.json"),
		PrivateKeyJwtKid:      strPtr("kid-2026-q1"),
		SsoIdpId:              &ssoID,
		IsActive:              boolPtr(true),
		Direction:             strPtr("outbound"),
		HasClientSecret:       boolPtr(false),
		HasPrivateKeyJwtKey:   boolPtr(true),
		Version:               int64Ptr(7),
		CreatedAt:             &now,
		CreatedBy:             strPtr("alice@example.com"),
		UpdatedAt:             &now,
		UpdatedBy:             strPtr("bob@example.com"),
		ManagedBy:             strPtr("terraform"),
		ManagedByClientId:     strPtr("fc_abc123"),
		ManagedByModule:       strPtr("workload-creds"),
		LastModifiedBy:        strPtr("terraform"),
	}

	m := workloadOAuthClientToModel(fixtureTenantID, c)

	assertEqual := func(name, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	assertEqual("ID", m.ID.ValueString(), fixtureTenantID+"/"+id.String())
	assertEqual("Name", m.Name.ValueString(), "salesforce-prod-cc")
	assertEqual("OauthClientID", m.OauthClientID.ValueString(), "3MVG9_kFoxRl")
	assertEqual("IdpType", m.IdpType.ValueString(), "generic_oidc")
	assertEqual("AuthMethod", m.AuthMethod.ValueString(), "private_key_jwt")
	assertEqual("AudienceParamStrategy", m.AudienceParamStrategy.ValueString(), "audience_param")
	assertEqual("PrivateKeyJwtAlg", m.PrivateKeyJwtAlg.ValueString(), "RS256")
	assertEqual("SsoIdpID", m.SsoIdpID.ValueString(), ssoID.String())
	assertEqual("Direction", m.Direction.ValueString(), "outbound")
	if !m.IsActive.ValueBool() {
		t.Error("IsActive should be true")
	}
	if !m.HasPrivateKeyJwtKey.ValueBool() {
		t.Error("HasPrivateKeyJwtKey should be true")
	}
	if m.Version.ValueInt64() != 7 {
		t.Errorf("Version = %d, want 7", m.Version.ValueInt64())
	}
	// WriteOnly attrs must NOT be in state.
	if !m.ClientSecret.IsNull() {
		t.Error("ClientSecret should be Null in state (WriteOnly)")
	}
	if !m.PrivateKeyJwtPrivateKey.IsNull() {
		t.Error("PrivateKeyJwtPrivateKey should be Null in state (WriteOnly)")
	}
}

func TestWorkloadIdentityProviderToModel_RoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	id := mustParseUUID(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	tid := mustParseUUID(t, fixtureTenantID)

	domains := []string{"engineering.example.com"}
	required := []string{"sub", "aud"}
	p := &adminapi.WorkloadIdentityProvider{
		Id:                    &id,
		TenantId:              tid,
		Name:                  "aws-prod-eks",
		Description:           strPtr("AWS EKS workloads"),
		CloudProvider:         "aws",
		ProtocolType:          "WORKLOAD_IDENTITY",
		JwksUri:               "https://sts.amazonaws.com/.well-known/jwks.json",
		AllowedIssuers:        []string{"https://sts.amazonaws.com"},
		ExpectedAudiences:     []string{"sts.amazonaws.com"},
		IdentityClaim:         strPtr("sub"),
		AllowClockSkewSeconds: int32Ptr(60),
		RequiredClaims:        &required,
		DomainNames:           &domains,
		Active:                boolPtr(true),
		CatchAll:              boolPtr(false),
		ValidateAudience:      boolPtr(true),
		ValidateIssuer:        boolPtr(true),
		ValidConfiguration:    boolPtr(true),
		PlatformManaged:       boolPtr(false),
		Aws:                   boolPtr(true),
		WorkloadIdentity:      boolPtr(true),
		CreatedAt:             &now,
		UpdatedAt:             &now,
	}

	m := workloadIdentityProviderToModel(fixtureTenantID, p)

	if got := m.ID.ValueString(); got != fixtureTenantID+"/"+id.String() {
		t.Errorf("ID = %q, want composite", got)
	}
	if m.Name.ValueString() != "aws-prod-eks" {
		t.Errorf("Name = %q", m.Name.ValueString())
	}
	if m.CloudProvider.ValueString() != "aws" {
		t.Errorf("CloudProvider = %q", m.CloudProvider.ValueString())
	}
	if m.ProtocolType.ValueString() != "WORKLOAD_IDENTITY" {
		t.Errorf("ProtocolType = %q", m.ProtocolType.ValueString())
	}
	if !m.AWS.ValueBool() {
		t.Error("AWS discriminator should be true")
	}
	if !m.WorkloadIdentity.ValueBool() {
		t.Error("WorkloadIdentity discriminator should be true")
	}
	if m.PlatformManaged.ValueBool() {
		t.Error("PlatformManaged should be false")
	}
	if !m.ValidConfiguration.ValueBool() {
		t.Error("ValidConfiguration should be true")
	}
	if m.AllowedIssuers.IsNull() || len(m.AllowedIssuers.Elements()) != 1 {
		t.Errorf("AllowedIssuers should have 1 element, got %d", len(m.AllowedIssuers.Elements()))
	}
	if m.ExpectedAudiences.IsNull() || len(m.ExpectedAudiences.Elements()) != 1 {
		t.Error("ExpectedAudiences should have 1 element")
	}
	if m.RequiredClaims.IsNull() || len(m.RequiredClaims.Elements()) != 2 {
		t.Error("RequiredClaims should have 2 elements")
	}
}
