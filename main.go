// Entry point for the Ferentin Terraform provider. Wired to
// internal/provider.New; the executable is named after the binary protocol
// HashiCorp publishes via the registry.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/ferentin-net/terraform-provider-ferentin/internal/provider"
)

// version is overridden by goreleaser at link time:
//
//	go build -ldflags="-X main.version=v0.1.0"
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		// Must match the source declared in HCL: `source = "ferentin-net/ferentin"`.
		Address: "registry.terraform.io/ferentin-net/ferentin",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
