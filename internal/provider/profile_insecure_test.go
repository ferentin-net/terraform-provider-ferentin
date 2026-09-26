package provider

import "testing"

func TestProfileInsecureApplies(t *testing.T) {
	const dev = "https://api.dev.example.test"
	cases := []struct {
		name            string
		explicit        bool
		endpoint        string
		profileEndpoint string
		profileInsecure bool
		want            bool
	}{
		{"profile's own endpoint", false, dev, dev, true, true},
		{"trailing slash is the same endpoint", false, dev + "/", dev, true, true},
		// The case this function exists for: the HCL names another host, and
		// the dev profile's opt-in must not follow it there.
		{"HCL endpoint differs", false, "https://api.other.example.test", dev, true, false},
		{"profile names no endpoint", false, "https://api.other.example.test", "", true, false},
		{"profile does not opt in", false, dev, dev, false, false},
		{"explicit opt-in is kept", true, "https://api.other.example.test", dev, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := profileInsecureApplies(tc.explicit, tc.endpoint, tc.profileEndpoint, tc.profileInsecure); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
