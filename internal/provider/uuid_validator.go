package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// policyInstanceRefValidator rejects a policy `provider_instances` entry that
// is not a UUID.
//
// Both policy resources take instance UUIDs, and the platform normalizes a
// name to the instance's UUID on write while echoing UUIDs on read. Terraform
// then fails the apply with "Provider produced inconsistent result after
// apply: was cty.StringVal(\"my-instance\"), but now
// cty.StringVal(\"c3c03e4f-...\")" — which reads as a provider bug and is not
// one, and which arrives only after the policy has already been created
// server-side.
//
// Catching it at plan turns a confusing post-write failure into a message that
// names the attribute to reference instead. Unknown values (a reference to a
// resource not yet created) are skipped by the framework before this runs, so
// this only ever fires on a literal or an already-known value — which is
// exactly the case that would otherwise fail.
type policyInstanceRefValidator struct {
	// attrHint is the attribute to point the operator at, e.g.
	// "ferentin_llm_provider.<name>.instance_id".
	attrHint string
}

var _ validator.String = policyInstanceRefValidator{}

func (v policyInstanceRefValidator) Description(_ context.Context) string {
	return fmt.Sprintf("must be a UUID — reference %s", v.attrHint)
}

func (v policyInstanceRefValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v policyInstanceRefValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	if _, err := parseUUID(value); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"provider_instances entry must be a UUID",
			fmt.Sprintf(
				"%q is not a UUID. The platform accepts an instance NAME here for backward "+
					"compatibility but stores and returns the resolved UUID, so a name-based config "+
					"fails the apply with \"Provider produced inconsistent result after apply\" — "+
					"after the policy has already been written.\n\n"+
					"Reference %s instead.",
				value, v.attrHint,
			),
		)
	}
}
