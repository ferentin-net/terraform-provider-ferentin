package provider

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// examplesVersions is added to an example directory that declares no
// required_providers of its own. The per-resource and per-data-source examples
// are snippets tfplugindocs embeds in the docs, so they carry no terraform
// block, and a standalone `terraform validate` cannot resolve the provider
// without one.
const examplesVersions = `terraform {
  required_providers {
    ferentin = {
      source = "ferentin-net/ferentin"
    }
  }
}
`

// Validate every directory under examples/ against the real provider schema.
//
// These are the configurations the registry docs show and the scenario
// examples people copy, and before this nothing checked that they still
// validated: CHANGELOG records examples that used a strategy that does not
// exist and enum values in the wrong case. terraform fmt -check covered only
// their formatting.
//
// Each example is copied into a temp directory first, so a snippet can be
// given the terraform block it lacks without touching the committed file.
func TestExamplesValidate(t *testing.T) {
	tfBin, cliConfig := devOverrideTerraform(t)

	dirs := exampleDirs(t, filepath.Join("..", "..", "examples"))
	if len(dirs) == 0 {
		t.Fatal("found no example directories; the walk is looking in the wrong place")
	}

	for _, src := range dirs {
		name := filepath.ToSlash(strings.TrimPrefix(src, filepath.Join("..", "..")+string(filepath.Separator)))
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			declaresProviders := copyExample(t, src, dir)
			if !declaresProviders {
				if err := os.WriteFile(filepath.Join(dir, "versions.tf"), []byte(examplesVersions), 0o600); err != nil {
					t.Fatalf("write versions.tf: %v", err)
				}
			}
			if out, err := terraformValidate(tfBin, cliConfig, dir); err != nil {
				t.Errorf("terraform validate failed for %s: %v\n%s", name, err, out)
			}
		})
	}
}

// exampleDirs returns every directory under root that holds a .tf file.
func exampleDirs(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".tf") {
			seen[filepath.Dir(path)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}

// copyExample copies src (recursively, so a file() reference into a
// subdirectory still resolves) into dst and reports whether any .tf file in it
// declares required_providers.
func copyExample(t *testing.T, src, dst string) (declaresProviders bool) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		// Skip anything a local `terraform init` or apply left behind.
		if strings.HasPrefix(rel, ".terraform") || strings.HasSuffix(rel, ".tfstate") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".tf") && strings.Contains(string(data), "required_providers") {
			declaresProviders = true
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
	return declaresProviders
}
