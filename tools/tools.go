//go:build tools

// Package tools tracks build-time CLI dependencies in go.mod so they're
// version-pinned and `go run`-invocable from the Makefile. The build tag
// excludes this file from the regular build — it only exists to keep
// `go mod tidy` from pruning these modules.
package tools

import (
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)
