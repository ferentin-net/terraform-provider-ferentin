package provider

import (
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// addSDKError converts an SDK error into a rich, actionable Terraform
// diagnostic with operation context and remediation hints when the SDK
// sentinel makes one obvious. Routes:
//
//   - ErrPreconditionFailed (412) → "stale state, run terraform refresh"
//   - ErrUnauthorized   (401)     → "auth expired / wrong credentials"
//   - ErrForbidden      (403)     → "principal lacks scope for op"
//   - ErrConflict       (409)     → "name/slug already taken; pick another"
//   - ErrRateLimited    (429+retry-exhaust) → which budget refused it, and
//     whether -parallelism=1 can help (see rateLimitedDetail)
//   - ErrServer         (5xx)     → "transient or platform incident"
//
// All other errors (including network failures and ErrNotFound that the
// caller didn't intercept) fall through to a generic "Failed to <op>"
// diagnostic with the underlying message verbatim.
//
// Pattern: caller does the errors.Is(err, ErrNotFound) themselves when they
// want resource-removal behavior; addSDKError is for the catch-all path.
func addSDKError(diags *diag.Diagnostics, op string, err error) {
	switch {
	case errors.Is(err, adminapi.ErrPreconditionFailed):
		diags.AddError(
			fmt.Sprintf("%s — state out of date", op),
			"The platform's version of this resource has changed since Terraform last refreshed. "+
				"Run `terraform refresh` (or `terraform plan -refresh-only`) and re-apply to pick up "+
				"out-of-band edits, then retry.\n\nDetails: "+err.Error(),
		)
	case errors.Is(err, adminapi.ErrUnauthorized):
		diags.AddError(
			fmt.Sprintf("%s — authentication failed", op),
			"The platform rejected the bearer token. If you're using a static `token`, it has likely "+
				"expired (admin tokens live ~15 minutes — prefer the `client_id`+`client_secret` block "+
				"for automatic refresh). If you're using client_credentials, check that `client_id`, "+
				"`client_secret`, and `auth_url` match the target tenant.\n\nDetails: "+err.Error(),
		)
	case errors.Is(err, adminapi.ErrForbidden):
		diags.AddError(
			fmt.Sprintf("%s — insufficient permissions", op),
			"The principal authenticated successfully but lacks scope for this operation.\n\n"+
				"There is no global `admin` scope that covers the tenant-admin API — each resource "+
				"family has its own, and the token needs the specific one:\n\n"+
				"* edge sites — `edge:rw`\n"+
				"* LLM provider instances — `provider:rw`\n"+
				"* MCP providers / servers — `mcp:servers:rw`\n"+
				"* policies (LLM, MCP, data protection, endpoint) — `policy:rw`\n"+
				"* OTEL sinks and policies — `otel:rw`\n"+
				"* AI agents — `clients:agent:rw`\n"+
				"* workload OAuth clients and identity providers — `idps:rw`\n"+
				"* device groups — `devices:groups:rw` or `devices:rw`\n\n"+
				"Read operations take the matching `:r`. Check the role bound to this service "+
				"account, not the token's audience.\n\nDetails: "+err.Error(),
		)
	case errors.Is(err, adminapi.ErrConflict):
		diags.AddError(
			fmt.Sprintf("%s — conflict with existing resource", op),
			"The platform rejected this request as conflicting with existing state. Most commonly: "+
				"the name / slug / endpoint is already in use by another resource in the same tenant. "+
				"Pick a unique value or import the existing resource instead.\n\nDetails: "+err.Error(),
		)
	case errors.Is(err, adminapi.ErrRateLimited):
		diags.AddError(fmt.Sprintf("%s — rate-limited", op), rateLimitedDetail(err))
	case errors.Is(err, adminapi.ErrServer):
		diags.AddError(
			fmt.Sprintf("%s — platform error", op),
			"The admin-api returned 5xx after the transport's retry budget. This is usually "+
				"transient — re-run after ~30s. If it persists, check the platform status page or "+
				"contact support with the request ID below.\n\nDetails: "+err.Error(),
		)
	default:
		diags.AddError(op, err.Error())
	}
}

// rateLimitedDetail explains a 429 the transport's retries did not clear. The
// remedy depends on the budget that refused it (platform #3335): an endpoint
// category's per-minute window clears by spreading the requests out, but the
// tenant's hourly write budget, and this principal's share of it, refuse every
// write until the hour slides on, so -parallelism=1 would only make the apply
// slower to fail.
func rateLimitedDetail(err error) string {
	r, _ := adminapi.RateLimitDetails(err)
	wait := ""
	if r.RetryAfter > 0 {
		wait = fmt.Sprintf(" The platform asked for at least %s before the next try.", r.RetryAfter)
	}
	if r.WriteWide() {
		return fmt.Sprintf("Every write to this tenant draws on the write budget %q, and it is spent, "+
			"so the platform refuses every create, update and delete until it refills. It is an "+
			"hourly budget, so lowering -parallelism does not help: wait, or spread large applies "+
			"over time. Plans and refreshes still work. Resources Terraform already applied are "+
			"in state, so re-running the apply carries on from here.%s\n\nDetails: %s",
			r.Category, wait, err.Error())
	}
	bucket := "A platform rate limit"
	if r.Category != "" {
		bucket = fmt.Sprintf("The platform's rate limit %q", r.Category)
	}
	return fmt.Sprintf("%s was exceeded and the transport's retries did not clear it. Re-run "+
		"`terraform apply -parallelism=1` to serialize requests, or wait a minute and retry.%s"+
		"\n\nDetails: %s", bucket, wait, err.Error())
}
