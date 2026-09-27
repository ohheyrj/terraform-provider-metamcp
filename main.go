// Command terraform-provider-metamcp is the Terraform provider plugin binary.
// It is not run directly: Terraform executes it as a plugin over gRPC.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/provider"
)

// version is set at build time with
// -ldflags "-X main.version=<version>". It is also reported by the provider's
// user agent, so it should be a real release or "dev".
var version = "dev"

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false,
		"set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/ohheyrj/metamcp",
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err.Error())
	}
}
