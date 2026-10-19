package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
)

// The platform stamps managed_by, managed_by_client_id and managed_by_module
// once, at create (IacProvenanceSupport.stampForCreate), and carries them over
// unchanged on every update (stampForUpdate). Without UseStateForUnknown an
// update plans them as "(known after apply)", which reads as if an apply could
// change who created the object. Every resource that has them must keep state.
func TestProvenanceAttributesKeepState(t *testing.T) {
	ctx := context.Background()
	want := fmt.Sprintf("%T", stringplanmodifier.UseStateForUnknown())

	p := &FerentinProvider{}
	checked := 0
	for _, newResource := range p.Resources(ctx) {
		r := newResource()
		meta := &resource.MetadataResponse{}
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "ferentin"}, meta)
		resp := &resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, resp)

		for _, name := range []string{"managed_by", "managed_by_client_id", "managed_by_module"} {
			attr, ok := resp.Schema.Attributes[name].(schema.StringAttribute)
			if !ok {
				continue
			}
			checked++
			found := false
			for _, m := range attr.PlanModifiers {
				if fmt.Sprintf("%T", m) == want {
					found = true
				}
			}
			if !found {
				t.Errorf("%s.%s: missing UseStateForUnknown, so every update plans it as unknown", meta.TypeName, name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no resource has provenance attributes; the check is looking in the wrong place")
	}
}
