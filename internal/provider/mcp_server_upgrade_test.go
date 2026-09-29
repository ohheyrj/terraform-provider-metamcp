package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// State written before bearer_token became write-only still contains the token.
// Terraform refuses to accept a value for a write-only attribute back from a
// provider, so every already-managed server failed as soon as the provider was
// upgraded:
//
//	Error: Invalid resource state upgrade
//	... the provider "metamcp" returned a value for the write-only attribute
//	"metamcp_mcp_server.this.bearer_token"
//
// The framework only nullifies write-only values on the UpgradeState path — the
// version-match passthrough returns raw state untouched — so the resource needs
// a version bump and an upgrade that clears the token. This test drives that
// upgrade with a state blob in the exact shape the previous provider wrote.
func TestUpgradeStateFromVersion0ClearsWriteOnlyToken(t *testing.T) {
	ctx := context.Background()
	r := &mcpServerResource{}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", schemaResp.Diagnostics)
	}
	if got := schemaResp.Schema.Version; got != 1 {
		t.Fatalf("resource schema version = %d, want 1; without the bump Terraform "+
			"takes the passthrough branch and never calls the upgrader", got)
	}

	// Exactly what the pre-write-only provider stored: bearer_token present,
	// no token_fingerprint (that attribute did not exist yet).
	legacy := []byte(`{
		"uuid": "0929bfd5-0807-4e76-a5f4-7625d3a77fe0",
		"name": "cronometer",
		"description": null,
		"type": "STREAMABLE_HTTP",
		"command": null,
		"args": null,
		"env": null,
		"url": "https://cronometer.example/mcp",
		"bearer_token": "legacy-token-OLD-STYLE",
		"headers": null,
		"created_at": "2026-09-01T10:00:00Z",
		"is_public": true
	}`)

	upgraders := r.UpgradeState(ctx)
	upgrader, ok := upgraders[0]
	if !ok {
		t.Fatal("no state upgrader registered for version 0")
	}

	resp := resource.UpgradeStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}
	upgrader.StateUpgrader(ctx, resource.UpgradeStateRequest{
		RawState: &tfprotov6.RawState{JSON: legacy},
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgrade failed: %v", resp.Diagnostics)
	}

	var got mcpServerResourceModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if resp.Diagnostics.HasError() {
		t.Fatalf("reading upgraded state: %v", resp.Diagnostics)
	}

	// The whole point: the write-only value must be gone.
	if !got.BearerToken.IsNull() {
		t.Errorf("bearer_token = %q after upgrade, want null; Terraform rejects any "+
			"value for a write-only attribute", got.BearerToken.ValueString())
	}

	// Nothing else may be lost.
	if got.UUID.ValueString() != "0929bfd5-0807-4e76-a5f4-7625d3a77fe0" {
		t.Errorf("uuid = %q, want it preserved", got.UUID.ValueString())
	}
	if got.Name.ValueString() != "cronometer" {
		t.Errorf("name = %q, want it preserved", got.Name.ValueString())
	}
	if got.URL.ValueString() != "https://cronometer.example/mcp" {
		t.Errorf("url = %q, want it preserved", got.URL.ValueString())
	}
	if got.CreatedAt.ValueString() != "2026-09-01T10:00:00Z" {
		t.Errorf("created_at = %q, want it preserved", got.CreatedAt.ValueString())
	}
	if got.IsPublic.IsNull() || !got.IsPublic.ValueBool() {
		t.Errorf("is_public = %v, want true preserved", got.IsPublic)
	}
}

// A state blob with no token must upgrade cleanly too: that is the common case
// for servers whose configuration never had a bearer_token.
func TestUpgradeStateFromVersion0WithoutToken(t *testing.T) {
	ctx := context.Background()
	r := &mcpServerResource{}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	legacy := []byte(`{
		"uuid": "11111111-2222-3333-4444-555555555555",
		"name": "openwearables",
		"type": "STREAMABLE_HTTP",
		"url": "https://ow.example/mcp",
		"bearer_token": null,
		"is_public": true
	}`)

	upgrader := r.UpgradeState(ctx)[0]
	resp := resource.UpgradeStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}
	upgrader.StateUpgrader(ctx, resource.UpgradeStateRequest{
		RawState: &tfprotov6.RawState{JSON: legacy},
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgrade failed: %v", resp.Diagnostics)
	}

	var got mcpServerResourceModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if resp.Diagnostics.HasError() {
		t.Fatalf("reading upgraded state: %v", resp.Diagnostics)
	}
	if !got.BearerToken.IsNull() {
		t.Errorf("bearer_token = %q, want null", got.BearerToken.ValueString())
	}
	if got.Name.ValueString() != "openwearables" {
		t.Errorf("name = %q, want it preserved", got.Name.ValueString())
	}
}
