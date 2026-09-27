package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestUserIDForVisibility pins the three outcomes, which are easy to conflate
// because two of them send a pointer and the failure is silent either way.
func TestUserIDForVisibility(t *testing.T) {
	const me = "user-0000-1111-2222-3333"

	tests := []struct {
		name     string
		isPublic types.Bool
		ownID    string
		onCreate bool
		want     string
		wantNil  bool // true means "send nothing", as distinct from "send empty"
		wantErr  bool
	}{
		// Public sends the empty string, which CLEARS the owner. It is not the
		// same as sending nothing, and conflating the two is the silent bug this
		// table exists to catch.
		{name: "public clears the owner", isPublic: types.BoolValue(true),
			ownID: me, want: ""},
		{name: "private claims the user", isPublic: types.BoolValue(false),
			ownID: me, want: me},
		{name: "unset on update sends nothing", isPublic: types.BoolNull(),
			ownID: me, wantNil: true},
		{name: "unset on create claims the user", isPublic: types.BoolNull(),
			ownID: me, onCreate: true, want: me},
		{name: "unknown counts as unset when updating", isPublic: types.BoolUnknown(),
			ownID: me, wantNil: true},
		{name: "private without a user id is an error", isPublic: types.BoolValue(false),
			ownID: "", wantErr: true},
		{name: "unset on create without a user id is an error", isPublic: types.BoolNull(),
			ownID: "", onCreate: true, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := userIDForVisibility(tc.isPublic, tc.ownID, tc.onCreate)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil (leave ownership alone), got %q", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected a non-nil pointer to %q, got nil (would send nothing)", tc.want)
			}
			if *got != tc.want {
				t.Errorf("expected %q, got %q", tc.want, *got)
			}
		})
	}
}

// The caller only reaches this at all when the planned value differs from state,
// so an unset value never means "change visibility" — that is what keeps the
// empty-string case reserved for an explicit is_public = true.
var _ = context.Background

// TestNoChurnOnUnchangedServer is the regression test for a perpetual diff.
//
// MetaMCP's serializer always returns error_status, and for a STDIO server it is
// reset when the server is updated. Storing it made Terraform see "NONE" become
// "ERROR", or become unknown, on every plan — so an import or an unrelated rename
// proposed an update. The attribute is now absent from the provider entirely; a
// plan that changes nothing must stay empty.
func TestNoChurnOnUnchangedServer(t *testing.T) {
	fake := newFakeMetaMCP(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(fake.URL),
		Steps: []resource.TestStep{
			{
				Config: `
resource "metamcp_mcp_server" "test" {
  name = "churn-check"
  type = "STREAMABLE_HTTP"
  url  = "https://example.com/mcp"
}`,
			},
			// PlanOnly with the same config: any non-empty plan means an
			// attribute is unstable across a refresh.
			{
				Config: `
resource "metamcp_mcp_server" "test" {
  name = "churn-check"
  type = "STREAMABLE_HTTP"
  url  = "https://example.com/mcp"
}`,
				PlanOnly: true,
			},
		},
	})
}

// TestVisibilityRoundTrip proves is_public is reported and can be changed, and
// that a server defaults to private rather than public when it is not set.
func TestVisibilityRoundTrip(t *testing.T) {
	fake := newFakeMetaMCP(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(fake.URL),
		Steps: []resource.TestStep{
			{
				// Omitted: must claim the authenticated user, never widen access.
				Config: `
resource "metamcp_mcp_server" "private_by_default" {
  name = "default-visibility"
  type = "STREAMABLE_HTTP"
  url  = "https://example.com/mcp"
}`,
				Check: resource.TestCheckResourceAttr(
					"metamcp_mcp_server.private_by_default", "is_public", "false"),
			},
			{
				Config: `
resource "metamcp_mcp_server" "private_by_default" {
  name      = "default-visibility"
  type      = "STREAMABLE_HTTP"
  url       = "https://example.com/mcp"
  is_public = true
}`,
				Check: resource.TestCheckResourceAttr(
					"metamcp_mcp_server.private_by_default", "is_public", "true"),
			},
			{
				Config: `
resource "metamcp_mcp_server" "private_by_default" {
  name      = "default-visibility"
  type      = "STREAMABLE_HTTP"
  url       = "https://example.com/mcp"
  is_public = false
}`,
				Check: resource.TestCheckResourceAttr(
					"metamcp_mcp_server.private_by_default", "is_public", "false"),
			},
		},
	})
}
