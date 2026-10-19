package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// The provider overview (templates/index.md.tmpl) lists every resource, data
// source and guide by hand, grouped by area, because tfplugindocs generates no
// index of its own. Without this a new one would ship missing from the page
// readers start on.
func TestDocsIndexListsEverything(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile(filepath.Join("..", "..", "templates", "index.md.tmpl"))
	if err != nil {
		t.Fatalf("read the overview template: %v", err)
	}
	index := string(raw)
	p := &FerentinProvider{}

	for _, newResource := range p.Resources(ctx) {
		meta := &resource.MetadataResponse{}
		newResource().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "ferentin"}, meta)
		if link := "](resources/" + strings.TrimPrefix(meta.TypeName, "ferentin_") + ".md)"; !strings.Contains(index, link) {
			t.Errorf("templates/index.md.tmpl does not link resource %s (%s)", meta.TypeName, link)
		}
	}
	for _, newDataSource := range p.DataSources(ctx) {
		meta := &datasource.MetadataResponse{}
		newDataSource().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "ferentin"}, meta)
		if link := "](data-sources/" + strings.TrimPrefix(meta.TypeName, "ferentin_") + ".md)"; !strings.Contains(index, link) {
			t.Errorf("templates/index.md.tmpl does not link data source %s (%s)", meta.TypeName, link)
		}
	}

	guides, err := filepath.Glob(filepath.Join("..", "..", "templates", "guides", "*.md.tmpl"))
	if err != nil || len(guides) == 0 {
		t.Fatalf("no guide templates found (%v)", err)
	}
	for _, g := range guides {
		name := strings.TrimSuffix(filepath.Base(g), ".md.tmpl")
		if link := "](guides/" + name + ".md)"; !strings.Contains(index, link) {
			t.Errorf("templates/index.md.tmpl does not link guide %s (%s)", name, link)
		}
	}
}
