// Package provider implements the MetaMCP Terraform provider and its tests.
package provider

import (
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acceptance tests create real objects in a real MetaMCP, so they only run when
// TF_ACC is set. The unit tests elsewhere in this package and in internal/client
// cover the wire format and need nothing; these exist to prove the resources
// round-trip against a live server, which mocks cannot establish.
//
//	export TF_ACC=1
//	export METAMCP_ENDPOINT=https://metamcp.example.com
//	export METAMCP_EMAIL=...
//	export METAMCP_PASSWORD=...
//	go test ./internal/provider/... -v -run TestAcc
//
// Every name below is prefixed "tf-acc-" so test debris is obvious, and the
// framework destroys what each test creates.

func protoV6ProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"metamcp": providerserver.NewProtocol6WithError(New("test")()),
	}
}

// requireFakeOrLive returns the provider factories to use, starting a fake
// MetaMCP unless live mode was explicitly requested.
func requireFakeOrLive(t *testing.T) map[string]func() (tfprotov6.ProviderServer, error) {
	t.Helper()

	if os.Getenv("METAMCP_LIVE_ACC") == "" {
		return fakeProviderFactories(newFakeMetaMCP(t).URL)
	}

	for _, v := range []string{"METAMCP_ENDPOINT", "METAMCP_EMAIL", "METAMCP_PASSWORD"} {
		if os.Getenv(v) == "" {
			t.Fatalf("%s must be set when METAMCP_LIVE_ACC=1", v)
		}
	}
	// Live tests must not silently fall back to fake credentials.
	_ = os.Unsetenv("METAMCP_USERNAME")
	return protoV6ProviderFactories()
}

func TestAccNamespaceResource(t *testing.T) {
	factories := requireFakeOrLive(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "metamcp_namespace" "test" {
  name        = "tf-acc-namespace"
  description = "created by terraform-provider-metamcp acceptance tests"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("metamcp_namespace.test", "name", "tf-acc-namespace"),
					resource.TestCheckResourceAttrSet("metamcp_namespace.test", "uuid"),
					resource.TestCheckResourceAttrSet("metamcp_namespace.test", "created_at"),
				),
			},
			// Update, then confirm the value actually persisted rather than
			// only appearing in state.
			{
				Config: `
resource "metamcp_namespace" "test" {
  name        = "tf-acc-namespace"
  description = "updated by acceptance tests"
}`,
				Check: resource.TestCheckResourceAttr(
					"metamcp_namespace.test", "description", "updated by acceptance tests"),
			},
			// Import, proving the import path handles a real UUID.
			{
				ResourceName:                         "metamcp_namespace.test",
				ImportState:                          true,
				ImportStateIdFunc:                    importByUUID("metamcp_namespace.test"),
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "uuid",
			},
		},
	})
}

func TestAccMcpServerResource(t *testing.T) {
	factories := requireFakeOrLive(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "metamcp_mcp_server" "test" {
  name = "tf-acc-server"
  type = "STREAMABLE_HTTP"
  url  = "https://example.com/mcp"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("metamcp_mcp_server.test", "name", "tf-acc-server"),
					resource.TestCheckResourceAttr("metamcp_mcp_server.test", "type", "STREAMABLE_HTTP"),
					resource.TestCheckResourceAttrSet("metamcp_mcp_server.test", "uuid"),
				),
			},
		},
	})
}

// TestAccNamespaceServerAssociation proves the association round-trips: the
// server is created, attached by UUID, read back, then detached.
func TestAccNamespaceServerAssociation(t *testing.T) {
	factories := requireFakeOrLive(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "metamcp_mcp_server" "attached" {
  name = "tf-acc-attached"
  type = "STREAMABLE_HTTP"
  url  = "https://example.com/mcp"
}

resource "metamcp_namespace" "with_server" {
  name             = "tf-acc-assoc"
  mcp_server_uuids = [metamcp_mcp_server.attached.uuid]
}`,
				Check: resource.TestCheckResourceAttrPair(
					"metamcp_namespace.with_server", "mcp_server_uuids.0",
					"metamcp_mcp_server.attached", "uuid"),
			},
			// Detaching must be reflected, not ignored.
			{
				Config: `
resource "metamcp_mcp_server" "attached" {
  name = "tf-acc-attached"
  type = "STREAMABLE_HTTP"
  url  = "https://example.com/mcp"
}

resource "metamcp_namespace" "with_server" {
  name             = "tf-acc-assoc"
  mcp_server_uuids = []
}`,
				Check: resource.TestCheckResourceAttr(
					"metamcp_namespace.with_server", "mcp_server_uuids.#", "0"),
			},
		},
	})
}

func TestAccMcpServerStdioRequiresCommand(t *testing.T) {
	factories := requireFakeOrLive(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "metamcp_mcp_server" "test" {
  name = "tf-acc-stdio"
  type = "STDIO"
}`,
				ExpectError: regexp.MustCompile(`command is required when type is STDIO`),
			},
		},
	})
}

func TestAccEndpointAndAPIKey(t *testing.T) {
	factories := requireFakeOrLive(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "metamcp_namespace" "test" {
  name = "tf-acc-endpoint-ns"
}

resource "metamcp_endpoint" "test" {
  name           = "tf-acc-endpoint"
  namespace_uuid = metamcp_namespace.test.uuid
}

resource "metamcp_api_key" "test" {
  name = "tf-acc-key"
}

data "metamcp_endpoint" "by_name" {
  name       = metamcp_endpoint.test.name
  depends_on = [metamcp_endpoint.test]
}

data "metamcp_api_key" "by_name" {
  name       = metamcp_api_key.test.name
  depends_on = [metamcp_api_key.test]
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("metamcp_endpoint.test", "uuid"),
					resource.TestCheckResourceAttrSet("metamcp_endpoint.test", "url"),
					resource.TestCheckResourceAttr("metamcp_api_key.test", "is_active", "true"),
					// The key value must be populated at create time.
					resource.TestCheckResourceAttrSet("metamcp_api_key.test", "key"),
					// The data sources must agree with the resources they read.
					resource.TestCheckResourceAttrPair(
						"data.metamcp_endpoint.by_name", "uuid",
						"metamcp_endpoint.test", "uuid"),
					resource.TestCheckResourceAttrPair(
						"data.metamcp_api_key.by_name", "key",
						"metamcp_api_key.test", "key"),
				),
			},
		},
	})
}
