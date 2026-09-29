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
		},
	}
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

	created, err := r.client.CreateEndpoint(ctx, client.EndpointInput{
		Name:              plan.Name.ValueString(),
		Description:       managedStringPtr(plan.Description),
		NamespaceUUID:     plan.NamespaceUUID.ValueString(),
		EnableAPIKeyAuth:  plan.EnableAPIKeyAuth.ValueBoolPointer(),
		EnableOauth:       plan.EnableOauth.ValueBoolPointer(),
		UseQueryParamAuth: plan.UseQueryParamAuth.ValueBoolPointer(),
		CreateMcpServer:   plan.CreateMcpServer.ValueBoolPointer(),
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

	// namespaceUuid is required by the update schema even though it is not
	// changing, so it is always sent.
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

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
