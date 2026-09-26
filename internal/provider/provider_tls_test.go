package provider

import (
	"errors"
	"testing"
)

// fakeSettings records every destination it is asked about and answers
// "insecure" only for the targets in allow, and only when the caller's opt-in
// matches wantExplicit — so a wiring that drops the opt-in on one path fails.
type fakeSettings struct {
	refuse       error
	allow        map[string]bool
	wantExplicit bool
	asked        []string
}

func (f *fakeSettings) CheckOptIn(bool) error { return f.refuse }

func (f *fakeSettings) InsecureFor(target string, explicit bool) bool {
	f.asked = append(f.asked, target)
	return explicit == f.wantExplicit && f.allow[target]
}

const (
	tlsDevEdge   = "https://api.local.ferentin.test"
	tlsDevIssuer = "https://auth.local.ferentin.test/tenant/1"
	tlsOther     = "https://api.other.example.test"
)

func TestResolveConnection_Endpoint(t *testing.T) {
	for _, tc := range []struct {
		name, hcl, profile, want string
	}{
		{"the provider block wins", tlsOther, tlsDevEdge, tlsOther},
		{"the profile fills an unset endpoint", "", tlsDevEdge, tlsDevEdge},
		{"production when neither names one", "", "", DefaultEndpoint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := resolveConnection(&fakeSettings{}, tc.hcl, tc.profile, false)
			if err != nil {
				t.Fatal(err)
			}
			if conn.endpoint != tc.want {
				t.Errorf("endpoint = %q, want %q", conn.endpoint, tc.want)
			}
		})
	}
}

// The admin API and the issuer are asked about SEPARATELY, each by its own
// origin, and the caller's opt-in reaches both. The old code computed one bool
// against the admin endpoint and applied it to the issuer.
func TestResolveConnection_AsksPerDestination(t *testing.T) {
	f := &fakeSettings{allow: map[string]bool{tlsDevEdge: true}, wantExplicit: true}
	conn, err := resolveConnection(f, "", tlsDevEdge, true)
	if err != nil {
		t.Fatal(err)
	}
	if !conn.adminInsecure {
		t.Error("adminInsecure = false for the endpoint the settings allow")
	}
	if conn.issuerInsecure(tlsDevIssuer) {
		t.Error("the admin endpoint's answer was applied to the issuer")
	}
	if !conn.issuerInsecure(tlsDevEdge) {
		t.Error("issuerInsecure did not pass the caller's opt-in through")
	}
	if len(f.asked) != 3 || f.asked[0] != tlsDevEdge || f.asked[1] != tlsDevIssuer {
		t.Errorf("asked about %q, want the admin endpoint, then each issuer target", f.asked)
	}
}

// An opt-in the managed layer forbids is refused, not quietly overridden —
// before anything is resolved, so nothing downstream can use it.
func TestResolveConnection_RefusesAForbiddenOptIn(t *testing.T) {
	refusal := errors.New("managed says no")
	f := &fakeSettings{refuse: refusal}
	if _, err := resolveConnection(f, tlsDevEdge, "", true); !errors.Is(err, refusal) {
		t.Fatalf("err = %v, want the settings' refusal", err)
	}
	if len(f.asked) != 0 {
		t.Errorf("asked about %q after refusing", f.asked)
	}
}
