package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/client"
)

// Import and "adopting an object that already exists" both reduce to one
// question the resource-level tests do not answer on their own: given a key the
// provider did NOT create, does Read produce a model that matches a
// configuration which leaves the optional attributes out?
//
// The answer has to be yes for is_public, and the failure would be quiet: an
// imported PUBLIC key read back as private, or one read back in a way that makes
// the next plan propose an in-place change to a field the API cannot change —
// which the plan modifier would then turn into a replacement, minting a new
// secret for a key nobody asked to rotate.
//
// These call Read directly with a server-supplied key, following the precedent in
// mcp_server_ui_created_test.go, because a resource.Test step that seeds a
// differently-named object cannot be constructed here without the create path
// also running and rewriting what is under test.

// apiKeyResourceForRead builds a resource backed by a client pointed at the fake,
// and a state carrying only the uuid — which is exactly what a bare
// `terraform import` leaves behind.
func apiKeyResourceForRead(ctx context.Context, t *testing.T, endpoint string) *apiKeyResource {
	t.Helper()
	c, err := client.New(client.Config{
		Endpoint: endpoint,
		Email:    "test@example.com",
		Password: "not-a-real-password",
	})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}
	if err := c.SignIn(ctx); err != nil {
		t.Fatalf("signing in: %v", err)
	}
	return &apiKeyResource{client: c}
}

func apiKeySchemaForRead(t *testing.T) schema.Schema {
	t.Helper()
	r := &apiKeyResource{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// TestReadAdoptsServerOwnership is the import-critical case: a key that exists on
// the server, with ownership the provider never saw, must read back with the
// visibility the SERVER reports.
//
// Both directions are asserted, because only one of them is intuitive: a public
// key read as private looks like a security improvement and is in fact a lie,
// and it would be "fixed" by a later apply that makes the key public again.
func TestReadAdoptsServerOwnership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		owner    *string
		wantBool string
	}{
		{"a public key reads back public", nil, "true"},
		{"a private key reads back private", strPtr(fakeUserID), "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFakeMetaMCP(t)
			uuid := f.seedAPIKey(map[string]any{
				"name": "adopted", "user_id": tc.owner, "is_active": true,
			})
			r := apiKeyResourceForRead(ctx, t, f.URL)

			var state apiKeyResourceModel
			state.UUID = types.StringValue(uuid)

			var resp resource.ReadResponse
			resp.State = tfsdk.State{Schema: apiKeySchemaForRead(t)}
			req := resource.ReadRequest{State: tfsdk.State{Schema: apiKeySchemaForRead(t)}}
			if diags := req.State.Set(ctx, &state); diags.HasError() {
				t.Fatalf("building state: %v", diags)
			}
			r.Read(ctx, req, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("read failed: %v", resp.Diagnostics)
			}

			var got apiKeyResourceModel
			if diags := resp.State.Get(ctx, &got); diags.HasError() {
				t.Fatalf("reading state: %v", diags)
			}
			if got.IsPublic.IsNull() {
				t.Fatalf("is_public is null after import; it must be reported")
			}
			if v := map[bool]string{true: "true", false: "false"}[got.IsPublic.ValueBool()]; v != tc.wantBool {
				t.Errorf("is_public read back as %v, want %v (server owner %#v)",
					got.IsPublic, tc.wantBool, tc.owner)
			}
			// The secret must survive adoption too: it is how a client
			// authenticates, and import is the only way to recover it for a key
			// that already exists.
			if got.Key.IsNull() || got.Key.ValueString() == "" {
				t.Error("the key value was not recovered on read; an imported key would be unusable")
			}
		})
	}
}

// TestReadDropsRemovedKey proves a key deleted outside Terraform is dropped from
// state rather than left to produce a permanent error.
func TestReadDropsRemovedKey(t *testing.T) {
	ctx := context.Background()
	f := newFakeMetaMCP(t)
	r := apiKeyResourceForRead(ctx, t, f.URL)

	var state apiKeyResourceModel
	// A well-formed uuid that the fake has never seen.
	state.UUID = types.StringValue("00000000-1111-2222-3333-444444444444")

	var resp resource.ReadResponse
	resp.State = tfsdk.State{Schema: apiKeySchemaForRead(t)}
	req := resource.ReadRequest{State: tfsdk.State{Schema: apiKeySchemaForRead(t)}}
	if diags := req.State.Set(ctx, &state); diags.HasError() {
		t.Fatalf("building state: %v", diags)
	}
	r.Read(ctx, req, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a missing key must not be an error: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a key that no longer exists should be removed from state so it can be recreated")
	}
}

// TestUpdateRefusesInPlaceVisibilityChange exercises the defence-in-depth guard
// directly. It should be unreachable, because the plan modifier replaces first —
// but if it is ever reached, a silent divergence that surfaces as
// "inconsistent result after apply" is the worst outcome, so the refusal itself
// is asserted rather than assumed.
func TestUpdateRefusesInPlaceVisibilityChange(t *testing.T) {
	ctx := context.Background()
	f := newFakeMetaMCP(t)
	uuid := f.seedAPIKey(map[string]any{"name": "nofix", "user_id": fakeUserID})
	r := apiKeyResourceForRead(ctx, t, f.URL)

	state := apiKeyResourceModel{
		UUID:     types.StringValue(uuid),
		Name:     types.StringValue("nofix"),
		IsActive: types.BoolValue(true),
		IsPublic: types.BoolValue(false),
	}
	plan := state
	plan.IsPublic = types.BoolValue(true)

	var resp resource.UpdateResponse
	resp.State = tfsdk.State{Schema: apiKeySchemaForRead(t)}
	req := resource.UpdateRequest{}
	req.State = tfsdk.State{Schema: apiKeySchemaForRead(t)}
	req.Plan = tfsdk.Plan{Schema: apiKeySchemaForRead(t)}
	if diags := req.State.Set(ctx, &state); diags.HasError() {
		t.Fatalf("state: %v", diags)
	}
	if diags := req.Plan.Set(ctx, &plan); diags.HasError() {
		t.Fatalf("plan: %v", diags)
	}

	r.Update(ctx, req, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an in-place visibility change must be refused, not silently dropped")
	}
	// The key must be untouched: a refusal that still changed the key would be
	// worse than the divergence it is preventing.
	if owner := f.apiKeys[uuid]["user_id"]; owner == nil {
		t.Error("the guard refused but the key was made public anyway")
	}
}
