package provider

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// makeFakeJWT builds a minimally-valid JWT (header.payload.sig) with the
// given payload claims. Signature is bogus — the helper doesn't verify.
func makeFakeJWT(t *testing.T, payload string) string {
	t.Helper()
	enc := func(s string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	return enc(`{"alg":"EdDSA","typ":"at+jwt"}`) + "." + enc(payload) + "." + enc("sig")
}

func TestTenantIDFromJWT_Realistic(t *testing.T) {
	// Same shape the user shared: client_credentials-issued JWT with tid.
	token := makeFakeJWT(t, `{
		"sub": "service-account-d83958",
		"tid": "4858cc23-a4cc-442e-885c-dbcf014389e7",
		"scope": "admin",
		"auth_type": "client_credentials"
	}`)

	tid, err := tenantIDFromJWT(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tid != "4858cc23-a4cc-442e-885c-dbcf014389e7" {
		t.Errorf("tid = %q, want 4858cc23-a4cc-442e-885c-dbcf014389e7", tid)
	}
}

func TestTenantIDFromJWT_NoTidClaim(t *testing.T) {
	token := makeFakeJWT(t, `{"sub":"some-user","scope":"openid"}`)

	_, err := tenantIDFromJWT(token)
	if !errors.Is(err, ErrNoTenantClaim) {
		t.Fatalf("want ErrNoTenantClaim, got %v", err)
	}
}

func TestTenantIDFromJWT_NotAJWT(t *testing.T) {
	_, err := tenantIDFromJWT("not-a-jwt")
	if err == nil {
		t.Fatal("expected error for malformed token")
	}
	if !strings.Contains(err.Error(), "not a JWT") {
		t.Errorf("error should hint at JWT format, got: %v", err)
	}
}

func TestTenantIDFromJWT_BadBase64(t *testing.T) {
	_, err := tenantIDFromJWT("aaa.!!!.ccc")
	if err == nil {
		t.Fatal("expected error for base64 decoding failure")
	}
}

func TestTenantIDFromJWT_BadJSON(t *testing.T) {
	enc := func(s string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	token := enc("header") + "." + enc("not-json{") + "." + enc("sig")
	_, err := tenantIDFromJWT(token)
	if err == nil {
		t.Fatal("expected JSON parse error")
	}
}
