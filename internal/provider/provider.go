// Package provider implements the Ferentin Terraform provider on top of the
// terraform-plugin-framework v1.x runtime. The provider holds an
// *adminapi.SDKClient and a default tenant_id; resources pull both from
// req.ProviderData on Configure.
package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/providervalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/profileauth"
)

// Environment-variable fallbacks for the provider-block attributes. Mirrors
// AWS's `AWS_*` / `AWS_PROFILE` envs — when an attribute is unset, the
// corresponding env wins (and a literal flag override beats the env, per the
// framework's normal precedence).
const (
	envEndpoint         = "FERENTIN_ENDPOINT"
	envTenantID         = "FERENTIN_TENANT_ID"
	envToken            = "FERENTIN_TOKEN"
	envInsecure         = "FERENTIN_INSECURE_SKIP_VERIFY"
	envClientID         = "FERENTIN_CLIENT_ID"
	envClientSecret     = "FERENTIN_CLIENT_SECRET"
	envAuthURL          = "FERENTIN_AUTH_URL"
	envProfile          = "FERENTIN_PROFILE"
	envSharedConfigFile = "FERENTIN_SHARED_CONFIG_FILE"
)

// DefaultEndpoint is the production admin-api URL. Used when neither the
// `endpoint` attribute, FERENTIN_ENDPOINT env var, nor the shared profile's
// `endpoint` field supplies a value — matches the AWS provider's behavior
// of defaulting to the public service URL when nothing more specific is
// configured. Local-dev and air-gapped deployments override via attribute.
const DefaultEndpoint = "https://api.ferentin.net"

// FerentinProvider is the provider's main type. `version` is set by main.go
// from the link-time ldflags and surfaced in the Metadata response so
// `terraform providers` shows what shipped.
type FerentinProvider struct {
	version string
}

// FerentinProviderModel mirrors the provider block in HCL. Fields are
// optional in the schema; missing fields fall back to env vars (handled in
// Configure). The framework treats `types.String` as "may be Null / Unknown",
// so we always check IsNull / IsUnknown before reading.
//
// `Token` and `ClientSecret` are Sensitive (redacted in logs and plan output).
// Provider-level attributes are never persisted to state, so they don't need
// the resource-level WriteOnly mechanic.
type FerentinProviderModel struct {
	Endpoint           types.String `tfsdk:"endpoint"`
	TenantID           types.String `tfsdk:"tenant_id"`
	Token              types.String `tfsdk:"token"`
	ClientID           types.String `tfsdk:"client_id"`
	ClientSecret       types.String `tfsdk:"client_secret"`
	AuthURL            types.String `tfsdk:"auth_url"`
	Profile            types.String `tfsdk:"profile"`
	SharedConfigFile   types.String `tfsdk:"shared_config_file"`
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
}

// ProviderData is what every resource / data source receives via
// req.ProviderData after Configure. The double-pointer dance the framework
// uses internally means we hand back a value (not a pointer) — match that
// shape so resources can `req.ProviderData.(ProviderData)`.
type ProviderData struct {
	SDK      *adminapi.SDKClient
	TenantID string // default; resource attribute may override
}

// New returns a constructor compatible with providerserver.Serve.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &FerentinProvider{version: version}
	}
}

// Compile-time assertion that we satisfy the framework interfaces.
var (
	_ provider.Provider                     = (*FerentinProvider)(nil)
	_ provider.ProviderWithConfigValidators = (*FerentinProvider)(nil)
)

func (p *FerentinProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "ferentin"
	resp.Version = p.version
}

