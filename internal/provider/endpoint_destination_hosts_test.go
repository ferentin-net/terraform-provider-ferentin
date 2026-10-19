package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// testdata/cross-language/destination-host-vectors.json is a byte-identical
// copy of ferentin-platform's
// shared-core/src/test/resources/fixtures/cross-language/destination-host-vectors.json
// (platform 6e11cd18e, sha256 026e2a1789e43777d48e35e65a9e527124a6a83d541b42cbd38cefb1f829d6b8).
// Refresh it by copying, never by editing: it is the agent's grammar. `make
// sync-host-vectors` copies it and prints the new hash for
// destinationHostVectorsSHA256.
const destinationHostVectorsSHA256 = "026e2a1789e43777d48e35e65a9e527124a6a83d541b42cbd38cefb1f829d6b8"

type destinationHostVectors struct {
	SchemaVersion int `json:"schema_version"`
	Validity      []struct {
		Host  string `json:"host"`
		Valid bool   `json:"valid"`
		Why   string `json:"why"`
	} `json:"validity"`
	Matching []struct {
		Pattern string `json:"pattern"`
	} `json:"matching"`
}

func TestValidRuleHost_CrossLanguageVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/cross-language/destination-host-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != destinationHostVectorsSHA256 {
		t.Fatalf("destination-host-vectors.json sha256 %x, pinned %s: copy the platform's file, do not edit it",
			sum, destinationHostVectorsSHA256)
	}
	var v destinationHostVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.SchemaVersion != 1 {
		t.Fatalf("schema_version %d: this test reads version 1", v.SchemaVersion)
	}
	if len(v.Validity) == 0 {
		t.Fatal("no validity vectors")
	}
	for _, c := range v.Validity {
		if got := validRuleHost(c.Host); got != c.Valid {
			t.Errorf("validRuleHost(%q) = %v, want %v (%s)", c.Host, got, c.Valid, c.Why)
		}
	}
	// Every pattern the matcher is pinned with is one the grammar accepts.
	for _, m := range v.Matching {
		if !validRuleHost(m.Pattern) {
			t.Errorf("matching pattern %q refused", m.Pattern)
		}
	}
}

// Lowercased as Java's toLowerCase(Locale.ROOT) does. strings.ToLower maps
// `İ` to `i`, which would pass `İ.example` at plan for admin-api to refuse.
func TestNormalizeDestinationHost_FullCaseMapping(t *testing.T) {
	if got := normalizeDestinationHost("\u0130.example"); validRuleHost(got) {
		t.Errorf("İ.example normalized to %q and accepted; admin-api refuses it", got)
	}
	if got := normalizeDestinationHost("\u212A.example"); got != "k.example" {
		t.Errorf("Kelvin-sign host normalized to %q; admin-api stores k.example", got)
	}
}

// The grammar is checked on the form admin-api stores, so case and surrounding
// space are not refusals there and must not be here.
func TestValidRuleHost_NormalizedFormAccepted(t *testing.T) {
	for _, h := range []string{" API.OpenAI.com ", "\t.Anthropic.COM", "*.ChatGPT.com"} {
		if !validRuleHost(normalizeDestinationHost(h)) {
			t.Errorf("%q refused after normalizing to %q", h, normalizeDestinationHost(h))
		}
	}
}

func TestValidRuleHost_ByteBound(t *testing.T) {
	label := strings.Repeat("a", 63)
	long := strings.TrimSuffix(strings.Repeat(label+".", 9), ".") // 575 bytes, every label valid
	if validRuleHost(long) {
		t.Errorf("a %d-byte host was accepted; the bound is %d", len(long), destinationHostMaxBytes)
	}
}

func hostList(t *testing.T, hosts ...string) types.List {
	t.Helper()
	elems := make([]attr.Value, len(hosts))
	for i, h := range hosts {
		elems[i] = types.StringValue(h)
	}
	l, d := types.ListValue(types.StringType, elems)
	if d.HasError() {
		t.Fatal(d)
	}
	return l
}

func TestReconcileDestinationHosts(t *testing.T) {
	cases := []struct {
		name       string
		prior, got types.List
		wantPrior  bool
	}{
		{"normalized form keeps spelling", hostList(t, "API.OpenAI.com", " .chatgpt.com"), hostList(t, "api.openai.com", ".chatgpt.com"), true},
		{"identical", hostList(t, ".anthropic.com"), hostList(t, ".anthropic.com"), true},
		{"console edit reads as platform", hostList(t, "api.openai.com"), hostList(t, "api.deepseek.com"), false},
		{"console reorder reads as platform", hostList(t, "a.example", "b.example"), hostList(t, "b.example", "a.example"), false},
		{"length change reads as platform", hostList(t, "a.example"), hostList(t, "a.example", "b.example"), false},
		{"import has no prior", types.ListNull(types.StringType), hostList(t, "a.example"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reconcileDestinationHosts(c.prior, c.got)
			want := c.got
			if c.wantPrior {
				want = c.prior
			}
			if !got.Equal(want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestEndpointRuleValidateConfig_DestinationHosts(t *testing.T) {
	ctx := context.Background()
	s := endpointRuleSchema(t)
	hostsType := s.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes["destination_hosts"]

	validate := func(hosts ...tftypes.Value) []string {
		cfg := tfsdk.Config{Schema: s, Raw: endpointRuleValue(t, s, map[string]tftypes.Value{
			"name":              tftypes.NewValue(tftypes.String, "r"),
			"action":            tftypes.NewValue(tftypes.String, "block"),
			"destination_kind":  tftypes.NewValue(tftypes.String, "host"),
			"destination_hosts": tftypes.NewValue(hostsType, hosts),
		})}
		resp := &resource.ValidateConfigResponse{}
		(&EndpointDestinationRuleResource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: cfg}, resp)
		var out []string
		for _, d := range resp.Diagnostics.Errors() {
			out = append(out, d.Summary())
		}
		return out
	}
	str := func(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

	if errs := validate(str("API.OpenAI.com"), str(".anthropic.com"), str("*.chatgpt.com")); len(errs) != 0 {
		t.Errorf("valid hosts refused: %v", errs)
	}
	errs := validate(str("https://api.openai.com"), str("api.*.example"), str("ok.example"))
	if len(errs) != 2 || !strings.Contains(errs[0], "https://api.openai.com") || !strings.Contains(errs[1], "api.*.example") {
		t.Errorf("want one error per refused host, got %v", errs)
	}
	// An unknown host (from a resource not yet created) is checked at apply.
	if errs := validate(tftypes.NewValue(tftypes.String, tftypes.UnknownValue)); len(errs) != 0 {
		t.Errorf("unknown host refused at plan: %v", errs)
	}

	many := make([]tftypes.Value, destinationHostsMax+1)
	for i := range many {
		many[i] = str("h.example")
	}
	if errs := validate(many...); len(errs) != 1 || !strings.Contains(errs[0], "at most 64") {
		t.Errorf("want the 64-host bound, got %v", errs)
	}
	// The list's length is known even when a host in it is not.
	many[0] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	if errs := validate(many...); len(errs) != 1 || !strings.Contains(errs[0], "at most 64") {
		t.Errorf("want the 64-host bound with an unknown host, got %v", errs)
	}
}
