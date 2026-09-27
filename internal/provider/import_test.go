package provider

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Import tests run against an in-process fake MetaMCP, so they need no live
// instance and no credentials. They use the real Terraform plugin protocol,
// which is the only way to prove the ImportState wiring actually works — the
// client-level tests cannot see it.
//
// The pattern is: create a resource through Terraform, then import it into a
// fresh state and verify every attribute matches what was created. That catches
// the usual import bug, where a computed or defaulted attribute is left unset
// and the next plan proposes a spurious change.

func fakeProviderFactories(endpoint string) map[string]func() (tfprotov6.ProviderServer, error) {
	// These tests use the real Terraform plugin protocol but an in-process
	// fake, so they touch no network and need no credentials. TestMain sets
	// TF_ACC below for exactly that reason.
	//
	// Endpoint and credentials come from the environment so the test config
	// stays free of connection details.
	_ = os.Setenv("METAMCP_ENDPOINT", endpoint)
	_ = os.Setenv("METAMCP_EMAIL", "test@example.com")
	_ = os.Setenv("METAMCP_PASSWORD", "not-a-real-password")
	return map[string]func() (tfprotov6.ProviderServer, error){
		"metamcp": providerserver.NewProtocol6WithError(New("test")()),
	}
}

// importByUUID reads the uuid attribute out of state and uses it as the import
// ID. The resources have no separate id attribute (the uuid is the identity),
// and resource.Test defaults the import ID to the state id, which is empty here.
func importByUUID(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		return rs.Primary.Attributes["uuid"], nil
	}
}

func TestImportNamespace(t *testing.T) {
	fake := newFakeMetaMCP(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(fake.URL),
		Steps: []resource.TestStep{
			{
				Config: `
resource "metamcp_namespace" "test" {
  name        = "imported"
  description = "for import"
}`,
				Check: resource.TestCheckResourceAttrSet("metamcp_namespace.test", "uuid"),
			},
			{
				ResourceName:      "metamcp_namespace.test",
				ImportState:       true,
				ImportStateIdFunc: importByUUID("metamcp_namespace.test"),
				ImportStateVerify: true,
				// These resources expose no separate id attribute; the uuid is
				// the identity, so verification compares that instead.
				ImportStateVerifyIdentifierAttribute: "uuid",
			},
		},
	})
}

// TestImportNamespaceWithServers is the important one: the association must
// survive an import, because import is exactly where a servers array that only
// namespaces.get returns would otherwise be missed.
func TestImportNamespaceWithServers(t *testing.T) {
	fake := newFakeMetaMCP(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(fake.URL),
		Steps: []resource.TestStep{
			{
				Config: `
resource "metamcp_mcp_server" "one" {
  name = "import-one"
  type = "STREAMABLE_HTTP"
  url  = "https://example.com/mcp"
}

resource "metamcp_namespace" "test" {
  name             = "imported-assoc"
  mcp_server_uuids = [metamcp_mcp_server.one.uuid]
}`,
			},
			{
				ResourceName:      "metamcp_namespace.test",
				ImportState:       true,
				ImportStateIdFunc: importByUUID("metamcp_namespace.test"),
				ImportStateVerify: true,
				// These resources expose no separate id attribute; the uuid is
				// the identity, so verification compares that instead.
				ImportStateVerifyIdentifierAttribute: "uuid",
			},
		},
	})
}

func TestImportMcpServer(t *testing.T) {
	fake := newFakeMetaMCP(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(fake.URL),
		Steps: []resource.TestStep{
			{
				Config: `
resource "metamcp_mcp_server" "test" {
  name = "import-server"
  type = "STREAMABLE_HTTP"
  url  = "https://example.com/mcp"
}`,
			},
			{
				ResourceName:                         "metamcp_mcp_server.test",
				ImportState:                          true,
				ImportStateIdFunc:                    importByUUID("metamcp_mcp_server.test"),
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "uuid",
			},
		},
	})
}

// TestImportRejectsNonUUID covers the guard: a mistyped import ID should produce
// a clear error, not a confusing failure later.
func TestImportRejectsNonUUID(t *testing.T) {
	fake := newFakeMetaMCP(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(fake.URL),
		Steps: []resource.TestStep{
			{
				Config: `
resource "metamcp_namespace" "test" {
  name = "guard"
}`,
			},
			{
				ResourceName:  "metamcp_namespace.test",
				ImportState:   true,
				ImportStateId: "not-a-uuid",
				ExpectError:   regexp.MustCompile(`Expected a namespace UUID`),
			},
		},
	})
}