// ConfigValidators declares plan-time invariants between provider attributes.
// Catches conflicting / incomplete auth blocks before any HTTP request, with
// proper attribute paths shown in `terraform plan` output.
//
// Three mutually exclusive auth modes are supported (cf. AWS provider's
// static / env / shared-credentials trio):
//   - `token`                            — pre-minted bearer
//   - `client_id` + `client_secret`      — OAuth2 client_credentials w/ refresh
//   - `profile` (+ `shared_config_file`) — read tokens stashed by `ferentin login`
//
// These are belt-and-suspenders alongside the runtime checks in Configure():
// the runtime checks still cover the env-var fallback paths (which the
// framework can't see at plan time).
func (p *FerentinProvider) ConfigValidators(_ context.Context) []provider.ConfigValidator {
	return []provider.ConfigValidator{
		// token is mutually exclusive with the client_credentials pair…
		providervalidator.Conflicting(
			path.MatchRoot("token"),
			path.MatchRoot("client_id"),
		),
		providervalidator.Conflicting(
			path.MatchRoot("token"),
			path.MatchRoot("client_secret"),
		),
		// …and with the profile path.
		providervalidator.Conflicting(
			path.MatchRoot("token"),
			path.MatchRoot("profile"),
		),
		providervalidator.Conflicting(
			path.MatchRoot("client_id"),
			path.MatchRoot("profile"),
		),
		providervalidator.Conflicting(
			path.MatchRoot("client_secret"),
			path.MatchRoot("profile"),
		),
		// If either half of the client_credentials pair is set, both must be.
		providervalidator.RequiredTogether(
			path.MatchRoot("client_id"),
			path.MatchRoot("client_secret"),
		),
	}
}

func (p *FerentinProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages Ferentin admin-api resources (edge sites, LLM/MCP/OTEL policies, OIDC clients, …) " +
			"under a single tenant.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Admin-api base URL. Defaults to `https://api.ferentin.net` (production); " +
					"override for local-dev or air-gapped deployments (e.g. `https://api.local.ferentin.test`). " +
					"Falls back to env `FERENTIN_ENDPOINT`, then to the named profile's `endpoint` value " +
					"in the shared config file, then to the production default.",
				Optional: true,
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Default tenant UUID for tenant-scoped resources. Each resource may " +
					"override via its own `tenant_id` attribute. Falls back to env `FERENTIN_TENANT_ID`, " +
					"and finally auto-resolves from the access token's `tid` claim — so production deployments " +
					"typically don't need to set this at all (the credential already binds to a tenant).\n\n" +
					"**Note on auto-resolve cost.** When this attribute is unset, the provider calls the " +
					"current token source during `Configure()` to read a bearer for `tid` extraction. In " +
					"`client_credentials` mode that triggers an IdP token mint on every `terraform plan` and " +
					"`apply`; in `profile` mode it triggers a refresh only if the stored token is expired. Set " +
					"this attribute (or `FERENTIN_TENANT_ID`) explicitly in CI / hot loops to skip the lookup.",
				Optional: true,
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "Pre-minted bearer access token. Marked **Sensitive** so it's redacted " +
					"in plan output and logs. Provider-level attributes never enter persistent Terraform state " +
					"(they're configuration, not managed objects), so a leaked state file does NOT expose the " +
					"token. Suitable for tests and CI; production deployments should prefer the client-credentials " +
					"auth block (auto-refresh; see `client_id` / `client_secret`). Falls back to env `FERENTIN_TOKEN`.",
				Optional:  true,
				Sensitive: true,
			},
			"client_id": schema.StringAttribute{
				MarkdownDescription: "OAuth2 client_id for service-account auth (client_credentials grant). " +
					"Mutually exclusive with `token`. Pair with `client_secret`. Falls back to env " +
					"`FERENTIN_CLIENT_ID`.",
				Optional: true,
			},
			"client_secret": schema.StringAttribute{
				MarkdownDescription: "OAuth2 client_secret. **Sensitive** — redacted in logs and plan output. " +
					"Provider config does not persist to state. Pair with `client_id`. " +
					"Falls back to env `FERENTIN_CLIENT_SECRET`.",
				Optional:  true,
				Sensitive: true,
			},
			"auth_url": schema.StringAttribute{
				MarkdownDescription: "Authorization server base URL, used **only** with `client_id` / " +
					"`client_secret`. Falls back to env `FERENTIN_AUTH_URL`.\n\n" +
					"**Must be tenant-scoped.** The platform routes `client_credentials` token mints " +
					"per tenant, so the value is either `<auth-base>/tenant/<tenant_id>` or the " +
					"subdomain form `https://<tenant-shortname>.auth.<domain>`. A bare `https://auth.<domain>` " +
					"is rejected with *\"Tenant could not be determined. Use a tenant-specific endpoint " +
					"for this grant type.\"*\n\n" +
					"Defaults to `endpoint` with `auth.` substituted for `api.` plus `/tenant/<tenant_id>` " +
					"— e.g. endpoint `https://api.ferentin.net` with tenant `abc…` derives " +
					"`https://auth.ferentin.net/tenant/abc…`. Set it explicitly for the subdomain form, " +
					"or when the endpoint host does not start with `api.`.",
				Optional: true,
			},
			"profile": schema.StringAttribute{
				MarkdownDescription: "Named profile to read from the `ferentin` CLI's shared credentials file. " +
					"Uses the tokens `ferentin login --profile <name>` stashed in the OS keyring (or " +
					"`~/.ferentin/profile:<name>` fallback) and refreshes them via the stored refresh_token " +
					"as they expire. The profile's `endpoint` from `~/.ferentin/.ferentin.yaml` populates `endpoint` " +
					"when that is otherwise unset, and its `insecure` applies only to that endpoint's origin — not to " +
					"an endpoint set in HCL, nor to the issuer its tokens refresh against. A managed (MDM) " +
					"configuration takes precedence over the file, and a Ferentin production host is always verified. " +
					"Mutually exclusive with `token` and `client_id`/`client_secret`. Falls back to env " +
					"`FERENTIN_PROFILE`.",
				Optional: true,
			},
			"shared_config_file": schema.StringAttribute{
				MarkdownDescription: "Path to the YAML config file that holds the `profiles.<name>` entries. " +
					"Defaults to `~/.ferentin/.ferentin.yaml` (the CLI's canonical location). " +
					"Falls back to env `FERENTIN_SHARED_CONFIG_FILE`.",
				Optional: true,
			},
			"insecure_skip_verify": schema.BoolAttribute{
				MarkdownDescription: "Skip TLS verification. Local-dev only; do NOT set in production. " +
					"Applies to the admin API and to the issuer a profile's tokens refresh against. Never " +
					"takes effect for a Ferentin production host, and is refused where the machine's managed " +
					"(MDM) configuration sets `insecure: false`. Falls back to env `FERENTIN_INSECURE_SKIP_VERIFY=1`.",
				Optional: true,
			},
		},
	}
}

