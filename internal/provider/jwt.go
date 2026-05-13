package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// tenantIDFromJWT extracts the `tid` claim from a JWT access token without
// verifying the signature — the platform verifies on every request, so this
// is read-only metadata. Used to auto-resolve the provider's default
// tenant_id when the user didn't set one (the credential already binds the
// principal to a tenant; asking for it separately is redundant).
//
// Threat model. An attacker who can substitute the bearer token (e.g., via
// a compromised CI env var) controls the `tid` we read. They CANNOT use
// this to access a tenant they don't have access to — the platform
// verifies the JWT signature on every request and rejects tokens whose
// `tid` doesn't match the principal's bound tenant. The worst case is
// "every API call gets 403 with a confusing message"; no data ever leaks.
// Adding signature verification HERE would not strengthen this — the
// platform is already the trust boundary and we have nothing to verify
// against (no JWKS, no issuer pinning) at provider-init time.
//
// Returns ErrNoTenantClaim when the token parses but lacks `tid`, and a
// wrapped parse error when the token isn't a well-formed JWT.
func tenantIDFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("not a JWT (expected 3 dot-separated parts, got %d)", len(parts))
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode JWT payload: %w", err)
	}

	var claims struct {
		Tid string `json:"tid"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("parse JWT claims: %w", err)
	}
	if claims.Tid == "" {
		return "", ErrNoTenantClaim
	}
	return claims.Tid, nil
}

// ErrNoTenantClaim signals that the JWT parsed cleanly but doesn't include
// a `tid` claim. Surface to the user as "set tenant_id explicitly" — usually
// means they're hitting a non-Ferentin token endpoint by mistake.
var ErrNoTenantClaim = errors.New("JWT has no tid claim")
