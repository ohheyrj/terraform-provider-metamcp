package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/client"
)

var (
	_ datasource.DataSource              = &mcpServerDataSource{}
	_ datasource.DataSourceWithConfigure = &mcpServerDataSource{}
)

type mcpServerDataSourceModel struct {
	UUID        types.String `tfsdk:"uuid"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Type        types.String `tfsdk:"type"`
	Command     types.String `tfsdk:"command"`
	Args        types.List   `tfsdk:"args"`
	URL         types.String `tfsdk:"url"`
	CreatedAt   types.String `tfsdk:"created_at"`
	ErrorStatus types.String `tfsdk:"error_status"`

	// IsPublic mirrors the resource: a null owner in the API means public.
	IsPublic types.Bool `tfsdk:"is_public"`
}

type mcpServerDataSource struct {
	client *client.Client
}

// NewMcpServerDataSource returns a new metamcp_mcp_server data source.
func NewMcpServerDataSource() datasource.DataSource {
	return &mcpServerDataSource{}
}

func (d *mcpServerDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_server"
}

func (d *mcpServerDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{
		MarkdownDescription: "Looks up an existing MCP server by name or UUID.\n\n" +
			"Credential fields (`env`, `headers`, `bearer_token`) are deliberately " +
			"not exposed here: they belong in the resource, where Terraform tracks " +
			"them as sensitive.",
		Attributes: map[string]dsschema.Attribute{
			"uuid": dsschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Server UUID. Exactly one of `uuid` or `name` must be set.",
			},
			"name": dsschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Server name. Exactly one of `uuid` or `name` must be set.",
			},
			"description": dsschema.StringAttribute{Computed: true},
			"type":        dsschema.StringAttribute{Computed: true},
			"command":     dsschema.StringAttribute{Computed: true},
			"args": dsschema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
			},
			"url":          dsschema.StringAttribute{Computed: true},
			"created_at":   dsschema.StringAttribute{Computed: true},
			"error_status": dsschema.StringAttribute{Computed: true},
			"is_public": dsschema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether the server is public. MetaMCP encodes this " +
					"as the absence of an owner.",
			},
		},
	}
}

func (d *mcpServerDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *mcpServerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config mcpServerDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	hasUUID := !config.UUID.IsNull() && !config.UUID.IsUnknown()
	hasName := !config.Name.IsNull() && !config.Name.IsUnknown()
	if hasUUID == hasName {
		resp.Diagnostics.AddError("Invalid MCP server lookup", "Set exactly one of `uuid` or `name`.")
		return
	}

	var found *client.McpServer
	if hasUUID {
		got, err := d.client.GetMcpServer(ctx, config.UUID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Unable to read MetaMCP server", err.Error())
			return
		}
		found = got
	} else {
		list, err := d.client.ListMcpServers(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Unable to list MetaMCP servers", err.Error())
			return
		}
		for i := range list {
			if list[i].Name == config.Name.ValueString() {
				found = &list[i]
				break
			}
		}
		if found == nil {
			resp.Diagnostics.AddError("MetaMCP server not found",
				fmt.Sprintf("No MCP server named %q.", config.Name.ValueString()))
			return
		}
	}

	config.UUID = types.StringValue(found.UUID)
	config.Name = types.StringValue(found.Name)
	config.Description = stringOrNull(found.Description)
	config.Type = types.StringValue(string(found.Type))
	config.Command = stringOrNull(found.Command)
	config.URL = stringOrNull(found.URL)
	config.CreatedAt = types.StringValue(found.CreatedAt)
	config.ErrorStatus = types.StringValue(found.ErrorStatus)
	config.IsPublic = types.BoolValue(found.UserID == nil)
	config.Args = stringListValue(ctx, found.Args, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