func (p *FerentinProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data FerentinProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := stringOrEnv(data.Endpoint, envEndpoint)
	tenantID := stringOrEnv(data.TenantID, envTenantID)
	token := stringOrEnv(data.Token, envToken)
	clientID := stringOrEnv(data.ClientID, envClientID)
	clientSecret := stringOrEnv(data.ClientSecret, envClientSecret)
	authURL := stringOrEnv(data.AuthURL, envAuthURL)
	authURLSupplied := authURL != ""
	profileName := stringOrEnv(data.Profile, envProfile)
	sharedConfigFile := stringOrEnv(data.SharedConfigFile, envSharedConfigFile)
	explicitInsecure := boolOrEnv(data.InsecureSkipVerify, envInsecure)

	// The CLI's configuration, managed (MDM) layer first. Read even with no
	// profile: the machine-wide managed `insecure` binds a static-token or
	// client_credentials run exactly as it binds a profile run.
	settings, err := profileauth.LoadSettings(profileName, sharedConfigFile)
	if err != nil {
		summary, detail := settingsLoadDiagnostic(err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	// Not when the caller opted in explicitly: CheckOptIn (in resolveConnection,
	// below) refuses that opt-in for the same cause, and one cause earns one
	// diagnostic.
	if w := settings.SuppressedInsecure(); w != nil && !explicitInsecure {
		resp.Diagnostics.AddWarning("The profile's `insecure: true` is not in effect", w.Error()+
			"\n\nA certificate error from this run means verification stayed on, "+
			"not that the endpoint is broken. Trust the endpoint's certificate authority instead.")
	}
	conn, err := resolveConnection(settings, endpoint, settings.Endpoint, explicitInsecure)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("insecure_skip_verify"),
			"Certificate verification cannot be skipped on this machine",
			err.Error()+"\n\nUnset `insecure_skip_verify` and FERENTIN_INSECURE_SKIP_VERIFY, or "+
				"trust the endpoint's certificate authority instead.",
		)
		return
	}
	endpoint = conn.endpoint

	// tenant_id resolution is deferred: we first build the SDK, then if the
	// user didn't set it explicitly we derive it from the JWT's `tid` claim
	// (which the auth-server populates from the principal's bound tenant).
	// Final fallback to "missing tenant_id" diagnostic happens after the
	// SDK is built.

	// Auth-mode resolution. The provider-level ConfigValidators already
	// reject mixing modes at plan time when the attributes come from HCL;
	// the runtime checks below also cover the env-var fallback path that
	// the framework can't see at plan time.
	ccPresent := clientID != "" || clientSecret != ""
	profilePresent := profileName != ""

	switch {
	case profilePresent && (token != "" || ccPresent):
		resp.Diagnostics.AddError(
			"Conflicting auth configuration",
			"Use either `profile`, `token`, or `client_id`+`client_secret` — not more than one.",
		)
	case token != "" && ccPresent:
		resp.Diagnostics.AddError(
			"Conflicting auth configuration",
			"Set either `token` (static / pre-minted) or `client_id`+`client_secret` "+
				"(OAuth2 client_credentials) — not both.",
		)
	case !profilePresent && !ccPresent && token == "":
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Missing auth credentials",
			"Configure one of: `profile` (FERENTIN_PROFILE) to read tokens from "+
				"`ferentin login`'s storage, `token` (FERENTIN_TOKEN) for a pre-minted bearer, "+
				"or `client_id`+`client_secret` (FERENTIN_CLIENT_ID / FERENTIN_CLIENT_SECRET) for "+
				"OAuth2 client_credentials.",
		)
	case ccPresent && (clientID == "" || clientSecret == ""):
		resp.Diagnostics.AddError(
			"Incomplete client_credentials configuration",
			"Both `client_id` and `client_secret` are required when using OAuth2 client_credentials auth.",
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	opts := adminapi.SDKOptions{
		Endpoint: endpoint,
		// The admin API's answer. In client_credentials mode the SDK also mints
		// against auth_url with it. With no profile, the only inputs that can
		// say "skip" are the caller's opt-in and a managed `insecure: true`,
		// and both are meant for every non-production host — so the answer is
		// the same for auth_url except where auth_url is production, which the
		// handshake refuses to skip whatever this says.
		SkipTLS:   conn.adminInsecure,
		UserAgent: "terraform-provider-ferentin/" + p.version,
		// Platform #651 provenance. "iac" is the platform's enum value for
		// Terraform / OpenTofu / CC-token automation; ManagedByModule is
		// free-form, identifies which provider build wrote the row so
		// `managed_by_module` surfaces drift cleanly across provider
		// versions.
		ManagedBy:       "iac",
		ManagedByModule: "terraform-provider-ferentin/" + p.version,
		OnRateLimit: func(s adminapi.RateLimitState) {
			tflog.Debug(ctx, "admin-api rate limit", map[string]any{
				"limit":     s.Limit,
				"remaining": s.Remaining,
				"reset":     s.Reset.String(),
				"policy":    s.Policy,
			})
		},
	}

	var (
		sdk      *adminapi.SDKClient
		authMode string
	)
	switch {
	case profilePresent:
		opts.UserAgent += " (profile=" + profileName + ")"
		src, perr := profileauth.NewProfileTokenSource(profileName, profileauth.ProfileTokenSourceConfig{
			// Asked about the stored issuer, a different origin from endpoint.
			InsecureFor: conn.issuerInsecure,
		})
		if perr != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("profile"),
				"Failed to load profile credentials",
				perr.Error()+"\n\nRun `ferentin login --profile "+profileName+"` to populate the profile.",
			)
			return
		}
		opts.Source = src
		sdk, err = adminapi.NewWithToken(opts)
		authMode = "profile=" + profileName
	case ccPresent:
		// Default auth_url: `api.` → `auth.` (the platform's canonical pairing),
		// plus the per-tenant token path the grant requires. See deriveAuthURL
		// for why the tenant path is mandatory rather than a nicety.
		if authURL == "" {
			if tenantID == "" {
				resp.Diagnostics.AddAttributeError(
					path.Root("auth_url"),
					"Cannot derive auth URL without a tenant",
					"`client_credentials` mints against a PER-TENANT token endpoint, so deriving "+
						"`auth_url` needs `tenant_id`.\n\nEither set `tenant_id` (or export "+
						"FERENTIN_TENANT_ID), or set `auth_url` to the tenant-scoped base yourself — "+
						"`https://auth.example.com/tenant/<tenant-uuid>`, or the subdomain form "+
						"`https://<tenant-shortname>.auth.example.com`.",
				)
				return
			}
			authURL = deriveAuthURL(endpoint, tenantID)
		}
		if authURL == "" {
			resp.Diagnostics.AddAttributeError(
				path.Root("auth_url"),
				"Cannot derive auth URL from endpoint",
				"Set `auth_url` explicitly, or export FERENTIN_AUTH_URL. "+
					"Endpoint did not contain `api.` for the default `api.→auth.` substitution.\n\n"+
					"Note the value must be TENANT-SCOPED — `<base>/tenant/<tenant-uuid>` or "+
					"`https://<tenant-shortname>.auth.<domain>` — because the platform routes "+
					"client_credentials mints per tenant.",
			)
			return
		}
		opts.UserAgent += " (client_credentials)"
		sdk, err = adminapi.NewWithClientCredentials(opts, adminapi.ClientCredentialsOptions{
			AuthURL:      authURL,
			ClientID:     clientID,
			ClientSecret: clientSecret,
		})
		authMode = "client_credentials"
	default:
		opts.Token = token
		sdk, err = adminapi.NewWithToken(opts)
		authMode = "static_token"
	}
	if err != nil {
		if attr, summary, detail, ok := cleartextSDKDiagnostic(err, ccPresent && !profilePresent && authURLSupplied); ok {
			resp.Diagnostics.AddAttributeError(attr, summary, detail)
			return
		}
		resp.Diagnostics.AddError("Failed to build admin-api SDK client", err.Error())
		return
	}

	// Resolve tenant_id from the JWT's `tid` claim when the user didn't set
	// it. The credential already binds to a tenant; asking for it separately
	// is redundant when we can read it off the bearer. Works for all three
	// auth modes (static token, client_credentials, profile) because every
	// admin-api JWT carries `tid` regardless of grant type.
	//
	// We pull the source via sdk.TokenSource() rather than opts.Source: the
	// SDK's NewWithClientCredentials sets the source on its own internal
	// copy of opts (Go pass-by-value), so the provider's local opts.Source
	// stays nil in CC mode. The accessor returns whichever source the SDK
	// actually wired up, regardless of which constructor we took.
	if tenantID == "" {
		bearer, terr := resolveBearer(ctx, token, sdk.TokenSource())
		if terr != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("tenant_id"),
				"Missing tenant ID (auto-resolve failed)",
				"Set `tenant_id` on the provider block, export FERENTIN_TENANT_ID, or fix the underlying auth issue.\n\n"+
					"Auto-resolve from the JWT's `tid` claim failed: "+terr.Error(),
			)
			return
		}
		tid, terr := tenantIDFromJWT(bearer)
		if terr != nil {
			hint := "Set `tenant_id` on the provider block, or export FERENTIN_TENANT_ID."
			if errors.Is(terr, ErrNoTenantClaim) {
				hint += " The bearer token has no `tid` claim — usually means you're hitting a non-Ferentin endpoint."
			}
			resp.Diagnostics.AddAttributeError(
				path.Root("tenant_id"),
				"Missing tenant ID",
				hint+"\n\nUnderlying error: "+terr.Error(),
			)
			return
		}
		tenantID = tid
		tflog.Debug(ctx, "tenant_id auto-resolved from JWT tid claim", map[string]any{
			"tenant_id": tenantID,
		})
	}

	pd := ProviderData{SDK: sdk, TenantID: tenantID}
	resp.DataSourceData = pd
	resp.ResourceData = pd

	tflog.Info(ctx, "Ferentin provider configured", map[string]any{
		"endpoint":  endpoint,
		"tenant_id": tenantID,
		"auth_mode": authMode,
	})
}

