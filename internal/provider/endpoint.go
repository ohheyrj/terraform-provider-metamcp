package provider

import (
	"context"
	"errors"
	"fmt"

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
	_ resource.Resource                = &endpointResource{}
	_ resource.ResourceWithConfigure   = &endpointResource{}
	_ resource.ResourceWithImportState = &endpointResource{}
)

// NewEndpointResource returns a new metamcp_endpoint resource.
func NewEndpointResource() resource.Resource {
	return &endpointResource{}
}

type endpointResource struct {
	client *client.Client
}

type endpointResourceModel struct {
	UUID              types.String `tfsdk:"uuid"`
	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	NamespaceUUID     types.String `tfsdk:"namespace_uuid"`
	EnableAPIKeyAuth  types.Bool   `tfsdk:"enable_api_key_auth"`
	EnableOauth       types.Bool   `tfsdk:"enable_oauth"`
	UseQueryParamAuth types.Bool   `tfsdk:"use_query_param_auth"`
	CreateMcpServer   types.Bool   `tfsdk:"create_mcp_server"`
	CreatedAt         types.String `tfsdk:"created_at"`
	UpdatedAt         types.String `tfsdk:"updated_at"`
	URL               types.String `tfsdk:"url"`

	// IsPublic maps to the API's user_id field exactly as it does on a
	// namespace and an MCP server: a null owner means public.
	//
	// Visibility on an endpoint is not cosmetic — it decides who may reach the
	// endpoint's URL and who may administer it: the API's lookup middleware
	// lets any authenticated caller use a public endpoint, and its update and
	// delete paths refuse to touch an endpoint whose owner is someone else.
	IsPublic types.Bool `tfsdk:"is_public"`
}

func (r *endpointResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_endpoint"
}

func (r *endpointResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a MetaMCP endpoint: the URL an MCP client " +
			"connects to, publishing one namespace's servers behind an auth mode.\n\n" +
			"A namespace holds the servers; an endpoint exposes them. Create the " +
			"namespace first and reference its `uuid` here.",
		Attributes: map[string]schema.Attribute{
			"uuid": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"description": schema.StringAttribute{
				Optional: true,
			},
			"namespace_uuid": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "UUID of the namespace this endpoint publishes.",
			},
			"enable_api_key_auth": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Allow MCP API keys to authenticate. Defaults to `true`.",
			},
			"enable_oauth": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Allow OAuth authentication. Defaults to `false`.",
			},
			"use_query_param_auth": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Accept the credential as a query parameter instead of a header.",
			},
			"create_mcp_server": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				MarkdownDescription: "Create a MetaMCP server for this endpoint. " +
					"Only meaningful at creation; the API ignores it on update.",
			},
			"created_at": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Computed: true,
			},
			"url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The MCP URL clients connect to, derived from the endpoint's name.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"is_public": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the endpoint is public, i.e. reachable by every " +
					"user rather than only its owner.\n\n" +
					"MetaMCP encodes this as the absence of an owner, so setting it " +
					"`true` clears ownership and setting it `false` claims the " +
					"endpoint for the authenticated user. Left unset, the endpoint " +
					"is created private for the authenticated user.\n\n" +
					"A public endpoint may only publish a public namespace, so this " +
					"constrains which namespace the endpoint may point at. The API " +
					"refuses the combination with its own message. See " +
					"`metamcp_namespace.is_public`.\n\n" +
					"**Changing this on an existing endpoint is not supported by the " +
					"MetaMCP API.** Its update procedure does not accept the " +
					"ownership field at all — only create does — so ownership is " +
					"settled when the endpoint is created and cannot be altered " +
					"afterwards. The provider asks for a replacement instead, which " +
					"creates a new endpoint and URL. Use `create_mcp_server` to " +
					"control whether a replacement also creates its companion " +
					"server. To set visibility without a replacement, `terraform " +
					"state rm` the endpoint and re-import it after changing it in " +
					"MetaMCP's own UI — note that import adopts whatever ownership is " +
					"already there, so this attribute follows the server either way.\n\n" +
					"This is `Optional`+`Computed` because ownership can only be read " +
					"back, never derived from configuration alone.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
					endpointVisibilityRequiresReplace{},
				},
			},
		},
	}
}

// endpointVisibilityRequiresReplace forces a replacement when is_public changes.
//
// The API's endpoints.update does not pass user_id to the repository at all,
// while endpoints.create does — so ownership is fixed at creation and a changed
// is_public can never be applied in place. Left as an in-place update, Terraform
// would send the new ownership, the API would silently keep the old one, and the
// read-back would then disagree with the plan:
//
//	Provider produced inconsistent result after apply
//	.is_public: was cty.True, but now cty.False
//
// which surfaces *after* the resource has already been changed. Failing loudly at
// plan time is the alternative, but it would make the whole endpoint
// unmanageable for anyone who only wanted to change its namespace, so replacing
// is the lesser evil: it is what the API forces, made explicit.
//
// The two-way rule for the value itself is the same one the API documents for
// ownership: a change to true means a public endpoint, which may only publish a
// public namespace.
type endpointVisibilityRequiresReplace struct{}

