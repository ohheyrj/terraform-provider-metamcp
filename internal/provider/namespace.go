package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/client"
)

var (
	_ resource.Resource                = &namespaceResource{}
	_ resource.ResourceWithConfigure   = &namespaceResource{}
	_ resource.ResourceWithImportState = &namespaceResource{}

	// uuidPattern matches the UUID shape the API returns and accepts.
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F-]{32,36}$`)
)

// NewNamespaceResource returns a new metamcp_namespace resource.
func NewNamespaceResource() resource.Resource {
	return &namespaceResource{}
}

type namespaceResource struct {
	client *client.Client
}

// namespaceResourceModel maps the resource schema.
type namespaceResourceModel struct {
	UUID        types.String `tfsdk:"uuid"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`

	// McpServerUUIDs is the set of servers associated with this namespace. The
	// API returns the associated servers (as full objects) from namespaces.get,
	// but returns plain namespaces from namespaces.list, so this attribute is
	// only accurate when read back with GetNamespace.
	McpServerUUIDs types.Set `tfsdk:"mcp_server_uuids"`
}

func (r *namespaceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_namespace"
}

func (r *namespaceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a MetaMCP namespace: a grouping of MCP servers " +
			"published together behind one or more endpoints.",
		Attributes: map[string]schema.Attribute{
			"uuid": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Server-assigned namespace identifier.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Namespace name. Must not be empty.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-text description.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation timestamp, as reported by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last-modified timestamp, as reported by the API.",
			},
			"mcp_server_uuids": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				MarkdownDescription: "UUIDs of the MCP servers associated with this namespace.\n\n" +
					"Reference `metamcp_mcp_server.<name>.uuid` here to attach a server. " +
					"Servers are attached by UUID, so this resource does not need to " +
					"depend on the server's other attributes.\n\n" +
					"Note that the association is *authoritative*: removing a UUID from " +
					"this set detaches that server, including one attached outside " +
					"Terraform.",
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *namespaceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected resource Configure type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", req.ProviderData),
		)
		return
	}
	r.client = c
}

func (r *namespaceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan namespaceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateNamespace(ctx, client.NamespaceInput{
		Name:           plan.Name.ValueString(),
		Description:    stringPtr(plan.Description),
		McpServerUUIDs: stringSet(ctx, plan.McpServerUUIDs, &resp.Diagnostics),
	})
	if resp.Diagnostics.HasError() {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to create MetaMCP namespace",
			fmt.Sprintf("Creating namespace %q failed: %s", plan.Name.ValueString(), err))
		return
	}

	// Read the namespace back so the association reflects what the server
	// actually stored, rather than echoing the request.
	if after, err := r.client.GetNamespace(ctx, created.UUID); err == nil {
		created = after
	}
	resp.Diagnostics.Append(applyNamespace(ctx, &plan, created)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *namespaceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state namespaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, err := r.client.GetNamespace(ctx, state.UUID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		// Gone from the API: drop it from state so Terraform plans a recreate.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read MetaMCP namespace",
			fmt.Sprintf("Reading namespace %s failed: %s", state.UUID.ValueString(), err))
		return
	}

	resp.Diagnostics.Append(applyNamespace(ctx, &state, got)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *namespaceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state namespaceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	updated, err := r.client.UpdateNamespace(ctx, state.UUID.ValueString(), client.NamespaceInput{
		Name:           plan.Name.ValueString(),
		Description:    stringPtr(plan.Description),
		McpServerUUIDs: stringSet(ctx, plan.McpServerUUIDs, &resp.Diagnostics),
	})
	if resp.Diagnostics.HasError() {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to update MetaMCP namespace",
			fmt.Sprintf("Updating namespace %s failed: %s", state.UUID.ValueString(), err))
		return
	}

	if after, err := r.client.GetNamespace(ctx, updated.UUID); err == nil {
		updated = after
	}
	resp.Diagnostics.Append(applyNamespace(ctx, &plan, updated)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *namespaceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state namespaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteNamespace(ctx, state.UUID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Unable to delete MetaMCP namespace",
			fmt.Sprintf("Deleting namespace %s failed: %s", state.UUID.ValueString(), err))
		return
	}
}

func (r *namespaceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !uuidPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError(
			"Invalid namespace import ID",
			fmt.Sprintf("Expected a namespace UUID, got %q. "+
				"Find the UUID with `terraform state` or the MetaMCP web UI.", req.ID),
		)
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("uuid"), req, resp)
}

// applyNamespace copies API values into the Terraform model.
//
// The associated servers are mapped to a sorted set of UUIDs. A response from
// namespaces.list carries no servers array at all, so a nil slice leaves any
// existing set untouched rather than emptying it — otherwise every read would
// look like a namespace losing all of its servers.
func applyNamespace(ctx context.Context, m *namespaceResourceModel, n *client.Namespace) diag.Diagnostics {
	var diags diag.Diagnostics

	m.UUID = types.StringValue(n.UUID)
	m.Name = types.StringValue(n.Name)
	m.Description = stringOrNull(n.Description)
	m.CreatedAt = types.StringValue(n.CreatedAt)
	m.UpdatedAt = types.StringValue(n.UpdatedAt)

	if n.Servers != nil {
		uuids := make([]string, 0, len(n.Servers))
		for _, srv := range n.Servers {
			uuids = append(uuids, srv.UUID)
		}
		sort.Strings(uuids)
		v, d := types.SetValueFrom(ctx, types.StringType, uuids)
		diags.Append(d...)
		m.McpServerUUIDs = v
	}

	return diags
}
