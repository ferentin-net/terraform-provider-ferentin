// Package provider implements the Ferentin Terraform provider on top of the
// terraform-plugin-framework v1.x runtime. The provider holds an
// *adminapi.SDKClient and a default tenant_id; resources pull both from
// req.ProviderData on Configure.
package provider

import (
	"context"
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
)

// envEndpoint, envTenantID, envToken, envInsecure, envClientID, envClientSecret,
// envAuthURL are the standard environment-variable fallbacks for SDKOptions
// equivalents on the provider block. Matches §3.2 of the design doc.
const (
	envEndpoint     = "FERENTIN_ENDPOINT"
	envTenantID     = "FERENTIN_TENANT_ID"
	envToken        = "FERENTIN_TOKEN"
	envInsecure     = "FERENTIN_INSECURE_SKIP_VERIFY"
	envClientID     = "FERENTIN_CLIENT_ID"
	envClientSecret = "FERENTIN_CLIENT_SECRET"
	envAuthURL      = "FERENTIN_AUTH_URL"
)

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
// These are belt-and-suspenders alongside the runtime checks in Configure():
// the runtime checks still cover the env-var fallback paths (which the
// framework can't see at plan time).
func (p *FerentinProvider) ConfigValidators(_ context.Context) []provider.ConfigValidator {
	return []provider.ConfigValidator{
		// token is mutually exclusive with the client_credentials pair.
		providervalidator.Conflicting(
			path.MatchRoot("token"),
			path.MatchRoot("client_id"),
		),
		providervalidator.Conflicting(
			path.MatchRoot("token"),
			path.MatchRoot("client_secret"),
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
				MarkdownDescription: "Admin-api base URL, e.g. `https://api.ferentin.net`. " +
					"Falls back to env `FERENTIN_ENDPOINT`.",
				Optional: true,
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Default tenant UUID for tenant-scoped resources. Each resource may " +
					"override via its own `tenant_id` attribute. Falls back to env `FERENTIN_TENANT_ID`.",
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
				MarkdownDescription: "Authorization server base URL (used with `client_id` / `client_secret`). " +
					"Falls back to env `FERENTIN_AUTH_URL`. Defaults to `endpoint` with `auth.` substituted " +
					"for `api.` (e.g. `https://auth.ferentin.net` for endpoint `https://api.ferentin.net`).",
				Optional: true,
			},
			"insecure_skip_verify": schema.BoolAttribute{
				MarkdownDescription: "Skip TLS verification. Local-dev only; do NOT set in production. " +
					"Falls back to env `FERENTIN_INSECURE_SKIP_VERIFY=1`.",
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
	insecure := boolOrEnv(data.InsecureSkipVerify, envInsecure)

	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Missing admin-api endpoint",
			"Set the `endpoint` attribute on the provider block, or export FERENTIN_ENDPOINT.",
		)
	}
	if tenantID == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("tenant_id"),
			"Missing tenant ID",
			"Set the `tenant_id` attribute on the provider block, or export FERENTIN_TENANT_ID. "+
				"Individual resources may also set `tenant_id` to override this default.",
		)
	}

	// Auth-mode resolution: client_credentials beats static token when both
	// halves of CC are present. Mixing token + (client_id|client_secret) is
	// ambiguous — fail closed.
	ccPresent := clientID != "" || clientSecret != ""
	if token != "" && ccPresent {
		resp.Diagnostics.AddError(
			"Conflicting auth configuration",
			"Set either `token` (static / pre-minted) or `client_id`+`client_secret` "+
				"(OAuth2 client_credentials) — not both.",
		)
	}
	if !ccPresent && token == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Missing auth credentials",
			"Configure either `token` (static bearer; FERENTIN_TOKEN) or "+
				"`client_id`+`client_secret` (OAuth2; FERENTIN_CLIENT_ID / FERENTIN_CLIENT_SECRET).",
		)
	}
	if ccPresent && (clientID == "" || clientSecret == "") {
		resp.Diagnostics.AddError(
			"Incomplete client_credentials configuration",
			"Both `client_id` and `client_secret` are required when using OAuth2 client_credentials auth.",
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	opts := adminapi.SDKOptions{
		Endpoint:  endpoint,
		SkipTLS:   insecure,
		UserAgent: "terraform-provider-ferentin/" + p.version,
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
		err      error
		authMode string
	)
	if ccPresent {
		// Default auth_url: derive from endpoint by swapping `api.` → `auth.`.
		// This is the platform's canonical pairing (api.ferentin.net pairs
		// with auth.ferentin.net). If the user wants something else, they
		// can set auth_url explicitly.
		if authURL == "" {
			authURL = deriveAuthURL(endpoint)
		}
		if authURL == "" {
			resp.Diagnostics.AddAttributeError(
				path.Root("auth_url"),
				"Cannot derive auth URL from endpoint",
				"Set `auth_url` explicitly, or export FERENTIN_AUTH_URL. "+
					"Endpoint did not contain `api.` for the default `api.→auth.` substitution.",
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
	} else {
		opts.Token = token
		sdk, err = adminapi.NewWithToken(opts)
		authMode = "static_token"
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to build admin-api SDK client", err.Error())
		return
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

// deriveAuthURL returns the platform's canonical auth-server URL for a given
// admin-api endpoint by substituting `api.` with `auth.`. Returns "" if the
// substitution doesn't apply — e.g. localhost endpoints or non-standard hosts
// require an explicit `auth_url`.
func deriveAuthURL(endpoint string) string {
	const marker = "://api."
	if i := strings.Index(endpoint, marker); i >= 0 {
		return endpoint[:i] + "://auth." + endpoint[i+len(marker):]
	}
	return ""
}

func (p *FerentinProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewEdgeSiteResource,
		NewLLMProviderInstanceResource,
		NewLLMPolicyResource,
		NewMCPServerResource,
		NewMCPProviderResource,
		NewMCPPolicyResource,
		NewOtelSinkResource,
		NewOtelPolicyResource,
		NewAIAgentResource,
	}
}

func (p *FerentinProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewLLMProviderDataSource,
		NewMCPProviderDataSource,
		NewMCPProvidersDataSource,
		NewMCPServerCardDataSource,
		NewOtelSinkProviderDataSource,
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