func (endpointVisibilityRequiresReplace) Description(_ context.Context) string {
	return "changing endpoint visibility requires replacing the endpoint, " +
		"because the MetaMCP API cannot update it"
}

func (m endpointVisibilityRequiresReplace) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (endpointVisibilityRequiresReplace) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	// A create or destroy has nothing to replace.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	// An unknown value cannot be compared. It is also the case the framework
	// treats as "cannot yet decide", so the next plan resolves it.
	if req.PlanValue.IsUnknown() || req.StateValue.IsUnknown() {
		return
	}
	if req.PlanValue.Equal(req.StateValue) {
		return
	}
	resp.RequiresReplace = true
	// Keep the planned value and let the framework carry it to the replacement,
	// so the new endpoint is created with the visibility that was asked for.
	resp.PlanValue = req.PlanValue
}

func (r *endpointResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *endpointResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan endpointResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, err := userIDForVisibility(plan.IsPublic, r.client.UserID(), true)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("is_public"), "Invalid visibility", err.Error())
		return
	}

	created, err := r.client.CreateEndpoint(ctx, client.EndpointInput{
		Name:              plan.Name.ValueString(),
		Description:       managedStringPtr(plan.Description),
		NamespaceUUID:     plan.NamespaceUUID.ValueString(),
		EnableAPIKeyAuth:  plan.EnableAPIKeyAuth.ValueBoolPointer(),
		EnableOauth:       plan.EnableOauth.ValueBoolPointer(),
		UseQueryParamAuth: plan.UseQueryParamAuth.ValueBoolPointer(),
		CreateMcpServer:   plan.CreateMcpServer.ValueBoolPointer(),
		UserID:            owner,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create MetaMCP endpoint",
			fmt.Sprintf("Creating endpoint %q failed: %s", plan.Name.ValueString(), err))
		return
	}

	applyEndpoint(&plan, created, r.client.Endpoint())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *endpointResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state endpointResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, err := r.client.GetEndpoint(ctx, state.UUID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read MetaMCP endpoint",
			fmt.Sprintf("Reading endpoint %s failed: %s", state.UUID.ValueString(), err))
		return
	}

	applyEndpoint(&state, got, r.client.Endpoint())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *endpointResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state endpointResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Defence in depth. endpoints.update does not accept the ownership field, so
	// a visibility change here could only ever be silently ignored — which would
	// then fail the apply with "inconsistent result after apply". The schema's
	// plan modifier replaces instead, so this should be unreachable; if it is
	// ever reached, saying so plainly beats a silent divergence.
	if !plan.IsPublic.Equal(state.IsPublic) {
		resp.Diagnostics.AddAttributeError(path.Root("is_public"), "Visibility cannot be changed in place",
			"The MetaMCP API cannot update an endpoint's ownership: only endpoints.create accepts it. "+
				"Change is_public by replacing the endpoint instead. This is a bug in the provider's "+
				"plan modifiers; please report it.")
		return
	}

	// namespaceUuid is required by the update schema even though it is not
	// changing, so it is always sent.
	//
	// Ownership is deliberately NOT sent: the API ignores it on update, so
	// including it would only make the request look like it was doing something
	// it is not.
	updated, err := r.client.UpdateEndpoint(ctx, state.UUID.ValueString(), client.EndpointUpdateInput{
		Name:              plan.Name.ValueString(),
		Description:       managedStringPtr(plan.Description),
		NamespaceUUID:     plan.NamespaceUUID.ValueString(),
		EnableAPIKeyAuth:  plan.EnableAPIKeyAuth.ValueBoolPointer(),
		EnableOauth:       plan.EnableOauth.ValueBoolPointer(),
		UseQueryParamAuth: plan.UseQueryParamAuth.ValueBoolPointer(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update MetaMCP endpoint",
			fmt.Sprintf("Updating endpoint %s failed: %s", state.UUID.ValueString(), err))
		return
	}

	applyEndpoint(&plan, updated, r.client.Endpoint())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *endpointResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state endpointResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteEndpoint(ctx, state.UUID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Unable to delete MetaMCP endpoint",
			fmt.Sprintf("Deleting endpoint %s failed: %s", state.UUID.ValueString(), err))
	}
}

