package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/client"
)

// namespaceDataSource reads a namespace by name.
var (
	_ datasource.DataSource              = &namespaceDataSource{}
	_ datasource.DataSourceWithConfigure = &namespaceDataSource{}
)

type namespaceDataSourceModel struct {
	UUID        types.String `tfsdk:"uuid"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

type namespaceDataSource struct {
	client *client.Client
}

// NewNamespaceDataSource returns a new metamcp_namespace data source.
func NewNamespaceDataSource() datasource.DataSource {
	return &namespaceDataSource{}
}

func (d *namespaceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_namespace"
}

func (d *namespaceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{
		MarkdownDescription: "Looks up an existing MetaMCP namespace by name or UUID.",
		Attributes: map[string]dsschema.Attribute{
			"uuid": dsschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Namespace UUID. Exactly one of `uuid` or `name` must be set.",
			},
			"name": dsschema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Namespace name. Exactly one of `uuid` or `name` must be set.",
			},
			"description": dsschema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Free-text description.",
			},
			"created_at": dsschema.StringAttribute{
				Computed: true,
			},
			"updated_at": dsschema.StringAttribute{
				Computed: true,
			},
		},
	}
}

func (d *namespaceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected data source Configure type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", req.ProviderData),
		)
		return
	}
	d.client = c
}

func (d *namespaceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config namespaceDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	hasUUID := !config.UUID.IsNull() && !config.UUID.IsUnknown()
	hasName := !config.Name.IsNull() && !config.Name.IsUnknown()
	if hasUUID == hasName {
		resp.Diagnostics.AddError(
			"Invalid namespace lookup",
			"Set exactly one of `uuid` or `name`.",
		)
		return
	}

	var found *client.Namespace
	if hasUUID {
		got, err := d.client.GetNamespace(ctx, config.UUID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Unable to read MetaMCP namespace", err.Error())
			return
		}
		found = got
	} else {
		list, err := d.client.ListNamespaces(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Unable to list MetaMCP namespaces", err.Error())
			return
		}
		for i := range list {
			if list[i].Name == config.Name.ValueString() {
				found = &list[i]
				break
			}
		}
		if found == nil {
			resp.Diagnostics.AddError(
				"MetaMCP namespace not found",
				fmt.Sprintf("No namespace named %q.", config.Name.ValueString()),
			)
			return
		}
	}

	config.UUID = types.StringValue(found.UUID)
	config.Name = types.StringValue(found.Name)
	config.Description = stringOrNull(found.Description)
	config.CreatedAt = types.StringValue(found.CreatedAt)
	config.UpdatedAt = types.StringValue(found.UpdatedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
