package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/client"
)

func strPtr(s string) *string { return &s }

// MetaMCP accepts both null and "" for its optional string fields and stores
// whichever it is handed, so a record created through the web UI comes back with
// "" for every field the form left untouched (a browser form submits empty
// strings), while a Terraform-managed record has a real null.
//
// Reading "" back verbatim made the provider report a value where the plan said
// null, and Terraform refuses the result after applying it:
//
//	Error: Provider produced inconsistent result after apply
//	  .description: was null, but now cty.StringVal("")
//	  .command:     was null, but now cty.StringVal("")
//	  .bearer_token: inconsistent values for sensitive attribute
//
// Applying first and erroring afterwards is the worst shape: the infrastructure
// ends up correct and the state write fails, so the next plan is confused too.
// The two representations mean the same thing here, so the read normalises them.
//
// A create-only resource test cannot catch this, which is why the suite was
// green: the fake echoes back what the client sent, so a client-created record
// holds real nulls and never produces "".
func TestReadNormalisesEmptyStringsToNull(t *testing.T) {
	var m mcpServerResourceModel
	s := &client.McpServer{
		UUID:        "0e5b0000-0000-4000-8000-000000000042",
		Name:        "ui-made",
		Type:        client.ServerTypeStreamableHTTP,
		Description: strPtr(""),
		Command:     strPtr(""),
		BearerToken: strPtr(""),
		URL:         strPtr("https://example.com/mcp"),
		CreatedAt:   "2026-01-01T00:00:00Z",
	}
	if d := applyMcpServer(context.Background(), &m, s); d.HasError() {
		t.Fatalf("applyMcpServer: %v", d)
	}

	for _, c := range []struct {
		name string
		got  types.String
	}{
		{"description", m.Description},
		{"command", m.Command},
		{"bearer_token", m.BearerToken},
	} {
		if !c.got.IsNull() {
			t.Errorf("%s: server returned \"\", read back as %q; must be null",
				c.name, c.got.ValueString())
		}
	}

	// A genuine value must still survive the normalisation.
	if m.URL.IsNull() || m.URL.ValueString() != "https://example.com/mcp" {
		t.Errorf("url was lost or mangled: %v", m.URL)
	}
	// And a real null must still read as null.
	var m2 mcpServerResourceModel
	if d := applyMcpServer(context.Background(), &m2,
		&client.McpServer{Name: "x", Type: client.ServerTypeStdio, CreatedAt: "t"}); d.HasError() {
		t.Fatalf("applyMcpServer: %v", d)
	}
	if !m2.Description.IsNull() {
		t.Errorf("a nil description must read as null, got %q", m2.Description.ValueString())
	}
}
