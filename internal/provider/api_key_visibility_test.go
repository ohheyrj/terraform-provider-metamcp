package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// metamcp_api_key exposes is_public the same way metamcp_namespace,
// metamcp_mcp_server and metamcp_endpoint do: MetaMCP has no visibility column,
// so a null user_id means public and a real user id means private.
//
// Two things are specific to API keys, and they are what most of this file is
// about:
//
//  1. Ownership is returned by apiKeys.list and by NOTHING else. Neither create
//     nor update includes a user_id field, and a missing JSON field decodes to
//     nil — exactly the value that MEANS public. A provider that read ownership
//     from the create response would report every key as public and fail the
//     apply with "inconsistent result after apply".
//
//  2. Ownership is accepted by apiKeys.create and by NOTHING else, so a
//     visibility change cannot be applied in place. Replacing an API key issues
//     a new secret and invalidates the old one, which is why the replacement
//     tests below assert the key's value actually changed and not merely that
//     the plan asked for a destroy-and-create.
//
// The silent failure modes each direction guards against:
//
//   - sending "" instead of an explicit null is a foreign-key violation, since
//     user_id references users.id and "" matches no row
//   - omitting the attribute must claim the caller, never default to public
//   - attempting an in-place visibility change looks like it worked, then fails
//     the apply once the key has already been touched

// apiKeyVisibilityConfig is the shared fixture: one key whose visibility the
// caller supplies, with "" meaning "omit the attribute entirely".
func apiKeyVisibilityConfig(name, isPublic string) string {
	visibility := ""
	if isPublic != "" {
		visibility = "\n  is_public = " + isPublic
	}
	return `
resource "metamcp_api_key" "test" {
  name = "` + name + `"` + visibility + `
}`
}

// TestAPIKeyCreateSendsVisibility is the wire-format guard. It asserts on what
// reached the API rather than on state, because the ownership actually sent is
// the only thing that decides whether a key is public.
//
// The fake's ownerFor panics if user_id arrives as "", so a passing run also
// proves the empty-string bug is absent — that encoding is not merely ignored
// here, it is fatal.
func TestAPIKeyCreateSendsVisibility(t *testing.T) {
	for _, tc := range []struct {
		name       string
		isPublic   string
		wantPublic bool
	}{
		{"explicit true is public", "true", true},
		{"explicit false is private", "false", false},
		// Omitted must NOT widen access: the API defaults an absent user_id to
		// the authenticated user, so the key is created private.
		{"omitted is private, not public", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMetaMCP(t)

			var sawCreate map[string]any
			f.onAPIKeyWrite = func(proc string, in map[string]any) {
				if proc == "apiKeys.create" {
					sawCreate = in
				}
			}

			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
				Steps: []resource.TestStep{
					{
						Config: apiKeyVisibilityConfig("visibility-key", tc.isPublic),
						Check: resource.TestCheckResourceAttr(
							"metamcp_api_key.test", "is_public",
							map[bool]string{true: "true", false: "false"}[tc.wantPublic]),
					},
				},
			})

			if sawCreate == nil {
				t.Fatal("apiKeys.create was never called")
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

// TestAPIKeyVisibilityReadBackIsOwnership is the regression guard for the
// create-response trap, and the reason the provider lists a key back after
// creating it.
//
// It compares state against the owner the fake STORED, which is set from the
// create request rather than from anything the create response returns — so a
// provider reading ownership out of that response fails here, reporting a
// private key as public.
func TestAPIKeyVisibilityReadBackIsOwnership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		public string
		want   string
	}{
		{"private reads back false", "false", "false"},
		{"public reads back true", "true", "true"},
		{"omitted reads back false", "", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMetaMCP(t)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
				Steps: []resource.TestStep{
					{
						Config: apiKeyVisibilityConfig("readback-key", tc.public),
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttr("metamcp_api_key.test", "is_public", tc.want),
							// The stored owner is the authority: a null owner is
							// public, a real one is private.
							resource.TestCheckResourceAttrWith(
								"metamcp_api_key.test", "uuid",
								func(uuid string) error {
									return f.checkStoredAPIKeyOwner(uuid, tc.want == "true")
								}),
						),
					},
				},
			})
		})
	}
}

