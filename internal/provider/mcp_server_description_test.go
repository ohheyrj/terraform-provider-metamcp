package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/client"
)

// A server can carry a description the Terraform configuration never declares —
// it was set in the MetaMCP UI, or in configuration and later removed. The
// provider used to send an omitted `description` key for that (a nil pointer
// plus `omitempty`), and the API reads an absent key as "keep", so the stored
// value survived. The read-back then disagreed with the plan and the apply
// failed *after* the change had already gone through:
//
//	Provider produced inconsistent result after apply
//	.description: was null, but now cty.StringVal("ldn.casa Kubernetes MCP")
//
// The provider now sends "" to clear it, which is what the API's own web UI does
// and what the read path already normalises back to null.
//
// These tests call Update directly instead of using a Terraform test step.
//
// An earlier version used resource.Test with an ImportState step to adopt the
// seeded server, and it proved nothing: the step created a *new* server rather
// than adopting the seeded one, so the description under test never entered
// state and the assertion could not fail. Building state and plan explicitly
// removes that possibility — the previous description is in state by
// construction. Terraform only raises the inconsistency after a real apply, but
// the provider's obligation is testable on its own: whatever it sends must leave
// the server agreeing with the plan.

// updateRequest assembles a well-formed UpdateRequest and a live response for
// the given state and plan models.
//
// Config is built from the plan's own tftypes value: tfsdk.Config exposes only
// Raw plus Get, with no Set, so it cannot be populated from a model directly.
// The write-only bearer_token is not involved in these tests, so plan and config
// being identical is faithful.
func updateRequest(t *testing.T, endpoint string, state, plan mcpServerResourceModel) (*mcpServerResource, resource.UpdateRequest, *resource.UpdateResponse) {
	t.Helper()
	ctx := context.Background()

	r := &mcpServerResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", schemaResp.Diagnostics)
	}
	c, err := client.New(client.Config{
		Endpoint: endpoint,
		Email:    "test@example.com",
		Password: "not-a-real-password",
	})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}
	r.client = c

	stateObj := tfsdk.State{Schema: schemaResp.Schema}
	if diags := stateObj.Set(ctx, &state); diags.HasError() {
		t.Fatalf("building state: %v", diags)
	}
	planObj := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := planObj.Set(ctx, &plan); diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	return r, resource.UpdateRequest{
		State:  stateObj,
		Plan:   planObj,
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: planObj.Raw},
	}, resp
}

// mcpServerBaseModel mirrors what a Read populates, with every attribute set so
// the value can be stored in a tfsdk.State. Omitted attributes default to a Go
// zero value, which is not a valid Terraform value and is rejected on Set.
func mcpServerBaseModel(id string) mcpServerResourceModel {
	return mcpServerResourceModel{
		UUID:             types.StringValue(id),
		Name:             types.StringValue("kubernetes"),
		Description:      types.StringNull(),
		Type:             types.StringValue("STREAMABLE_HTTP"),
		Command:          types.StringNull(),
		Args:             types.ListNull(types.StringType),
		Env:              types.MapNull(types.StringType),
		URL:              types.StringValue("http://mcp-k8s.mcp-k8s.svc.cluster.local:8080/mcp"),
		BearerToken:      types.StringNull(),
		TokenFingerprint: types.StringNull(),
		Headers:          types.MapNull(types.StringType),
		CreatedAt:        types.StringValue("2026-09-20T00:00:00Z"),
		IsPublic:         types.BoolValue(true),
	}
}

func TestClearingDescriptionConvergesAfterApply(t *testing.T) {
	ctx := context.Background()
	f := newFakeMetaMCP(t)
	id := f.seedServer(map[string]any{
		"name":        "kubernetes",
		"description": "ldn.casa Kubernetes MCP",
		"type":        "STREAMABLE_HTTP",
		"url":         "http://mcp-k8s.mcp-k8s.svc.cluster.local:8080/mcp",
	})

	// State as Terraform holds it: the description came back on the read.
	state := mcpServerBaseModel(id)
	state.Description = types.StringValue("ldn.casa Kubernetes MCP")
	// Plan: the configuration declares no description, so it is null.
	plan := mcpServerBaseModel(id)
	plan.Description = types.StringNull()

	r, req, resp := updateRequest(t, f.URL, state, plan)
	r.Update(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics)
	}

	// The provider must leave the server agreeing with the plan, or Terraform
	// reports "was null, but now cty.StringVal(...)" on the next read.
	if got := f.serverField(id, "description"); got != "" && got != nil {
		t.Errorf("description on the server = %#v, want it cleared", got)
	}
}

func TestSettingDescriptionIsStored(t *testing.T) {
	ctx := context.Background()
	f := newFakeMetaMCP(t)
	id := f.seedServer(map[string]any{
		"name":        "kubernetes",
		"description": "stale value",
		"type":        "STREAMABLE_HTTP",
		"url":         "http://mcp-k8s.mcp-k8s.svc.cluster.local:8080/mcp",
	})

	state := mcpServerBaseModel(id)
	state.Description = types.StringValue("stale value")
	plan := mcpServerBaseModel(id)
	plan.Description = types.StringValue("managed by terraform")

	r, req, resp := updateRequest(t, f.URL, state, plan)
	r.Update(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics)
	}

	if got := f.serverField(id, "description"); got != "managed by terraform" {
		t.Errorf("description on the server = %#v, want %q", got, "managed by terraform")
	}
}
