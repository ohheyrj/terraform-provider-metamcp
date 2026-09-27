package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/client"
)

var (
	_ resource.Resource                = &apiKeyResource{}
	_ resource.ResourceWithConfigure   = &apiKeyResource{}
	_ resource.ResourceWithImportState = &apiKeyResource{}
)

// apiKeyNamePattern mirrors the API's own name validation.
var apiKeyNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_\s-]+$`)

// NewAPIKeyResource returns a new metamcp_api_key resource.
func NewAPIKeyResource() resource.Resource {
	return &apiKeyResource{}
}

type apiKeyResource struct {
	client *client.Client
}

type apiKeyResourceModel struct {
	UUID      types.String `tfsdk:"uuid"`
	Name      types.String `tfsdk:"name"`
	Key       types.String `tfsdk:"key"`
	IsActive  types.Bool   `tfsdk:"is_active"`
	CreatedAt types.String `tfsdk:"created_at"`
}

func (r *apiKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *apiKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a MetaMCP API key.\n\n" +
			"API keys authenticate the **MCP gateway** " +
			"(`/metamcp/<endpoint>/mcp`), not the admin API. Terraform itself " +
			"authenticates with an email and password (or a session cookie), so a " +
			"key created here cannot be used to run this provider.\n\n" +
			"The generated `key` is a live credential. It is marked sensitive and " +
			"is also written to Terraform state — use encrypted remote state, and " +
			"treat it as a secret.",
		Attributes: map[string]schema.Attribute{
			"uuid": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Key name. Letters, numbers, spaces, underscores " +
					"and hyphens only, at most 100 characters.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 100),
					stringvalidator.RegexMatches(
						apiKeyNamePattern,
						"must contain only letters, numbers, spaces, underscores and hyphens",
					),
				},
			},
			"key": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				MarkdownDescription: "The API key value. Returned by MetaMCP when the " +
					"key is created or listed, and stored in Terraform state.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"is_active": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the key is accepted. Defaults to `true`.",
			},
			"created_at": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *apiKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected resource Configure type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	r.client = c
}

func (r *apiKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apiKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// CreateApiKeyResponseSchema carries no is_active field, so the value is
	// taken from the plan rather than read back — reading it would zero it.
	active := plan.IsActive.ValueBool()

	created, err := r.client.CreateAPIKey(ctx, client.APIKeyInput{
		Name:     plan.Name.ValueString(),
		IsActive: &active,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create MetaMCP API key",
			fmt.Sprintf("Creating API key %q failed: %s", plan.Name.ValueString(), err))
		return
	}

	plan.UUID = types.StringValue(created.UUID)
	plan.Key = types.StringValue(created.Secret)
	plan.IsActive = types.BoolValue(active)
	plan.CreatedAt = types.StringValue(created.CreatedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apiKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// There is no apiKeys.get procedure, so a read lists and filters.
	got, err := r.client.GetAPIKeyByUUID(ctx, state.UUID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read MetaMCP API key",
			fmt.Sprintf("Reading API key %s failed: %s", state.UUID.ValueString(), err))
		return
	}

	state.Name = types.StringValue(got.Name)
	state.IsActive = types.BoolValue(got.IsActive)
	state.CreatedAt = types.StringValue(got.CreatedAt)
	// The list response includes the key itself, so it can be refreshed rather
	// than only set at create time.
	if got.Secret != "" {
		state.Key = types.StringValue(got.Secret)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *apiKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state apiKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.Name.ValueString()
	active := plan.IsActive.ValueBool()

	// UpdateApiKeyResponseSchema omits is_active, so the effective values are
	// carried forward from the plan instead of from the response.
	updated, err := r.client.UpdateAPIKey(ctx, state.UUID.ValueString(), client.APIKeyUpdateInput{
		Name:     &name,
		IsActive: &active,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update MetaMCP API key",
			fmt.Sprintf("Updating API key %s failed: %s", state.UUID.ValueString(), err))
		return
	}

	plan.UUID = types.StringValue(state.UUID.ValueString())
	plan.Key = state.Key // an update never returns the secret
	plan.IsActive = types.BoolValue(active)
	if updated.CreatedAt != "" {
		plan.CreatedAt = types.StringValue(updated.CreatedAt)
	} else {
		plan.CreatedAt = state.CreatedAt
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apiKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteAPIKey(ctx, state.UUID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Unable to delete MetaMCP API key",
			fmt.Sprintf("Deleting API key %s failed: %s", state.UUID.ValueString(), err))
	}
}

func (r *apiKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !uuidPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid API key import ID",
			fmt.Sprintf("Expected an API key UUID, got %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("uuid"), req, resp)
}

// ---------------------------------------------------------------- data source

var (
	_ datasource.DataSource              = &apiKeyDataSource{}
	_ datasource.DataSourceWithConfigure = &apiKeyDataSource{}
)

type apiKeyDataSourceModel struct {
	UUID      types.String `tfsdk:"uuid"`
	Name      types.String `tfsdk:"name"`
	Key       types.String `tfsdk:"key"`
	IsActive  types.Bool   `tfsdk:"is_active"`
	CreatedAt types.String `tfsdk:"created_at"`
}

type apiKeyDataSource struct {
	client *client.Client
}

// NewAPIKeyDataSource returns a new metamcp_api_key data source.
func NewAPIKeyDataSource() datasource.DataSource {
	return &apiKeyDataSource{}
}

func (d *apiKeyDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (d *apiKeyDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{
		MarkdownDescription: "Looks up an existing MetaMCP API key by name or UUID.\n\n" +
			"The key value is exposed and marked sensitive.",
		Attributes: map[string]dsschema.Attribute{
			"uuid": dsschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "API key UUID. Exactly one of `uuid` or `name` must be set.",
			},
			"name": dsschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "API key name. Exactly one of `uuid` or `name` must be set.",
			},
			"key":        dsschema.StringAttribute{Computed: true, Sensitive: true},
			"is_active":  dsschema.BoolAttribute{Computed: true},
			"created_at": dsschema.StringAttribute{Computed: true},
		},
	}
}

func (d *apiKeyDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected data source Configure type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	d.client = c
}

func (d *apiKeyDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config apiKeyDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	hasUUID := !config.UUID.IsNull() && !config.UUID.IsUnknown()
	hasName := !config.Name.IsNull() && !config.Name.IsUnknown()
	if hasUUID == hasName {
		resp.Diagnostics.AddError("Invalid API key lookup", "Set exactly one of `uuid` or `name`.")
		return
	}

	list, err := d.client.ListAPIKeys(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list MetaMCP API keys", err.Error())
		return
	}

	var found *client.APIKey
	for i := range list {
		if (hasUUID && list[i].UUID == config.UUID.ValueString()) ||
			(hasName && list[i].Name == config.Name.ValueString()) {
			found = &list[i]
			break
		}
	}
	if found == nil {
		resp.Diagnostics.AddError("MetaMCP API key not found",
			"No API key matched the given identifier.")
		return
	}

	config.UUID = types.StringValue(found.UUID)
	config.Name = types.StringValue(found.Name)
	config.Key = types.StringValue(found.Secret)
	config.IsActive = types.BoolValue(found.IsActive)
	config.CreatedAt = types.StringValue(found.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
