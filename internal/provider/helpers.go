// Package-level conversion helpers shared across every resource and data
// source. Two directions:
//   - "ToTF" / "OrDefault" / "ToList" — SDK response → Terraform-state types
//   - "set*Ptr" / "ToSDK" / "Parse*" — Terraform-config → SDK input types
//
// Keeping these in one file lets new sub-clients copy the canonical pattern
// without grepping across the package.
package provider

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// -------------------------------------------------------------------------
// Parse / validate inbound (Terraform config → SDK input)
// -------------------------------------------------------------------------

// parseUUID converts a string into the openapi_types.UUID alias used by gen
// types. Returns an error if the string isn't a valid UUID.
func parseUUID(s string) (openapi_types.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return openapi_types.UUID{}, err
	}
	return openapi_types.UUID(u), nil
}

// setStringPtr copies a Terraform String into an outbound *string, leaving
// nil when the value is Null or Unknown.
func setStringPtr(in types.String, out **string) {
	if in.IsNull() || in.IsUnknown() {
		return
	}
	v := in.ValueString()
	*out = &v
}

// setBoolPtr is the bool analog of setStringPtr.
func setBoolPtr(in types.Bool, out **bool) {
	if in.IsNull() || in.IsUnknown() {
		return
	}
	v := in.ValueBool()
	*out = &v
}

// setInt32Ptr converts a Terraform Int64 to an SDK *int32. Terraform's
// integer primitive is always 64-bit; we narrow on send.
func setInt32Ptr(in types.Int64, out **int32) {
	if in.IsNull() || in.IsUnknown() {
		return
	}
	v := int32(in.ValueInt64())
	*out = &v
}

// stringListToSDK converts a Terraform types.List of strings into a []string
// suitable for SDK inputs. Returns an empty (non-nil) slice for Null /
// Unknown lists — the platform's required `[]string` schemas distinguish
// "explicitly empty" from "missing" and we always want the former for
// resources we manage.
func stringListToSDK(ctx context.Context, l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return []string{}
	}
	var out []string
	_ = l.ElementsAs(ctx, &out, false)
	return out
}

// -------------------------------------------------------------------------
// To-Terraform-state (SDK response → Terraform model)
// -------------------------------------------------------------------------

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
// readers can tell whether the field is logically optional (Null OK) or
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

// stringSliceToList converts a *[]string from the SDK into a types.List of
// strings. Nil maps to ListNull(StringType). Empty slice maps to an empty
// list (not Null) — preserves the platform's "explicitly empty vs missing"
// distinction.
func stringSliceToList(p *[]string) types.List {
	if p == nil {
		return types.ListNull(types.StringType)
	}
	elems := make([]attr.Value, 0, len(*p))
	for _, s := range *p {
		elems = append(elems, types.StringValue(s))
	}
	lv, _ := types.ListValue(types.StringType, elems)
	return lv
}