// TestAPIKeyVisibilityChangeReplaces proves the provider asks for a replacement
// rather than attempting an in-place change the API cannot perform.
//
// Without the plan modifier Terraform would send the new ownership, the API
// would ignore it (apiKeys.update never passes user_id to the repository), and
// the apply would fail with "inconsistent result after apply" once the key had
// already been changed.
//
// The key's value is asserted to have CHANGED in the same test, because that is
// the consequence a user has to plan for: a replacement mints a new secret and
// invalidates the old one. Asserting only the plan action would let a future
// change that keeps the secret pass.
func TestAPIKeyVisibilityChangeReplaces(t *testing.T) {
	f := newFakeMetaMCP(t)

	var firstKey string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
		Steps: []resource.TestStep{
			{
				Config: apiKeyVisibilityConfig("replace-key", "false"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("metamcp_api_key.test", "is_public", "false"),
					resource.TestCheckResourceAttrWith(
						"metamcp_api_key.test", "key",
						func(v string) error {
							firstKey = v
							return nil
						}),
				),
			},
			{
				Config: apiKeyVisibilityConfig("replace-key", "true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("metamcp_api_key.test", "is_public", "true"),
					resource.TestCheckResourceAttrWith(
						"metamcp_api_key.test", "key",
						func(v string) error {
							if firstKey == "" {
								return fmt.Errorf("the first key value was never captured")
							}
							if v == firstKey {
								return fmt.Errorf("the key still has its old value after a "+
									"visibility change; the endpoint was replaced but the "+
									"credential was not regenerated (old %q)", firstKey)
							}
							return nil
						}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"metamcp_api_key.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
		},
	})
}

// TestAPIKeyNameChangeDoesNotReplace is the counterweight: only visibility is
// forced to replace. If the plan modifier were too broad, every rename would
// silently mint a new secret — invalidating a live credential as a side effect
// of editing a label — the worst possible outcome for this resource.
func TestAPIKeyNameChangeDoesNotReplace(t *testing.T) {
	f := newFakeMetaMCP(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
		Steps: []resource.TestStep{
			{
				Config: apiKeyVisibilityConfig("no-replace-key", "false"),
			},
			{
				// Same visibility, different name: an ordinary in-place update.
				Config: apiKeyVisibilityConfig("renamed-key", "false"),
				Check:  resource.TestCheckResourceAttr("metamcp_api_key.test", "name", "renamed-key"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"metamcp_api_key.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}

// TestAPIKeyVisibilityRoundTripsThroughImport proves the attribute is read back
// from the server rather than only echoed from configuration — the whole reason
// it is Optional+Computed. It also covers the case that matters most in
// practice: a key changed in MetaMCP's own UI is adopted with whatever
// visibility the server reports.
func TestAPIKeyVisibilityRoundTripsThroughImport(t *testing.T) {
	f := newFakeMetaMCP(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
		Steps: []resource.TestStep{
			{
				Config: apiKeyVisibilityConfig("import-key", "true"),
				Check:  resource.TestCheckResourceAttr("metamcp_api_key.test", "is_public", "true"),
			},
			{
				ResourceName:                         "metamcp_api_key.test",
				ImportState:                          true,
				ImportStateIdFunc:                    importByUUID("metamcp_api_key.test"),
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "uuid",
				// Nothing to exclude: every attribute this resource manages is
				// reported back by apiKeys.list, which is the point.
			},
		},
	})
}

// TestAPIKeyVisibilityDecidesAccess pins the property that gives is_public its
// meaning, and the reason it is worth an attribute at all: a public key is
// returned to a caller who does not own it, a private one is not.
//
// Without this, is_public could be a correct-looking field that changes nothing.
// The fake applies the API's own findAccessibleToUser rule — public keys OR the
// caller's own — for a different user id, so both directions are checked.
func TestAPIKeyVisibilityDecidesAccess(t *testing.T) {
	for _, tc := range []struct {
		name       string
		isPublic   string
		wantOthers bool
	}{
		{"a public key is visible to other users", "true", true},
		{"a private key is hidden from other users", "false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMetaMCP(t)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
				Steps: []resource.TestStep{
					{
						Config: apiKeyVisibilityConfig("access-key", tc.isPublic),
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttr("metamcp_api_key.test", "is_public", tc.isPublic),
							resource.TestCheckResourceAttrWith(
								"metamcp_api_key.test", "uuid",
								func(uuid string) error {
									return f.checkAPIKeyAccess(uuid, tc.wantOthers)
								}),
						),
					},
				},
			})
		})
	}
}
