package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ provider.Provider = &MetaMCPProvider{}
)

// MetaMCPProvider implements the MetaMCP Terraform provider.
//
// The methods below are the plugin interface Terraform calls; their doc
// comments describe what the host expects of each.
type MetaMCPProvider struct {
	version string
}

// MetaMCPProviderModel maps the provider configuration block.
type MetaMCPProviderModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Email    types.String `tfsdk:"email"`
	Password types.String `tfsdk:"password"`
	Cookie   types.String `tfsdk:"cookie"`
}

// New returns a provider factory for the given version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &MetaMCPProvider{version: version}
	}
}

// Metadata reports the provider's type name and version to Terraform.
func (p *MetaMCPProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "metamcp"
	resp.Version = p.version
}

// Schema describes the provider configuration block: the endpoint and the
// credentials used to reach the admin API.
func (p *MetaMCPProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Configure access to a MetaMCP instance's admin API.\n\n" +
			"MetaMCP's admin API is the tRPC surface its own web UI uses. " +
			"Authentication is a better-auth session obtained from an email and " +
			"password; an MCP *API key* cannot be used here, because it " +
			"authenticates the MCP gateway rather than the admin API.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Base URL of the MetaMCP instance, e.g. " +
					"`https://metamcp.example.com`. May also be set with the " +
					"`METAMCP_ENDPOINT` environment variable.",
			},
			"email": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Email address used to sign in. MetaMCP " +
					"authenticates by email only — a bare username will not work. " +
					"May also be set with the `METAMCP_EMAIL` environment variable.",
			},
			"password": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Password used to sign in. May also be set " +
					"with the `METAMCP_PASSWORD` environment variable. Prefer " +
					"`cookie` where possible: a session can be revoked, a password " +
					"cannot be un-leaked.",
			},
			"cookie": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "An existing better-auth session cookie, " +
					"used instead of `email`/`password`. Obtain it from the MetaMCP " +
					"web UI (DevTools → Application → Cookies → " +
					"`better-auth.session_token`). Sessions last 7 days and are " +
					"refreshed on use. May also be set with the `METAMCP_COOKIE` " +
					"environment variable.",
			},
		},
	}
}

// Configure builds a client and stashes it for the resources to use.
func (p *MetaMCPProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config MetaMCPProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Explicit configuration wins; the environment fills the gaps. This is the
	// order an operator expects, and it keeps credentials out of .tf files.
	endpoint := firstNonEmpty(config.Endpoint, "METAMCP_ENDPOINT", "")
	email := firstNonEmpty(config.Email, "METAMCP_EMAIL", "")
	password := firstNonEmpty(config.Password, "METAMCP_PASSWORD", "")
	cookie := firstNonEmpty(config.Cookie, "METAMCP_COOKIE", "")

	// Unknown values only appear mid-plan when derived from another resource,
	// which cannot happen for provider credentials; treat them as unset.
	if config.Endpoint.IsUnknown() || config.Email.IsUnknown() ||
		config.Password.IsUnknown() || config.Cookie.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Unknown provider configuration",
			"The provider configuration cannot be derived from values that are "+
				"not known until apply. Set endpoint and credentials directly or "+
				"via environment variables.",
		)
		return
	}

	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Missing MetaMCP endpoint",
			"Set endpoint, or the METAMCP_ENDPOINT environment variable, to the "+
				"base URL of your MetaMCP instance (for example "+
				"https://metamcp.example.com).",
		)
		return
	}

	hasStatic := cookie != ""
	hasCreds := email != "" && password != ""
	if !hasStatic && !hasCreds {
		resp.Diagnostics.AddAttributeError(
			path.Root("email"),
			"Missing MetaMCP credentials",
			"Either set cookie, or set both email and password. These may also "+
				"be supplied with the METAMCP_COOKIE, or METAMCP_EMAIL and "+
				"METAMCP_PASSWORD, environment variables.",
		)
		return
	}
	if !hasStatic && email != "" && password == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("password"),
			"Missing MetaMCP password",
			"email was set without password.",
		)
		return
	}
	if !hasStatic && password != "" && email == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("email"),
			"Missing MetaMCP email",
			"password was set without email. MetaMCP signs in by email address; "+
				"a username is not accepted.",
		)
		return
	}

	c, err := client.New(client.Config{
		Endpoint:  endpoint,
		Email:     email,
		Password:  password,
		Cookie:    cookie,
		UserAgent: "terraform-provider-metamcp/" + p.version,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create MetaMCP client", err.Error())
		return
	}

	// Validate the credentials now rather than letting the first resource fail
	// mid-apply. A terraform plan should not half-succeed because a password
	// was wrong.
	if err := c.SignIn(ctx); err != nil {
		resp.Diagnostics.AddError("Unable to authenticate to MetaMCP", err.Error())
		return
	}

	resp.DataSourceData = c
	resp.ResourceData = c
}

// Resources returns every resource this provider implements.
func (p *MetaMCPProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewNamespaceResource,
		NewMcpServerResource,
		NewEndpointResource,
		NewAPIKeyResource,
	}
}

// DataSources returns every data source this provider implements.
func (p *MetaMCPProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewNamespaceDataSource,
		NewMcpServerDataSource,
		NewEndpointDataSource,
		NewAPIKeyDataSource,
	}
}

// firstNonEmpty returns the first non-empty value, treating an unknown or null
// attribute as empty, then falling back to the named environment variable.
func firstNonEmpty(v types.String, env, fallback string) string {
	if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		return v.ValueString()
	}
	if env != "" {
		if s := os.Getenv(env); s != "" {
			return s
		}
	}
	return fallback
}

// stringOrNull converts an optional string pointer to a Terraform value.
func stringOrNull(s *string) types.String {
	if s == nil {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

// stringPtr converts a Terraform value to an optional string pointer.
func stringPtr(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return v.ValueStringPointer()
}

// stringMap converts a Terraform map to a Go map, returning nil when unset so
// an omitted attribute is not sent as an empty object.
//
// ElementsAs reports problems as diag.Diagnostics rather than error, so these
// helpers pass that through and let the caller append it to its own response.
func stringMap(ctx context.Context, m types.Map, diags *diag.Diagnostics) map[string]string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	diags.Append(m.ElementsAs(ctx, &out, false)...)
	return out
}

// stringList converts a Terraform list to a Go slice.
func stringList(ctx context.Context, l types.List, diags *diag.Diagnostics) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	out := []string{}
	diags.Append(l.ElementsAs(ctx, &out, false)...)
	return out
}

// stringMapValue converts a Go map to a Terraform map value.
func stringMapValue(ctx context.Context, m map[string]string, diags *diag.Diagnostics) types.Map {
	if m == nil {
		return types.MapNull(types.StringType)
	}
	v, d := types.MapValueFrom(ctx, types.StringType, m)
	diags.Append(d...)
	return v
}

// stringListValue converts a Go slice to a Terraform list value.
func stringListValue(ctx context.Context, s []string, diags *diag.Diagnostics) types.List {
	if s == nil {
		return types.ListNull(types.StringType)
	}
	v, d := types.ListValueFrom(ctx, types.StringType, s)
	diags.Append(d...)
	return v
}
