package provider

import (
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// The endpoint agent's grammar for a destination rule's destination_hosts,
// mirrored from the platform's EndpointRuleHosts (ferentin-platform#3102),
// which mirrors valid_rule_host in ferentin-endpoint.
//
// The agent checks every host when it reads a bundle and refuses the WHOLE
// endpoint policy over one it cannot read: the device keeps its last-known-good
// policy and no later change, to any rule, reaches it. Bundle 417 on dev was
// refused fleet-wide over `*.anthropic.com` (ferentin-endpoint#1242). admin-api
// now refuses such a host with 400, but by apply time other resources in the
// run may already exist, so this refuses it at plan.
//
// testdata/cross-language/destination-host-vectors.json pins it. That file is
// a byte-identical copy of the platform's; do not make the grammar stricter or
// looser here alone.

const (
	// destinationHostsMax is MAX_VALUES_PER_RULE_LIST, validate_list's bound.
	destinationHostsMax = 64
	// destinationHostMaxBytes is MAX_TEXT_BYTES, validate_list's per-value bound.
	destinationHostMaxBytes = 512
)

// normalizeDestinationHost is the form admin-api checks and stores: trimmed and
// lowercased. Java's trim() strips code points up to U+0020, so this does too.
//
// Lowercased with full case mapping, as Java's toLowerCase(Locale.ROOT) is, not
// strings.ToLower's one-to-one mapping: strings.ToLower reads `İ.example` as
// the valid `i.example`, where admin-api reads `i̇.example` and refuses it.
// A Caser holds state, so each call makes its own.
func normalizeDestinationHost(s string) string {
	return cases.Lower(language.Und).String(strings.TrimFunc(s, func(r rune) bool { return r <= ' ' }))
}

// validRuleHost is valid_rule_host: a hostname, or a suffix spelled
// `.example.com` or `*.example.com`, at most destinationHostMaxBytes bytes.
func validRuleHost(v string) bool {
	if v == "" || len(v) > destinationHostMaxBytes {
		return false
	}
	if suffix, ok := strings.CutPrefix(v, "*."); ok {
		return !strings.HasPrefix(suffix, ".") && validHost(suffix)
	}
	return validHost(v)
}

// validHost is valid_host: a hostname or a `.`-suffix. Like the agent, it
// refuses a hyphen only at the start or end of the whole value, not per label.
func validHost(v string) bool {
	c := strings.TrimPrefix(v, ".")
	if c == "" || strings.HasPrefix(c, "-") || strings.HasSuffix(c, "-") {
		return false
	}
	for i := 0; i < len(c); i++ {
		switch b := c[i]; {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '-', b == '.':
		default:
			return false
		}
	}
	// strings.Split keeps trailing empties, as Rust's split does: "x." has an
	// empty last label.
	for _, label := range strings.Split(c, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
	}
	return true
}

// reconcileDestinationHosts keeps the configured spelling of destination_hosts
// when the platform stored exactly its normalized form, so `API.OpenAI.com`
// is not an inconsistent result after apply nor drift on refresh. Anything
// else, such as a console edit, reads as the platform's value.
func reconcileDestinationHosts(prior, got types.List) types.List {
	if prior.IsNull() || prior.IsUnknown() || got.IsNull() || got.IsUnknown() {
		return got
	}
	p, g := prior.Elements(), got.Elements()
	if len(p) != len(g) {
		return got
	}
	for i := range p {
		ps, ok1 := p[i].(types.String)
		gs, ok2 := g[i].(types.String)
		if !ok1 || !ok2 || ps.IsUnknown() || ps.IsNull() || normalizeDestinationHost(ps.ValueString()) != gs.ValueString() {
			return got
		}
	}
	return prior
}

// destinationHostStrings returns the known, non-null elements of a list.
func destinationHostStrings(l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	for _, v := range l.Elements() {
		if s, ok := v.(types.String); ok && !s.IsUnknown() && !s.IsNull() {
			out = append(out, s.ValueString())
		}
	}
	return out
}
