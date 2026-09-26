package provider

import (
	"errors"
	"strings"
	"testing"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/profileauth"
	"github.com/hashicorp/terraform-plugin-framework/path"
)

// Runs the real SDK constructors, so a pinned-SDK bump that renames the field
// the refusal names fails here rather than pointing a user at the wrong
// attribute.
func TestCleartextSDKDiagnostic(t *testing.T) {
	_, err := adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: "http://admin.example.com", Token: "t"})
	attr, _, detail, ok := cleartextSDKDiagnostic(err, false)
	if !ok || !attr.Equal(path.Root("endpoint")) {
		t.Fatalf("cleartext endpoint: attr=%v ok=%v err=%v", attr, ok, err)
	}
	if !strings.Contains(detail, "https") {
		t.Fatalf("detail does not say how to fix it: %q", detail)
	}

	_, err = adminapi.NewWithClientCredentials(
		adminapi.SDKOptions{Endpoint: "https://api.example.com"},
		adminapi.ClientCredentialsOptions{AuthURL: "http://auth.example.com/tenant/1", ClientID: "c", ClientSecret: "s"})
	attr, _, _, ok = cleartextSDKDiagnostic(err, true)
	if !ok || !attr.Equal(path.Root("auth_url")) {
		t.Fatalf("cleartext auth_url: attr=%v ok=%v err=%v", attr, ok, err)
	}

	// Userinfo in the refused URL must not reach the diagnostic.
	_, err = adminapi.NewWithToken(adminapi.SDKOptions{Endpoint: "http://alice:s3cr3t@admin.example.com", Token: "t"})
	if _, _, detail, _ := cleartextSDKDiagnostic(err, false); strings.Contains(detail, "s3cr3t") {
		t.Fatalf("diagnostic echoes userinfo: %q", detail)
	}

	if _, _, _, ok := cleartextSDKDiagnostic(errors.New("something else"), false); ok {
		t.Fatal("an unrelated error was claimed as a cleartext refusal")
	}
}

func TestSettingsLoadDiagnostic_NamesACleartextEndpoint(t *testing.T) {
	summary, _ := settingsLoadDiagnostic(profileauth.ErrCleartextEndpoint)
	if strings.Contains(summary, "Failed to read") {
		t.Fatalf("a cleartext endpoint is reported as an unreadable file: %q", summary)
	}
	summary, _ = settingsLoadDiagnostic(errors.New("yaml: bad"))
	if !strings.Contains(summary, "Failed to read") {
		t.Fatalf("a parse failure lost its summary: %q", summary)
	}
}
