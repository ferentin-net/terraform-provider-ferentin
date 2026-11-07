package provider

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
)

// One endpoint posture row per scope (the tenant default, or a device group)
// is written by two resources: ferentin_endpoint_policy_settings (network
// posture, gateway, agent action, call-time enforcement) and
// ferentin_mcp_scope_defaults (the MCP scope defaults, an API over the same
// row's MCP columns). Both send If-Match with the row's version, and the
// platform keys that precondition on the row ("endpoint-policy-settings/<t>[/<g>]"),
// so a write by either one, or a console edit to any other column of the row,
// makes the other's next write a 412 even though they touch different columns.
//
// retryStalePostureVersion keeps the precondition meaningful without that
// false conflict. On a 412 it re-reads the scope and asks which of the fields
// THIS write sends changed since the resource last read them. None: the
// version moved for a column this resource does not own, so the write is
// retried once with the fresh version (both writes are partial, so nothing the
// other writer set is overwritten). Some: someone else changed what this apply
// is about to overwrite, which is the conflict If-Match exists to catch, so it
// is surfaced as a postureConflictError naming them.
//
// version "" means the write sent no If-Match, so a 412 cannot be a stale
// version and is returned as is.
func retryStalePostureVersion[T any](version string, write func(version string) (T, error),
	reread func() (freshVersion string, changed []string, err error)) (T, error) {
	out, err := write(version)
	if err == nil || version == "" || !errors.Is(err, adminapi.ErrPreconditionFailed) {
		return out, err
	}
	var zero T
	fresh, changed, rerr := reread()
	if rerr != nil {
		return zero, fmt.Errorf("%w (re-reading the posture row to check what changed failed: %v)", err, rerr)
	}
	if len(changed) > 0 {
		return zero, &postureConflictError{fields: changed, cause: err}
	}
	return write(fresh)
}

// postureConflictError is a 412 on the shared posture row where a field this
// write sends was changed elsewhere since Terraform last read it.
type postureConflictError struct {
	fields []string
	cause  error
}

func (e *postureConflictError) Error() string {
	return fmt.Sprintf("changed since Terraform last read it: %s: %v", strings.Join(e.fields, ", "), e.cause)
}

func (e *postureConflictError) Unwrap() error { return e.cause }

// addPostureConflictError reports err when it is a postureConflictError and
// returns true; otherwise it adds nothing and returns false.
func addPostureConflictError(diags *diag.Diagnostics, op string, err error) bool {
	var conflict *postureConflictError
	if !errors.As(err, &conflict) {
		return false
	}
	diags.AddError(op+" — changed elsewhere since the last refresh",
		fmt.Sprintf("The platform refused the write (412): %s on this scope's posture row changed since "+
			"Terraform last read it, so applying would overwrite that change without showing it. Run "+
			"`terraform plan` again to see the change, then apply.\n\nDetails: %v",
			strings.Join(conflict.fields, ", "), conflict.cause))
	return true
}

// changedFields compares attribute values pairwise and returns the names whose
// value differs.
type ownedField struct {
	name         string
	prior, fresh attr.Value
}

func changedFields(fields []ownedField) []string {
	var out []string
	for _, f := range fields {
		if f.prior == nil || f.fresh == nil {
			continue
		}
		if !f.prior.Equal(f.fresh) {
			out = append(out, f.name)
		}
	}
	return out
}

// versionString is the If-Match value for a version in state: "" (no
// precondition) when there is none.
func versionString(v types.Int64) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return strconv.FormatInt(v.ValueInt64(), 10)
}

// int64PtrString is versionString for a version on a response.
func int64PtrString(v *int64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(*v, 10)
}