func (r *endpointResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !uuidPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid endpoint import ID",
			fmt.Sprintf("Expected an endpoint UUID, got %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("uuid"), req, resp)
}

// applyEndpoint copies API values into the model.
//
// The client-facing MCP URL is derived rather than read back: MetaMCP builds it
// as /metamcp/<endpoint name>/mcp, and endpoint names are globally unique
// precisely because they appear in that path. The API returns no URL field.
func applyEndpoint(m *endpointResourceModel, e *client.Endpoint, baseURL string) {
	m.UUID = types.StringValue(e.UUID)
	m.URL = types.StringValue(baseURL + "/metamcp/" + e.Name + "/mcp")
	m.Name = types.StringValue(e.Name)
	m.Description = stringOrNull(e.Description)
	m.NamespaceUUID = types.StringValue(e.NamespaceUUID)
	m.EnableAPIKeyAuth = types.BoolValue(e.EnableAPIKeyAuth)
	m.EnableOauth = types.BoolValue(e.EnableOauth)
	m.UseQueryParamAuth = types.BoolValue(e.UseQueryParamAuth)
	m.CreatedAt = types.StringValue(e.CreatedAt)
	m.UpdatedAt = types.StringValue(e.UpdatedAt)
	// Same encoding as a namespace and a server: a null owner means public.
	m.IsPublic = types.BoolValue(e.UserID == nil)
}

// ---------------------------------------------------------------- data source

var (
	_ datasource.DataSource              = &endpointDataSource{}
	_ datasource.DataSourceWithConfigure = &endpointDataSource{}
)

type endpointDataSourceModel struct {
	UUID              types.String `tfsdk:"uuid"`
	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	NamespaceUUID     types.String `tfsdk:"namespace_uuid"`
	EnableAPIKeyAuth  types.Bool   `tfsdk:"enable_api_key_auth"`
	EnableOauth       types.Bool   `tfsdk:"enable_oauth"`
	UseQueryParamAuth types.Bool   `tfsdk:"use_query_param_auth"`
	CreatedAt         types.String `tfsdk:"created_at"`
	UpdatedAt         types.String `tfsdk:"updated_at"`

	// IsPublic mirrors the resource: a null owner in the API means public.
	IsPublic types.Bool `tfsdk:"is_public"`
}

type endpointDataSource struct {
	client *client.Client
}

// NewEndpointDataSource returns a new metamcp_endpoint data source.
func NewEndpointDataSource() datasource.DataSource {
	return &endpointDataSource{}
}

func (d *endpointDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_endpoint"
}

func (d *endpointDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{
		MarkdownDescription: "Looks up an existing MetaMCP endpoint by name or UUID.",
		Attributes: map[string]dsschema.Attribute{
			"uuid": dsschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Endpoint UUID. Exactly one of `uuid` or `name` must be set.",
			},
			"name": dsschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Endpoint name. Exactly one of `uuid` or `name` must be set.",
			},
			"description":          dsschema.StringAttribute{Computed: true},
			"namespace_uuid":       dsschema.StringAttribute{Computed: true},
			"enable_api_key_auth":  dsschema.BoolAttribute{Computed: true},
			"enable_oauth":         dsschema.BoolAttribute{Computed: true},
			"use_query_param_auth": dsschema.BoolAttribute{Computed: true},
			"created_at":           dsschema.StringAttribute{Computed: true},
			"updated_at":           dsschema.StringAttribute{Computed: true},
			"is_public": dsschema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether the endpoint is public. MetaMCP encodes this " +
					"as the absence of an owner.",
			},
		},
	}
}

func (d *endpointDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *endpointDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config endpointDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	hasUUID := !config.UUID.IsNull() && !config.UUID.IsUnknown()
	hasName := !config.Name.IsNull() && !config.Name.IsUnknown()
	if hasUUID == hasName {
		resp.Diagnostics.AddError("Invalid endpoint lookup", "Set exactly one of `uuid` or `name`.")
		return
	}

	var found *client.Endpoint
	if hasUUID {
		got, err := d.client.GetEndpoint(ctx, config.UUID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Unable to read MetaMCP endpoint", err.Error())
			return
		}
		found = got
	} else {
		list, err := d.client.ListEndpoints(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Unable to list MetaMCP endpoints", err.Error())
			return
		}
		for i := range list {
			if list[i].Name == config.Name.ValueString() {
				found = &list[i]
				break
			}
		}
		if found == nil {
			resp.Diagnostics.AddError("MetaMCP endpoint not found",
				fmt.Sprintf("No endpoint named %q.", config.Name.ValueString()))
			return
		}
	}

	config.UUID = types.StringValue(found.UUID)
	config.Name = types.StringValue(found.Name)
	config.Description = stringOrNull(found.Description)
	config.NamespaceUUID = types.StringValue(found.NamespaceUUID)
	config.EnableAPIKeyAuth = types.BoolValue(found.EnableAPIKeyAuth)
	config.EnableOauth = types.BoolValue(found.EnableOauth)
	config.UseQueryParamAuth = types.BoolValue(found.UseQueryParamAuth)
	config.CreatedAt = types.StringValue(found.CreatedAt)
	config.UpdatedAt = types.StringValue(found.UpdatedAt)
	config.IsPublic = types.BoolValue(found.UserID == nil)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