// resolveBearer returns the current bearer for whichever auth mode is active.
// For static `token`, that's the configured literal. For profile / CC, we
// ask the TokenSource — which lazily mints a fresh token via the appropriate
// grant. Failures here block tenant_id auto-resolution but otherwise leave
// the SDK working; the diagnostic above already explains how to recover.
func resolveBearer(ctx context.Context, staticToken string, source adminapi.TokenSource) (string, error) {
	if staticToken != "" {
		return staticToken, nil
	}
	if source == nil {
		return "", fmt.Errorf("no token source available")
	}
	return source.Token(ctx)
}

// deriveAuthURL returns the platform's canonical, TENANT-SCOPED auth-server base
// for a given admin-api endpoint: `api.` becomes `auth.`, then the tenant path
// the client_credentials grant requires is appended.
//
// The tenant path is not decoration. The platform routes CC token mints per
// tenant (`/tenant/{id}/token`), and the SDK appends `/token` to whatever this
// returns. A bare `https://auth.<domain>` therefore mints against the global
// endpoint, which the authorization server rejects: "Tenant could not be
// determined. Use a tenant-specific endpoint for this grant type."
//
// The subdomain form (`https://<tenant-shortname>.auth.<domain>`) is the other valid
// shape and deliberately is NOT derived: its label is the tenant *slug*, which
// this provider never sees — only the UUID. Callers wanting it set `auth_url`.
//
// Precondition: tenantID is non-empty; the caller diagnoses that case
// separately, because "no tenant" and "unrecognised endpoint host" need
// different advice. Returns "" when the `api.` substitution doesn't apply —
// localhost or a non-standard host must set `auth_url` explicitly.
func deriveAuthURL(endpoint, tenantID string) string {
	const marker = "://api."
	i := strings.Index(endpoint, marker)
	if i < 0 {
		return ""
	}
	base := strings.TrimRight(endpoint[:i]+"://auth."+endpoint[i+len(marker):], "/")
	return base + "/tenant/" + tenantID
}

