package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/client"
)

// TestPublicNeverSendsAnEmptyUserID guards the exact bug that broke a live
// apply: "" is not NULL, and the column is a foreign key, so the database
// rejects it.
func TestPublicNeverSendsAnEmptyUserID(t *testing.T) {
	got, err := userIDForVisibility(types.BoolValue(true), "user-1", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("public sent nothing; it must send an explicit null")
	}
	if *got == "" {
		t.Fatal(`public sent "", which is a foreign-key violation, not public`)
	}
	if *got != client.SendNullUserID {
		t.Fatalf("expected the null sentinel, got %q", *got)
	}
	// and it must actually serialise to a JSON null, not the sentinel text
	b, err := json.Marshal(client.McpServerInput{Name: "n", Type: client.ServerTypeStreamableHTTP, UserID: got})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"user_id":null`) {
		t.Fatalf("public did not serialise to a JSON null: %s", b)
	}
}

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
		// Public must be an EXPLICIT NULL, not an empty string. user_id is a
		// foreign key to users.id, so "" matches no row and Postgres rejects it;
		// sending "" here is precisely the bug that reached a real server.
		{name: "public sends an explicit null", isPublic: types.BoolValue(true),
			ownID: me, want: client.SendNullUserID},
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
