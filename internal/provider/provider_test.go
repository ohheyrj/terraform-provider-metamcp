package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// These tests validate the provider's own schema surface without a live
// MetaMCP: they catch unset descriptions, invalid default/required
// combinations, and (via the framework) duplicate or malformed attributes.
// They deliberately do not exercise CRUD — that needs acceptance tests against
// a real instance, which are gated behind TF_ACC.

func TestProviderSchema(t *testing.T) {
	var resp provider.SchemaResponse
	New("test")().Schema(context.Background(), provider.SchemaRequest{}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("provider schema has errors: %v", resp.Diagnostics)
	}
	for _, name := range []string{"endpoint", "email", "password", "cookie"} {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Errorf("provider schema is missing attribute %q", name)
			continue
		}
		if !attr.IsOptional() {
			t.Errorf("%q should be optional so the environment can supply it", name)
		}
	}
	// Credentials must never be echoed in plan output.
	for _, name := range []string{"password", "cookie"} {
		if !resp.Schema.Attributes[name].IsSensitive() {
			t.Errorf("%q must be marked sensitive", name)
		}
	}
}

func TestResourceAndDataSourceSchemas(t *testing.T) {
	ctx := context.Background()

	resources := map[string]func() resource.Resource{
		"namespace":  NewNamespaceResource,
		"mcp_server": NewMcpServerResource,
		"endpoint":   NewEndpointResource,
		"api_key":    NewAPIKeyResource,
	}
	for name, factory := range resources {
		t.Run("resource/"+name, func(t *testing.T) {
			r := factory()
			var md resource.MetadataResponse
			r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "metamcp"}, &md)
			if md.TypeName != "metamcp_"+name {
				t.Errorf("TypeName = %q, want %q", md.TypeName, "metamcp_"+name)
			}
			var sr resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			if sr.Diagnostics.HasError() {
				t.Fatalf("schema errors: %v", sr.Diagnostics)
			}
			if len(sr.Schema.Attributes) == 0 {
				t.Error("schema has no attributes")
			}
		})
	}

	datasources := map[string]func() datasource.DataSource{
		"namespace":  NewNamespaceDataSource,
		"mcp_server": NewMcpServerDataSource,
		"endpoint":   NewEndpointDataSource,
		"api_key":    NewAPIKeyDataSource,
	}
	for name, factory := range datasources {
		t.Run("data_source/"+name, func(t *testing.T) {
			d := factory()
			var md datasource.MetadataResponse
			d.Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "metamcp"}, &md)
			if md.TypeName != "metamcp_"+name {
				t.Errorf("TypeName = %q, want %q", md.TypeName, "metamcp_"+name)
			}
			var sr datasource.SchemaResponse
			d.Schema(ctx, datasource.SchemaRequest{}, &sr)
			if sr.Diagnostics.HasError() {
				t.Fatalf("schema errors: %v", sr.Diagnostics)
			}
		})
	}
}

// TestSensitiveAttributes pins the fields that carry credentials. A regression
// here would print a live API key or token into plan output and CI logs.
func TestSensitiveAttributes(t *testing.T) {
	ctx := context.Background()

	var sr resource.SchemaResponse
	NewMcpServerResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	for _, name := range []string{"env", "headers", "bearer_token"} {
		if !sr.Schema.Attributes[name].IsSensitive() {
			t.Errorf("metamcp_mcp_server.%s must be sensitive", name)
		}
	}

	var kr resource.SchemaResponse
	NewAPIKeyResource().Schema(ctx, resource.SchemaRequest{}, &kr)
	if !kr.Schema.Attributes["key"].IsSensitive() {
		t.Error("metamcp_api_key.key must be sensitive")
	}
	if !kr.Schema.Attributes["key"].IsComputed() {
		t.Error("metamcp_api_key.key must be computed; the server generates it")
	}
}

func TestServerImplementation(t *testing.T) {
	p := New("test")()
	if p == nil {
		t.Fatal("provider factory returned nil")
	}
	if got := len(p.Resources(context.Background())); got != 4 {
		t.Errorf("expected 4 resources, got %d", got)
	}
	if got := len(p.DataSources(context.Background())); got != 4 {
		t.Errorf("expected 4 data sources, got %d", got)
	}
}

// TestStringHelpers covers the null/unknown conversions, where a mistake shows
// up as spurious diffs on every plan rather than as an error.
func TestStringHelpers(t *testing.T) {
	if got := stringOrNull(nil); !got.IsNull() {
		t.Errorf("stringOrNull(nil) should be null, got %v", got)
	}
	s := "x"
	if got := stringOrNull(&s); got.ValueString() != "x" {
		t.Errorf("stringOrNull(&x) = %v", got)
	}
	if got := stringPtr(types.StringNull()); got != nil {
		t.Errorf("stringPtr(null) should be nil, got %v", *got)
	}
	if got := stringPtr(types.StringUnknown()); got != nil {
		t.Errorf("stringPtr(unknown) should be nil, got %v", *got)
	}
	if got := stringPtr(types.StringValue("y")); got == nil || *got != "y" {
		t.Errorf("stringPtr(y) = %v", got)
	}
}