func (p *FerentinProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewEdgeSiteResource,
		NewLLMProviderResource,
		NewLLMPolicyResource,
		NewMCPServerResource,
		NewMCPProviderResource,
		NewMCPServerFromCardResource,
		NewMCPPolicyResource,
		NewOtelSinkResource,
		NewOtelPolicyResource,
		NewAIAgentResource,
		NewWorkloadOAuthClientResource,
		NewWorkloadIdentityProviderResource,
		NewDataProtectionPolicyResource,
		NewDeviceGroupResource,
		NewEndpointDestinationRuleResource,
		NewEndpointPolicySettingsResource,
	}
}

func (p *FerentinProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewLLMProviderDataSource,
		NewMCPProviderDataSource,
		NewMCPProvidersDataSource,
		NewMCPServerCardDataSource,
		NewOtelSinkProviderDataSource,
		NewWorkloadOAuthClientTestDataSource,
		NewWorkloadIdentityProviderTestDataSource,
		NewDataProtectionProfilesDataSource,
		NewDataProtectionProfileDataSource,
		NewDataProtectionDetectorsDataSource,
		NewDataProtectionDetectorDataSource,
	}
}

func stringOrEnv(v types.String, envKey string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return os.Getenv(envKey)
}

func boolOrEnv(v types.Bool, envKey string) bool {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueBool()
	}
	return os.Getenv(envKey) == "1" || os.Getenv(envKey) == "true"
}

