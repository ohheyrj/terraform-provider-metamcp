package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/client"
)

// metamcp_namespace exposes is_public the same way metamcp_mcp_server does:
// MetaMCP has no visibility column, so a null user_id means public and a real
// user id means private. These tests pin the three directions, because the
// failure modes are silent:
//
//   - sending "" instead of an explicit null is a foreign-key violation, since
//     user_id references users.id and "" matches no row
//   - sending the owner when the intent was public leaves the namespace private
//   - sending null when the intent was "leave it alone" makes every update public

func namespaceResourceForTest(ctx context.Context, t *testing.T, endpoint string) *namespaceResource {
	t.Helper()
	c, err := client.New(client.Config{
		Endpoint: endpoint,
		Email:    "test@example.com",
		Password: "not-a-real-password",
	})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}
	// Sign in, as the provider does at Configure time. Without it no session
	// exists and Client.UserID() is empty, so anything that needs to name the
	// authenticated user — making an object private — cannot work. A test that
	// skipped this would exercise a state the provider never runs in.
	if err := c.SignIn(ctx); err != nil {
		t.Fatalf("signing in: %v", err)
	}
	if c.UserID() == "" {
		t.Fatal("client reports no user id after sign-in")
	}
	return &namespaceResource{client: c}
}

func namespaceSchema(ctx context.Context, t *testing.T) schema.Schema {
	t.Helper()
	r := &namespaceResource{}
	var resp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func namespaceBaseModel(name string) namespaceResourceModel {
	return namespaceResourceModel{
		UUID:           types.StringNull(),
		Name:           types.StringValue(name),
		Description:    types.StringNull(),
		CreatedAt:      types.StringNull(),
		UpdatedAt:      types.StringNull(),
		McpServerUUIDs: types.SetNull(types.StringType),
		IsPublic:       types.BoolNull(),
	}
}

func TestNamespaceIsPublicDrivesOwnership(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name       string
		isPublic   types.Bool
		wantOwner  any // nil means public
		wantOnWire string
	}{
		{"public clears ownership", types.BoolValue(true), nil, "null"},
		{"private claims it for the caller", types.BoolValue(false), fakeUserID, "user id"},
		{"unset leaves it to the API default", types.BoolNull(), fakeUserID, "user id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMetaMCP(t)
			r := namespaceResourceForTest(ctx, t, f.URL)

			plan := namespaceBaseModel("dashboards")
			plan.IsPublic = tc.isPublic

			var resp resource.CreateResponse
			resp.State = tfsdk.State{Schema: namespaceSchema(ctx, t)}
			req := resource.CreateRequest{
				Plan:   planForNamespace(ctx, t, plan),
				Config: configForNamespace(ctx, t, plan),
			}
			r.Create(ctx, req, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("create failed: %v", resp.Diagnostics)
			}

			var created namespaceResourceModel
			if diags := resp.State.Get(ctx, &created); diags.HasError() {
				t.Fatalf("reading created state: %v", diags)
			}
			got := f.namespaces[created.UUID.ValueString()]["user_id"]
			if got != tc.wantOwner {
				t.Errorf("stored user_id = %#v, want %#v (%s)", got, tc.wantOwner, tc.wantOnWire)
			}
			// The read-back must agree with configuration, or Terraform reports
			// an inconsistent result.
			if !created.IsPublic.Equal(tc.isPublic) && !tc.isPublic.IsNull() {
				t.Errorf("is_public read back as %v, want %v", created.IsPublic, tc.isPublic)
			}
		})
	}
}

