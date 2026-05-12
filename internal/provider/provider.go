// Package provider implements the Ferentin Terraform provider on top of the
// terraform-plugin-framework v1.x runtime. The provider holds an
// *adminapi.SDKClient and a default tenant_id; resources pull both from
// req.ProviderData on Configure.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// envEndpoint, envTenantID, envToken, envInsecure are the standard
// environment-variable fallbacks for SDKOptions equivalents on the provider
// block. Matches §3.2 of the design doc.
const (
	envEndpoint = "FERENTIN_ENDPOINT"
	envTenantID = "FERENTIN_TENANT_ID"
	envToken    = "FERENTIN_TOKEN"
	envInsecure = "FERENTIN_INSECURE_SKIP_VERIFY"
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
type FerentinProviderModel struct {
	Endpoint           types.String `tfsdk:"endpoint"`
	TenantID           types.String `tfsdk:"tenant_id"`
	Token              types.String `tfsdk:"token"`
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

// Compile-time assertion that we satisfy the framework interface.
var _ provider.Provider = (*FerentinProvider)(nil)

func (p *FerentinProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "ferentin"
	resp.Version = p.version
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
				MarkdownDescription: "Pre-minted bearer access token. Suitable for tests and CI; production " +
					"deployments should use the `ferentin admin clients` workflow to mint a service-account " +
					"client and rely on the upcoming client-credentials refresh path. Falls back to env " +
					"`FERENTIN_TOKEN`. Marked sensitive so it does not appear in plan/apply output.",
				Optional:  true,
				Sensitive: true,
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
	insecure := boolOrEnv(data.InsecureSkipVerify, envInsecure)

	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Missing admin-api endpoint",
			"Set the `endpoint` attribute on the provider block, or export FERENTIN_ENDPOINT.",
		)
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Missing bearer token",
			"Set the `token` attribute on the provider block, or export FERENTIN_TOKEN. "+
				"Token-source-based auth (NewWithClientCredentials) is on the roadmap.",
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
	if resp.Diagnostics.HasError() {
		return
	}

	sdk, err := adminapi.NewWithToken(adminapi.SDKOptions{
		Endpoint:  endpoint,
		Token:     token,
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
	})
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
	})
}

func (p *FerentinProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewEdgeSiteResource,
		NewLLMProviderInstanceResource,
		NewLLMPolicyResource,
	}
}

func (p *FerentinProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewLLMProviderDataSource,
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