// tlsSettings is the part of profileauth.Settings resolveConnection needs; an
// interface so the wiring can be tested without the machine's managed layer.
// settingsLoadDiagnostic names what went wrong reading the CLI's
// configuration. A profile or managed `endpoint` that is plain http:// is not
// a file that failed to read, and reporting it as one sends the user looking
// for a parse error in a file that parsed fine.
func settingsLoadDiagnostic(err error) (summary, detail string) {
	if errors.Is(err, profileauth.ErrCleartextEndpoint) {
		return "The profile's endpoint must use https", err.Error() +
			"\n\nAn admin token sent over http:// can be read and replayed by anyone on the network path. " +
			"Change the endpoint where it is set (`ferentin profiles set-endpoint`, or the managed configuration) to https://."
	}
	return "Failed to read shared config file", err.Error()
}

// cleartextSDKDiagnostic points a cleartext refusal from the SDK at the
// attribute that supplied the URL. `auth_url` is blamed only when the user SET
// it: a derived auth_url inherits the endpoint's scheme (deriveAuthURL keeps
// it), so a refusal of the derived value is fixed by changing `endpoint`, and
// pointing at an attribute the user never wrote sends them to the wrong line.
// The SDK's message already redacts userinfo and query, so it is shown as is.
func cleartextSDKDiagnostic(err error, userAuthURL bool) (path.Path, string, string, bool) {
	if !errors.Is(err, adminapi.ErrCleartextEndpoint) {
		return path.Path{}, "", "", false
	}
	// The SDK names the refused field in its message. Matching that name is
	// matching our own pinned SDK's text, so TestCleartextSDKDiagnostic runs the
	// real constructors: a bump that rewords it fails there, not in front of a
	// user reading the wrong attribute.
	attr := path.Root("endpoint")
	if userAuthURL && strings.Contains(err.Error(), "ClientCredentialsOptions.AuthURL") {
		attr = path.Root("auth_url")
	}
	return attr, "Credentials may only be sent over https", err.Error() +
		"\n\nUse an https:// URL. http:// is accepted only on a loopback address (127.0.0.1, [::1] or localhost) for a local stack.", true
}

