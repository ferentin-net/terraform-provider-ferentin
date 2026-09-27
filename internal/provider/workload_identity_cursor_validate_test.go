package provider

import (
	"strings"
	"testing"
)

// The configs that must stay SILENT matter as much as the ones that fail: an
// error on a working Cursor config would block the apply outright.
func TestCursorAllowedTeamsProblem(t *testing.T) {
	tests := []struct {
		name        string
		cloudConfig string
		wantProblem bool
		wantContain string
	}{
		{name: "unset", cloudConfig: "", wantProblem: true, wantContain: "not set"},
		{name: "not JSON", cloudConfig: "team_abc", wantProblem: true, wantContain: "not a JSON object"},
		{name: "JSON array, not object", cloudConfig: `["team_abc"]`, wantProblem: true, wantContain: "not a JSON object"},
		{name: "no allowed_team_ids", cloudConfig: `{"allowed_repositories":["acme/*"]}`, wantProblem: true, wantContain: "no `allowed_team_ids`"},
		{name: "empty list", cloudConfig: `{"allowed_team_ids":[]}`, wantProblem: true, wantContain: "no `allowed_team_ids`"},
		{name: "only empty entries", cloudConfig: `{"allowed_team_ids":["", null]}`, wantProblem: true, wantContain: "no `allowed_team_ids`"},
		{name: "empty string", cloudConfig: `{"allowed_team_ids":""}`, wantProblem: true, wantContain: "no `allowed_team_ids`"},
		{name: "wrong type", cloudConfig: `{"allowed_team_ids":42}`, wantProblem: true, wantContain: "no `allowed_team_ids`"},
		{name: "bare wildcard", cloudConfig: `{"allowed_team_ids":["*"]}`, wantProblem: true, wantContain: `["*"]`},
		{name: "double wildcard among bounded", cloudConfig: `{"allowed_team_ids":["team_abc","**"]}`, wantProblem: true, wantContain: `["**"]`},

		{name: "one team", cloudConfig: `{"allowed_team_ids":["team_abc123"]}`},
		{name: "bare string counts as one", cloudConfig: `{"allowed_team_ids":"team_abc123"}`},
		{name: "prefix pattern", cloudConfig: `{"allowed_team_ids":["team_acme*"]}`},
		{name: "with other constraints", cloudConfig: `{"allowed_team_ids":["team_abc"],"allowed_branches":["main"]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			detail, got := cursorAllowedTeamsProblem(tc.cloudConfig)
			if got != tc.wantProblem {
				t.Fatalf("problem = %v, want %v (detail: %q)", got, tc.wantProblem, detail)
			}
			if tc.wantContain != "" && !strings.Contains(detail, tc.wantContain) {
				t.Errorf("detail does not contain %q:\n%s", tc.wantContain, detail)
			}
		})
	}
}
