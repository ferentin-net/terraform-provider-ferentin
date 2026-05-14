package provider

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// mcpServerEndpointValidator catches at plan time the three endpoint /
// deployment-mode interlock mistakes that today fail late with an opaque
// platform 400. Without this validator a user sees the error only at
// terraform-apply time, after the rest of the plan has applied — and the
// platform diagnostic ("Invalid server endpoint URL: HTTP is not allowed.
// Use HTTPS.") doesn't name the actual fix ("set deployment_mode =
// edge_routed and bind an edge_site_id").
//
// Tracked in terraform-provider-ferentin#1.
type mcpServerEndpointValidator struct{}

func (v mcpServerEndpointValidator) Description(context.Context) string {
	return "Catches endpoint / deployment_mode / edge_site_id interlock mistakes at plan time."
}

func (v mcpServerEndpointValidator) MarkdownDescription(context.Context) string {
	return v.Description(context.Background())
}

func (v mcpServerEndpointValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data MCPServerResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Unknown values flow in when an attribute is interpolated from another
	// resource's computed output (e.g. `edge_site_id =
	// ferentin_edge_site.foo.site_id` before that site has been created).
	// Defer the check in those cases — apply-time re-evaluates and the
	// server-side validation is the ultimate backstop. Only fire when
	// we're CERTAIN the user's config is wrong.
	deploymentMode := ""
	if !data.DeploymentMode.IsNull() && !data.DeploymentMode.IsUnknown() {
		deploymentMode = data.DeploymentMode.ValueString()
	}

	// edgeSiteState: KNOWN_SET / KNOWN_EMPTY / UNKNOWN
	type tristate int
	const (
		knownSet tristate = iota
		knownEmpty
		unknown
	)
	var edgeSiteState tristate
	switch {
	case data.EdgeSiteID.IsUnknown():
		edgeSiteState = unknown
	case data.EdgeSiteID.IsNull() || data.EdgeSiteID.ValueString() == "":
		edgeSiteState = knownEmpty
	default:
		edgeSiteState = knownSet
	}

	// Check 1: edge_routed requires edge_site_id.
	// Fires only when deployment_mode is concretely "edge_routed" AND
	// edge_site_id is known-empty (not Unknown — Unknown means "bound
	// to another resource that hasn't been created yet, will resolve
	// at apply").
	if deploymentMode == "edge_routed" && edgeSiteState == knownEmpty {
		resp.Diagnostics.AddAttributeError(
			path.Root("edge_site_id"),
			"edge_site_id is required when deployment_mode = \"edge_routed\"",
			"Bind the server to a tenant edge site via `edge_site_id = ferentin_edge_site.<your_site>.site_id`. "+
				"Without it the platform has no edge device to route traffic through and rejects the create.",
		)
	}

	// Checks 2 + 3: HTTP scheme or private/unresolvable hostname require
	// deployment_mode = "edge_routed". For both we need to parse the
	// endpoint URL; do that once and reuse.
	if data.Endpoint.IsNull() || data.Endpoint.IsUnknown() {
		return
	}
	endpoint := data.Endpoint.ValueString()
	if endpoint == "" {
		return
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		// Malformed URL — let the platform's URL parser surface the precise
		// error. Client-side parsing is permissive on purpose so we don't
		// reject valid URLs the platform accepts.
		return
	}

	// "in edge mode" — be optimistic about Unknown: a not-yet-created
	// edge site is a perfectly reasonable plan-time state. Only deny
	// when we're sure the config places the server in a public mode
	// with no edge binding.
	isEdgeMode := deploymentMode == "edge_routed" || edgeSiteState == knownSet || edgeSiteState == unknown

	if strings.EqualFold(u.Scheme, "http") && !isEdgeMode {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"HTTP endpoint requires deployment_mode = \"edge_routed\"",
			"The platform's SSRF guard rejects plain-HTTP upstreams on the default `public` deployment mode. "+
				"Set `deployment_mode = \"edge_routed\"` and bind an `edge_site_id` so the platform routes the "+
				"traffic via your edge device (which can fetch HTTP), or change the endpoint to HTTPS.",
		)
	}

	if isPrivateOrUnresolvableHost(u.Hostname()) && !isEdgeMode {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Endpoint host is private or unresolvable and requires deployment_mode = \"edge_routed\"",
			fmt.Sprintf("The host %q matches the platform's reserved-hostname rules (`.local`/`.internal`/"+
				"`.corp` suffix, RFC1918 / loopback / link-local IP, or `.example.{com,org,net}` reserved TLDs). "+
				"On the default `public` deployment the platform's SSRF guard rejects these. Set "+
				"`deployment_mode = \"edge_routed\"` and bind an `edge_site_id` so the fetch happens at the "+
				"edge device, where private and test-net hosts are reachable.", u.Hostname()),
		)
	}
}

// isPrivateOrUnresolvableHost mirrors the platform's SsrfNetworkValidator
// rules for hostname patterns and IP literals — pattern-only, no DNS
// resolution. We deliberately don't enumerate every entry in the
// platform's BLOCKED_HOSTS set (metadata IPs, all-zeros, etc.) — those
// are explicit attacks; if a user types one into `endpoint` they'll see
// the platform error at apply time and that's appropriate. The patterns
// below are the ones a developer trips on by mistake.
func isPrivateOrUnresolvableHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}

	// Reserved suffix list — mirrors SsrfProtectionService.isReservedHostnameSuffix.
	// `.test` is RFC 2606 reserved-for-testing; explicitly NOT included
	// because the platform's dev profile allows it (api.local.ferentin.test
	// itself uses it), and the user's intent is unambiguous.
	for _, suffix := range []string{".local", ".internal", ".corp"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	// RFC 2606 reserved TLDs / test domains — don't resolve in the wild.
	for _, suffix := range []string{".example.com", ".example.org", ".example.net", ".invalid"} {
		if host == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(host, suffix) {
			return true
		}
	}

	// IP literal — apply RFC1918 / loopback / link-local rules.
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() ||
			ip.IsPrivate() ||
			ip.IsLinkLocalUnicast() ||
			ip.IsUnspecified()
	}

	// Bare `localhost` (sans suffix). Pattern-match cheap.
	if host == "localhost" || host == "localhost.localdomain" {
		return true
	}

	return false
}