type tlsSettings interface {
	CheckOptIn(explicit bool) error
	InsecureFor(target string, explicit bool) bool
}

// connection is where the provider connects and, per destination, whether it
// may skip certificate verification there.
type connection struct {
	endpoint       string
	adminInsecure  bool
	issuerInsecure func(target string) bool
}

// resolveConnection picks the admin endpoint (HCL / FERENTIN_ENDPOINT, then the
// profile's, then production) and answers TLS for each destination separately.
//
// One bool used to cover both the admin API and the issuer the profile's
// tokens refresh against, and the provider's own opt-in was ORed over the
// CLI's answer, so FERENTIN_INSECURE_SKIP_VERIFY=1 beat a managed
// `insecure: false` (ferentin-cli-app lessons 280 and 283). The opt-in is now
// an INPUT to the resolver, and refused outright where the managed layer
// forbids it, as the CLI refuses `--insecure`.
func resolveConnection(s tlsSettings, hclEndpoint, profileEndpoint string, explicit bool) (connection, error) {
	if err := s.CheckOptIn(explicit); err != nil {
		return connection{}, err
	}
	endpoint := hclEndpoint
	if endpoint == "" {
		endpoint = profileEndpoint
	}
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return connection{
		endpoint:      endpoint,
		adminInsecure: s.InsecureFor(endpoint, explicit),
		issuerInsecure: func(target string) bool {
			return s.InsecureFor(target, explicit)
		},
	}, nil
}
