package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// metamcp_endpoint exposes is_public the same way metamcp_namespace and
// metamcp_mcp_server do: MetaMCP has no visibility column, so a null user_id
// means public. These tests pin the three directions AND the one hard limit —
// ownership cannot be changed after creation, because the API's endpoints.update
// does not accept the field at all.
//
// The failure modes are silent, which is why each direction is named:
//
//   - sending "" instead of an explicit null is a foreign-key violation, since
//     user_id references users.id and "" matches no row
//   - omitting the attribute must claim the caller, never default to public
//   - attempting an in-place visibility change looks like it worked, then fails
//     the apply with "inconsistent result after apply" once the resource has
//     already been touched

// endpointVisibilityConfig is the shared fixture: a public namespace (a public
// endpoint may only publish a public one) and an endpoint whose visibility the
// caller supplies.
func endpointVisibilityConfig(isPublic string) string {
	visibility := ""
	if isPublic != "" {
		visibility = "\n  is_public      = " + isPublic
	}
	return `
resource "metamcp_namespace" "public_ns" {
  name      = "public-ns"
  is_public = true
}

resource "metamcp_endpoint" "test" {
  name           = "visibility-ep"
  namespace_uuid = metamcp_namespace.public_ns.uuid` + visibility + `
}`
}

// TestEndpointCreateSendsVisibility is the wire-format guard. It asserts on what
// reached the API rather than on state, because the sentinel is what matters:
// the fake panics if user_id arrives as "", so a passing run also proves the
// empty-string bug is absent.
func TestEndpointCreateSendsVisibility(t *testing.T) {
	for _, tc := range []struct {
		name       string
		isPublic   string
		wantPublic bool
	}{
		{"explicit true is public", "true", true},
		{"explicit false is private", "false", false},
		// Omitted must NOT widen access: the API defaults an absent user_id to
		// the authenticated user, so the endpoint is created private.
		{"omitted is private, not public", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMetaMCP(t)

			var sawCreate map[string]any
			f.onEndpointWrite = func(proc string, in map[string]any) {
				if proc == "endpoints.create" {
					sawCreate = in
				}
			}

			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
				Steps: []resource.TestStep{
					{
						Config: endpointVisibilityConfig(tc.isPublic),
						Check: resource.TestCheckResourceAttr(
							"metamcp_endpoint.test", "is_public",
							map[bool]string{true: "true", false: "false"}[tc.wantPublic]),
					},
				},
			})

			if sawCreate == nil {
				t.Fatal("endpoints.create was never called")
			}
			// A present key that is nil is an explicit JSON null; an absent key
			// means the API's own default applies, which is the caller's id.
			owner, present := sawCreate["user_id"]
			switch {
			case tc.wantPublic && (!present || owner != nil):
				t.Errorf("public sent user_id = %#v (present=%v), want an explicit null", owner, present)
			case !tc.wantPublic && present && owner == nil:
				t.Error("private sent an explicit null, which makes it public")
			case !tc.wantPublic && present && owner == "":
				t.Error(`private sent "", which is a foreign-key violation, not a user id`)
			}
		})
	}
}

// TestEndpointVisibilityChangeReplaces proves the provider asks for a
// replacement rather than attempting an in-place change the API cannot perform.
//
// Without the plan modifier Terraform would send the new ownership, the API
// would ignore it (its update procedure never passes user_id to the repository),
// and the apply would fail with "inconsistent result after apply" once the
// endpoint had already been changed.
func TestEndpointVisibilityChangeReplaces(t *testing.T) {
	f := newFakeMetaMCP(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
		Steps: []resource.TestStep{
			{
				Config: endpointVisibilityConfig("false"),
				Check:  resource.TestCheckResourceAttr("metamcp_endpoint.test", "is_public", "false"),
			},
			{
				Config: endpointVisibilityConfig("true"),
				Check:  resource.TestCheckResourceAttr("metamcp_endpoint.test", "is_public", "true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"metamcp_endpoint.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
		},
	})
}

// TestEndpointNamespaceChangeDoesNotReplace is the counterweight: only
// visibility is forced to replace. If the plan modifier were too broad, every
// unrelated edit would destroy and recreate the endpoint — and its URL with it.
func TestEndpointNamespaceChangeDoesNotReplace(t *testing.T) {
	f := newFakeMetaMCP(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
		Steps: []resource.TestStep{
			{
				Config: `
resource "metamcp_namespace" "public_ns" {
  name      = "public-ns"
  is_public = true
}

resource "metamcp_endpoint" "test" {
  name           = "no-replace-ep"
  namespace_uuid = metamcp_namespace.public_ns.uuid
  is_public      = false
}`,
			},
			{
				// Same visibility, different description: an ordinary in-place
				// update.
				Config: `
resource "metamcp_namespace" "public_ns" {
  name      = "public-ns"
  is_public = true
}

resource "metamcp_endpoint" "test" {
  name           = "no-replace-ep"
  namespace_uuid = metamcp_namespace.public_ns.uuid
  is_public      = false
  description    = "edited"
}`,
				Check: resource.TestCheckResourceAttr("metamcp_endpoint.test", "description", "edited"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"metamcp_endpoint.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}

// TestEndpointVisibilityRoundTripsThroughImport proves the attribute is read
// back from the server rather than only echoed from configuration — the whole
// reason it is Optional+Computed.
//
// create_mcp_server is excluded from the comparison because it is a
// create-time-only instruction the API never reports back, so it cannot survive
// an import. That is a pre-existing property of the resource, documented in its
// schema, not something import verification should assert.
func TestEndpointVisibilityRoundTripsThroughImport(t *testing.T) {
	f := newFakeMetaMCP(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
		Steps: []resource.TestStep{
			{
				Config: endpointVisibilityConfig("true"),
				Check:  resource.TestCheckResourceAttr("metamcp_endpoint.test", "is_public", "true"),
			},
			{
				ResourceName:                         "metamcp_endpoint.test",
				ImportState:                          true,
				ImportStateIdFunc:                    importByUUID("metamcp_endpoint.test"),
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "uuid",
				ImportStateVerifyIgnore:              []string{"create_mcp_server"},
			},
		},
	})
}
