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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
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
	IsPublic  types.Bool   `tfsdk:"is_public"`
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
			"is_public": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the key is public, i.e. usable by every " +
					"user rather than only its owner. **Defaults to `false`, " +
					"unlike the other resources**: MetaMCP's own create procedure " +
					"claims a key for the authenticated user when no ownership is " +
					"given, so an omitted `is_public` yields a private key.\n\n" +
					"MetaMCP encodes this as the absence of an owner, so setting it " +
					"`true` clears ownership and setting it `false` claims the key " +
					"for the authenticated user. This is also the flag the MetaMCP " +
					"web UI shows as *Public* / *Private* on the API keys page.\n\n" +
					"**Changing this on an existing key is not supported by the " +
					"MetaMCP API.** `apiKeys.update` does not accept the ownership " +
					"field at all, so a key's visibility is settled at creation. " +
					"The provider asks for a replacement instead — which issues a " +
					"**new secret and invalidates the old one**, so repoint " +
					"everything that holds this key. To avoid that, `terraform " +
					"state rm` the key, change its visibility in MetaMCP's own UI, " +
					"then `terraform import` it back; the provider reads ownership " +
					"from the server either way.\n\n" +
					"This is `Optional`+`Computed` because ownership can only be read " +
					"back, never derived from configuration alone.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
					visibilityFixingReplacement{},
				},
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

	// CreateApiKeyResponseSchema carries no is_active field and no ownership
	// field, so neither is read back here — reading them would zero them.
	// is_active comes from the plan, and ownership is settled below by listing
	// the key back.
	active := plan.IsActive.ValueBool()

	owner, err := userIDForVisibility(plan.IsPublic, r.client.UserID(), true)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("is_public"), "Invalid visibility", err.Error())
		return
	}

	created, err := r.client.CreateAPIKey(ctx, client.APIKeyInput{
		Name:     plan.Name.ValueString(),
		UserID:   owner,
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

	// The only place ownership can be read from: the create response omits it,
	// and a missing field decodes to nil — which is the value that *means*
	// public. Recording that would tell Terraform a key it just made private is
	// public, and the apply would fail with "inconsistent result after apply".
	if listed, err := r.client.GetAPIKeyByUUID(ctx, created.UUID); err == nil {
		created = listed
	}
	applyAPIKey(&plan, created)

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
	// Ownership comes from the list response, which is the only place the API
	// reports it: a null owner means public.
	state.IsPublic = types.BoolValue(got.UserID == nil)
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

	// Defence in depth. apiKeys.update does not accept an ownership field, so a
	// visibility change here could only ever be silently ignored — which would
	// then fail the apply with "inconsistent result after apply" once the key
	// had already been touched. The schema's plan modifier replaces instead, so
	// this should be unreachable; if it is ever reached, saying so plainly beats
	// a silent divergence.
	if !plan.IsPublic.Equal(state.IsPublic) {
		resp.Diagnostics.AddAttributeError(path.Root("is_public"), "Visibility cannot be changed in place",
			"The MetaMCP API cannot update an API key's ownership: apiKeys.update does not accept the field. "+
				"Change is_public by replacing the key instead. This is a bug in the provider's "+
				"plan modifiers; please report it.")
		return
	}

	name := plan.Name.ValueString()
	active := plan.IsActive.ValueBool()

	// UpdateApiKeyResponseSchema includes is_active but not user_id, so is_active
	// could be read back while ownership cannot — reading ownership from here
	// would report every key as public.
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
	plan.IsPublic = state.IsPublic
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
	IsPublic  types.Bool   `tfsdk:"is_public"`
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
			"key":       dsschema.StringAttribute{Computed: true, Sensitive: true},
			"is_active": dsschema.BoolAttribute{Computed: true},
			"is_public": dsschema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether the key is public. MetaMCP encodes this as the " +
					"absence of an owner, so a key with no owning user is public.",
			},
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
	config.IsPublic = types.BoolValue(found.UserID == nil)
	config.CreatedAt = types.StringValue(found.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// applyAPIKey copies the API's values into a Terraform model.
//
// Ownership is taken from UserID, which only ListAPIKeys populates, so this must
// be called with a key obtained from a list rather than from a create or update
// response.
func applyAPIKey(m *apiKeyResourceModel, k *client.APIKey) {
	m.IsPublic = types.BoolValue(k.UserID == nil)
}
