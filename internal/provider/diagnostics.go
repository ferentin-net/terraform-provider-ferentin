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
//   - ErrRateLimited    (429+retry-exhaust) → "platform rate-limiting;
//     widen retry budget or run plan with -parallelism=1"
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
			"The principal authenticated successfully but lacks scope for this operation. "+
				"Tenant-admin operations require `scope=admin` in the issued token; verify the "+
				"service account binding includes the admin role.\n\nDetails: "+err.Error(),
		)
	case errors.Is(err, adminapi.ErrConflict):
		diags.AddError(
			fmt.Sprintf("%s — conflict with existing resource", op),
			"The platform rejected this request as conflicting with existing state. Most commonly: "+
				"the name / slug / endpoint is already in use by another resource in the same tenant. "+
				"Pick a unique value or import the existing resource instead.\n\nDetails: "+err.Error(),
		)
	case errors.Is(err, adminapi.ErrRateLimited):
		diags.AddError(
			fmt.Sprintf("%s — rate-limited", op),
			"The platform's per-tenant rate limit was exceeded and the transport's retry budget "+
				"didn't clear it. Re-run `terraform apply -parallelism=1` to serialize requests, "+
				"or wait a minute and retry.\n\nDetails: "+err.Error(),
		)
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
