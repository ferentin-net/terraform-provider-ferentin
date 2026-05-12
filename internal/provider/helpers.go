package provider

import (
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// parseUUID converts a string into the openapi_types.UUID alias used by gen
// types. Returns an error if the string isn't a valid UUID.
func parseUUID(s string) (openapi_types.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return openapi_types.UUID{}, err
	}
	return openapi_types.UUID(u), nil
}

// strPtrToTF converts a *string from the SDK into a types.String. Nil maps
// to Null (which the framework treats as "absent from response"); empty
// string maps to StringValue("").
func strPtrToTF(p *string) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(*p)
}

// strPtrOrDefault is identical to strPtrToTF — kept as a separate name so
// reader can tell whether the field is logically optional (Null OK) or
// expected to be populated.
func strPtrOrDefault(p *string) types.String {
	return strPtrToTF(p)
}

// boolPtrOrDefault converts a *bool into a types.Bool. Nil maps to Null —
// which round-trips through Terraform state correctly so a future read
// doesn't trigger a spurious diff.
func boolPtrOrDefault(p *bool) types.Bool {
	if p == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*p)
}

// int32PtrToTF converts a *int32 into a types.Int64. Terraform-plugin-framework
// uses Int64 as the integer primitive; we cast on input and output.
func int32PtrToTF(p *int32) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*p))
}

// int64PtrToTF converts a *int64 into a types.Int64.
func int64PtrToTF(p *int64) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*p)
}

// timePtrToTF converts a *time.Time into a types.String in RFC 3339 form.
// Terraform doesn't have a native time primitive in the framework; we
// stringify for state portability.
func timePtrToTF(p *time.Time) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(p.Format(time.RFC3339Nano))
}

// enumPtrToTF stringifies a generic enum-like newtype (e.g.,
// gen.EdgeSiteResponseStatus, which is `type X string`). The Go generic
// constraint allows any underlying string type.
func enumPtrToTF[T ~string](p *T) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(string(*p))
}
