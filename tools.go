//go:build tools

// Package tools pins build-time tooling so `go generate` and CI use the same
// versions without a separate install step.
package tools

import (
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)
