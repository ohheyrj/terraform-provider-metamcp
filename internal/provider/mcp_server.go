package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ohheyrj/terraform-provider-metamcp/internal/client"
)

var (
	_ resource.Resource                = &mcpServerResource{}
	_ resource.ResourceWithConfigure   = &mcpServerResource{}
	_ resource.ResourceWithImportState = &mcpServerResource{}

	// serverNamePattern mirrors the API's own validation: letters, numbers,
	// underscores and hyphens only, with no consecutive underscores.
	serverNamePattern     = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	consecutiveUnderscore = regexp.MustCompile(`_{2,}`)
)

// NewMcpServerResource returns a new metamcp_mcp_server resource.
func NewMcpServerResource() resource.Resource {
	return &mcpServerResource{}
}

type mcpServerResource struct {
	client *client.Client
}

type mcpServerResourceModel struct {
	UUID        types.String `tfsdk:"uuid"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Type        types.String `tfsdk:"type"`
	Command     types.String `tfsdk:"command"`
	Args        types.List   `tfsdk:"args"`
	Env         types.Map    `tfsdk:"env"`
	URL         types.String `tfsdk:"url"`
	BearerToken types.String `tfsdk:"bearer_token"`
	Headers     types.Map    `tfsdk:"headers"`
	CreatedAt   types.String `tfsdk:"created_at"`

	// IsPublic maps to the API's user_id field: MetaMCP has no explicit
	// visibility flag, it treats a null user_id as "not owned by anyone", which
	// is what makes an object public. Ownership is not returned for a server
	// this provider did not create, so the attribute is Optional+Computed:
	// unset means "leave whatever it is alone", and it is only sent when the
	// value is actually changing.
	IsPublic types.Bool `tfsdk:"is_public"`
}

func (r *mcpServerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_server"
}

func (r *mcpServerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an MCP server registered with MetaMCP.\n\n" +
			"A server is either a local process (`STDIO`, needing `command` and " +
			"`args`) or a remote service (`SSE` or `STREAMABLE_HTTP`, needing " +
			"`url`). Servers are grouped into namespaces and published through " +
			"endpoints.",
		Attributes: map[string]schema.Attribute{
			"uuid": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Server name. Letters, numbers, underscores and " +
					"hyphens only; consecutive underscores are rejected.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(
						serverNamePattern,
						"must contain only letters, numbers, underscores and hyphens",
					),
				},
			},
			"description": schema.StringAttribute{
				Optional: true,
			},
			"type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Transport: `STDIO`, `SSE` or `STREAMABLE_HTTP`.",
				Validators: []validator.String{
					stringvalidator.OneOf("STDIO", "SSE", "STREAMABLE_HTTP"),
				},
			},
			"command": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Executable to run. Required when `type` is `STDIO`.",
			},
			"args": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Arguments for `command`.",
			},
			"env": schema.MapAttribute{
				Optional:            true,
				Sensitive:           true,
				ElementType:         types.StringType,
				MarkdownDescription: "Environment for a `STDIO` server. Marked sensitive because these routinely carry API keys.",
			},
			"url": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Remote MCP endpoint. Required when `type` is `SSE` or `STREAMABLE_HTTP`.",
			},
			"bearer_token": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Bearer token sent when connecting to a remote server.",
			},
			"headers": schema.MapAttribute{
				Optional:            true,
				Sensitive:           true,
				ElementType:         types.StringType,
				MarkdownDescription: "Additional HTTP headers for a remote server. Marked sensitive because these routinely carry credentials.",
			},
			"created_at": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"is_public": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the server is public, i.e. usable by every " +
					"user rather than only its owner.\n\n" +
					"MetaMCP encodes this as the absence of an owner, so setting it " +
					"true clears ownership and setting it false claims the server " +
					"for the authenticated user.\n\n" +
					"The API does not report ownership for servers you do not own, so " +
					"this is `Optional`+`Computed`: leave it unset to manage the " +
					"other attributes without touching visibility.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// userIDForVisibility converts is_public into the API's ownership field.
//
// MetaMCP expresses visibility as ownership, and there is no visibility column:
// a null user_id is what makes an object public. Three outcomes matter, and they
// are not interchangeable:
//
//   - public  -> a pointer to the empty string, which clears the owner
//   - private -> a pointer to the authenticated user's id
//   - unset   -> nil, sending nothing, so the existing owner is left alone
//
// The distinction between "private" and "unset" is the important one. Sending
// the empty string in place of nil would make every update public, and sending
// nil in place of the user's id would fail to make anything private; both fail
// silently, because either way the request is well formed.
//
// onCreate resolves only the null case: a new object has no owner to preserve,
// so an unset is_public claims it for the authenticated user rather than
// defaulting it to public. Omitting the attribute must never widen access.
func userIDForVisibility(isPublic types.Bool, ownUserID string, onCreate bool) (*string, error) {
	if isPublic.IsNull() || isPublic.IsUnknown() {
		if !onCreate {
			return nil, nil
		}
		if ownUserID == "" {
			return nil, errors.New(
				"cannot determine the authenticated user; set is_public explicitly, " +
					"or check that the session reports a user id")
		}
		id := ownUserID
		return &id, nil
	}

	if isPublic.ValueBool() {
		// Public is the ABSENCE of an owner, so this must serialise as an
		// explicit null. A pointer to "" would be sent verbatim and Postgres
		// rejects it: user_id is a foreign key to users.id, so "" is not NULL
		// and matches no row.
		sentinel := client.SendNullUserID
		return &sentinel, nil
	}

	// Private: the owner must be a real user id. The API passes user_id straight
	// through to the database, so an empty string would be stored as a bogus
	// owner rather than being resolved server-side.
	if ownUserID == "" {
		return nil, errors.New(
			"cannot make the server private: the authenticated user id is unknown")
	}
	id := ownUserID
	return &id, nil
}

func (r *mcpServerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// validateTransport enforces the same cross-field rules the API does, so an
// invalid combination fails during plan rather than mid-apply.
func validateTransport(m *mcpServerResourceModel) string {
	t := m.Type.ValueString()
	if t == "STDIO" {
		if m.Command.IsNull() || m.Command.ValueString() == "" {
			return "command is required when type is STDIO"
		}
		return ""
	}
	if m.URL.IsNull() || m.URL.ValueString() == "" {
		return fmt.Sprintf("url is required when type is %s", t)
	}
	if _, err := url.Parse(m.URL.ValueString()); err != nil {
		return fmt.Sprintf("url %q is not valid: %s", m.URL.ValueString(), err)
	}
	if m.Name.ValueString() != "" && consecutiveUnderscore.MatchString(m.Name.ValueString()) {
		return "name must not contain consecutive underscores"
	}
	return ""
}

func (r *mcpServerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan mcpServerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if msg := validateTransport(&plan); msg != "" {
		resp.Diagnostics.AddAttributeError(path.Root("type"), "Invalid MCP server configuration", msg)
		return
	}

	in := client.McpServerInput{
		Name:        plan.Name.ValueString(),
		Description: stringPtr(plan.Description),
		Type:        client.ServerType(plan.Type.ValueString()),
		Command:     stringPtr(plan.Command),
		Args:        stringList(ctx, plan.Args, &resp.Diagnostics),
		Env:         stringMap(ctx, plan.Env, &resp.Diagnostics),
		URL:         stringPtr(plan.URL),
		BearerToken: stringPtr(plan.BearerToken),
		Headers:     stringMap(ctx, plan.Headers, &resp.Diagnostics),
	}
	owner, err := userIDForVisibility(plan.IsPublic, r.client.UserID(), true)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("is_public"), "Invalid visibility", err.Error())
		return
	}
	in.UserID = owner
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateMcpServer(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create MetaMCP server",
			fmt.Sprintf("Creating server %q failed: %s", plan.Name.ValueString(), err))
		return
	}

	resp.Diagnostics.Append(applyMcpServer(ctx, &plan, created)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *mcpServerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state mcpServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, err := r.client.GetMcpServer(ctx, state.UUID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read MetaMCP server",
			fmt.Sprintf("Reading server %s failed: %s", state.UUID.ValueString(), err))
		return
	}

	resp.Diagnostics.Append(applyMcpServer(ctx, &state, got)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *mcpServerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state mcpServerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if msg := validateTransport(&plan); msg != "" {
		resp.Diagnostics.AddAttributeError(path.Root("type"), "Invalid MCP server configuration", msg)
		return
	}

	in := client.McpServerInput{
		Name:        plan.Name.ValueString(),
		Description: stringPtr(plan.Description),
		Type:        client.ServerType(plan.Type.ValueString()),
		Command:     stringPtr(plan.Command),
		Args:        stringList(ctx, plan.Args, &resp.Diagnostics),
		Env:         stringMap(ctx, plan.Env, &resp.Diagnostics),
		URL:         stringPtr(plan.URL),
		BearerToken: stringPtr(plan.BearerToken),
		Headers:     stringMap(ctx, plan.Headers, &resp.Diagnostics),
	}
	// Ownership is sent only when the value is actually changing. A null user_id
	// means *public* to this API, not "leave alone", so sending it on every
	// update would quietly make every managed server public.
	if !plan.IsPublic.Equal(state.IsPublic) {
		owner, uerr := userIDForVisibility(plan.IsPublic, r.client.UserID(), false)
		if uerr != nil {
			resp.Diagnostics.AddAttributeError(path.Root("is_public"), "Invalid visibility", uerr.Error())
			return
		}
		in.UserID = owner
	}
	if resp.Diagnostics.HasError() {
		return
	}

	updated, err := r.client.UpdateMcpServer(ctx, state.UUID.ValueString(), in)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update MetaMCP server",
			fmt.Sprintf("Updating server %s failed: %s", state.UUID.ValueString(), err))
		return
	}

	resp.Diagnostics.Append(applyMcpServer(ctx, &plan, updated)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *mcpServerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state mcpServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteMcpServer(ctx, state.UUID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Unable to delete MetaMCP server",
			fmt.Sprintf("Deleting server %s failed: %s", state.UUID.ValueString(), err))
	}
}

func (r *mcpServerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !uuidPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid MCP server import ID",
			fmt.Sprintf("Expected a server UUID, got %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("uuid"), req, resp)
}

// applyMcpServer copies API values into the model.
//
// Credential fields (`env`, `headers`, `bearer_token`) are only written back
// when the API returns a value. MetaMCP returns them on read, but silently
// dropping a value the user configured would cause a permanent diff, so an
// absent server-side value leaves the configured value in place instead.
func applyMcpServer(ctx context.Context, m *mcpServerResourceModel, s *client.McpServer) diag.Diagnostics {
	var diags diag.Diagnostics

	m.UUID = types.StringValue(s.UUID)
	m.Name = types.StringValue(s.Name)
	m.Description = stringOrNull(s.Description)
	m.Type = types.StringValue(string(s.Type))
	m.Command = stringOrNull(s.Command)
	m.URL = stringOrNull(s.URL)
	m.CreatedAt = types.StringValue(s.CreatedAt)
	// MetaMCP has no visibility column: the serializer passes user_id straight
	// through, and the server treats a null owner as public (see the
	// `effectiveUserId === null` checks in its namespace implementation), so a
	// nil owner here means public rather than "hidden from you".
	m.IsPublic = types.BoolValue(s.UserID == nil)

	if len(s.Args) > 0 || !m.Args.IsNull() {
		m.Args = stringListValue(ctx, s.Args, &diags)
	}
	if len(s.Env) > 0 || !m.Env.IsNull() {
		m.Env = stringMapValue(ctx, s.Env, &diags)
	}
	if len(s.Headers) > 0 || !m.Headers.IsNull() {
		m.Headers = stringMapValue(ctx, s.Headers, &diags)
	}
	if s.BearerToken != nil {
		m.BearerToken = types.StringValue(*s.BearerToken)
	}

	return diags
}
