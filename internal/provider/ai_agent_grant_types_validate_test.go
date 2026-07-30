package provider

import (
	"strings"
	"testing"
)

// The matrix for the issue #7 warning. The interesting cases are the two that
// must stay SILENT: a correctly-configured SERVICE agent, and the NATIVE/WEB
// agents whose conventional grant set legitimately has no client_credentials.
// A diagnostic that fires on correct configs is worse than none — operators
// learn to scroll past it.
func TestAgentGrantTypesAdvice(t *testing.T) {
	tests := []struct {
		name        string
		appType     string
		grantTypes  []string
		unset       bool
		wantWarning bool
		wantContain string
	}{
		{
			name:        "SERVICE unset warns",
			appType:     "SERVICE",
			unset:       true,
			wantWarning: true,
			wantContain: "not set",
		},
		{
			name:        "SERVICE without client_credentials warns and quotes the set",
			appType:     "SERVICE",
			grantTypes:  []string{"authorization_code", "refresh_token"},
			wantWarning: true,
			wantContain: `"authorization_code", "refresh_token"`,
		},
		{
			name:        "SERVICE with an empty list warns",
			appType:     "SERVICE",
			grantTypes:  []string{},
			wantWarning: true,
		},
		{
			name:       "SERVICE with client_credentials is silent",
			appType:    "SERVICE",
			grantTypes: []string{"client_credentials"},
		},
		{
			name:       "SERVICE with client_credentials among others is silent",
			appType:    "SERVICE",
			grantTypes: []string{"authorization_code", "client_credentials"},
		},
		// NATIVE / WEB act for a signed-in user: no client_credentials is the
		// CORRECT shape, so neither an unset nor an interactive grant set may
		// warn.
		{name: "NATIVE unset is silent", appType: "NATIVE", unset: true},
		{name: "WEB unset is silent", appType: "WEB", unset: true},
		{
			name:       "NATIVE with interactive grants is silent",
			appType:    "NATIVE",
			grantTypes: []string{"authorization_code", "refresh_token"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail, ok := agentGrantTypesAdvice(tt.appType, tt.grantTypes, tt.unset)
			if ok != tt.wantWarning {
				t.Fatalf("ok = %v, want %v (detail: %s)", ok, tt.wantWarning, detail)
			}
			if !tt.wantWarning {
				return
			}
			if tt.wantContain != "" && !strings.Contains(detail, tt.wantContain) {
				t.Errorf("detail missing %q:\n%s", tt.wantContain, detail)
			}
			// Every warning must name the fix, or it is just noise.
			if !strings.Contains(detail, `grant_types = ["client_credentials"]`) {
				t.Errorf("detail does not show the conventional set:\n%s", detail)
			}
			if !strings.Contains(detail, "assistant") {
				t.Errorf("detail does not name the consequence:\n%s", detail)
			}
		})
	}
}