// Turning a namespace public must send an explicit null, never "" — the API
// stores user_id verbatim and "" is a foreign-key violation. The fake panics on
// "", so this also fails loudly if the sentinel ever stops working.
func TestNamespacePublicUpdateSendsNull(t *testing.T) {
	ctx := context.Background()
	f := newFakeMetaMCP(t)
	r := namespaceResourceForTest(ctx, t, f.URL)

	// Existing, private.
	plan := namespaceBaseModel("dashboards")
	plan.IsPublic = types.BoolValue(false)

	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: namespaceSchema(ctx, t)}
	r.Create(ctx, resource.CreateRequest{
		Plan:   planForNamespace(ctx, t, plan),
		Config: configForNamespace(ctx, t, plan),
	}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics)
	}
	var created namespaceResourceModel
	_ = createResp.State.Get(ctx, &created)
	if owner := f.namespaces[created.UUID.ValueString()]["user_id"]; owner != fakeUserID {
		t.Fatalf("precondition: namespace is not private, owner = %#v", owner)
	}

	// Now make it public.
	state := created
	newPlan := created
	newPlan.IsPublic = types.BoolValue(true)

	var resp resource.UpdateResponse
	resp.State = tfsdk.State{Schema: namespaceSchema(ctx, t)}
	r.Update(ctx, resource.UpdateRequest{
		State:  stateForNamespace(ctx, t, state),
		Plan:   planForNamespace(ctx, t, newPlan),
		Config: configForNamespace(ctx, t, newPlan),
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics)
	}

	if owner := f.namespaces[created.UUID.ValueString()]["user_id"]; owner != nil {
		t.Errorf("stored user_id = %#v, want nil (public)", owner)
	}

	// And it must not churn: an unchanged is_public leaves ownership alone.
	var resp2 resource.UpdateResponse
	resp2.State = tfsdk.State{Schema: namespaceSchema(ctx, t)}
	r.Update(ctx, resource.UpdateRequest{
		State:  stateForNamespace(ctx, t, newPlan),
		Plan:   planForNamespace(ctx, t, newPlan),
		Config: configForNamespace(ctx, t, newPlan),
	}, &resp2)
	if resp2.Diagnostics.HasError() {
		t.Fatalf("second update failed: %v", resp2.Diagnostics)
	}
	if owner := f.namespaces[created.UUID.ValueString()]["user_id"]; owner != nil {
		t.Errorf("after a no-op update user_id = %#v, want nil; a changed owner "+
			"without a configuration change means visibility is being resent", owner)
	}
}

// The API refuses a private server inside a public namespace, and that refusal
// must surface rather than being swallowed.
func TestNamespacePublicRejectsPrivateServer(t *testing.T) {
	ctx := context.Background()
	f := newFakeMetaMCP(t)
	r := namespaceResourceForTest(ctx, t, f.URL)

	// A private server: it has an owner.
	privateID := f.seedServer(map[string]any{
		"name":    "cronometer",
		"type":    "STREAMABLE_HTTP",
		"url":     "http://cronometer/mcp",
		"user_id": fakeUserID,
	})

	plan := namespaceBaseModel("mixed")
	plan.IsPublic = types.BoolValue(true)
	sv, diags := types.SetValueFrom(ctx, types.StringType, []string{privateID})
	if diags.HasError() {
		t.Fatalf("building server set: %v", diags)
	}
	plan.McpServerUUIDs = sv

	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: namespaceSchema(ctx, t)}
	r.Create(ctx, resource.CreateRequest{
		Plan:   planForNamespace(ctx, t, plan),
		Config: configForNamespace(ctx, t, plan),
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("creating a public namespace with a private server succeeded; " +
			"the API refuses this, so the provider must report the refusal")
	}
	if d := resp.Diagnostics[0].Detail(); !strings.Contains(d, "public namespace") &&
		!strings.Contains(d, "public MCP") {
		t.Errorf("error did not carry the server's reason: %v", resp.Diagnostics[0].Detail())
	}
}

func planForNamespace(ctx context.Context, t *testing.T, m namespaceResourceModel) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: namespaceSchema(ctx, t)}
	if diags := p.Set(ctx, &m); diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}
	return p
}

func stateForNamespace(ctx context.Context, t *testing.T, m namespaceResourceModel) tfsdk.State {
	t.Helper()
	s := tfsdk.State{Schema: namespaceSchema(ctx, t)}
	if diags := s.Set(ctx, &m); diags.HasError() {
		t.Fatalf("building state: %v", diags)
	}
	return s
}

func configForNamespace(ctx context.Context, t *testing.T, m namespaceResourceModel) tfsdk.Config {
	t.Helper()
	p := planForNamespace(ctx, t, m)
	return tfsdk.Config{Schema: namespaceSchema(ctx, t), Raw: p.Raw}
}
