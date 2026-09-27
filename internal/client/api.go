package client

import "context"

// This file mirrors the server's zod schemas field-for-field. Names and
// optionality come from the MetaMCP source (packages/zod-types), not from
// guesswork: a field that is nullable server-side is a *string here, and one
// that is always present is a plain string.

// ---------------------------------------------------------------------------
// Shared enums
// ---------------------------------------------------------------------------

// ServerType is McpServerTypeEnum.
type ServerType string

// Server type values accepted by the API.
const (
	ServerTypeStdio          ServerType = "STDIO"
	ServerTypeSSE            ServerType = "SSE"
	ServerTypeStreamableHTTP ServerType = "STREAMABLE_HTTP"
)

// ServerStatus is McpServerStatusEnum.
type ServerStatus string

// Server status values.
const (
	ServerStatusActive   ServerStatus = "ACTIVE"
	ServerStatusInactive ServerStatus = "INACTIVE"
)

// ---------------------------------------------------------------------------
// Namespaces
// ---------------------------------------------------------------------------

// Namespace is NamespaceSchema.
//
// Servers is populated only by GetNamespace: the API's namespaces.get returns a
// NamespaceWithServersSchema (namespace plus its associated servers), whereas
// namespaces.list returns bare namespaces with no servers array at all. Treat a
// nil Servers as "not requested", not as "no servers associated".
type Namespace struct {
	UUID        string  `json:"uuid"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	UserID      *string `json:"user_id"`

	// Servers is present only on responses from namespaces.get.
	Servers []McpServer `json:"servers,omitempty"`
}

// NamespaceInput is CreateNamespaceRequestSchema / UpdateNamespaceRequestSchema.
type NamespaceInput struct {
	Name           string   `json:"name"`
	Description    *string  `json:"description,omitempty"`
	McpServerUUIDs []string `json:"mcpServerUuids,omitempty"`
	UserID         *string  `json:"user_id,omitempty"`
}

// namespaceEnvelope is the {success, data, message} wrapper these procedures return.
type namespaceEnvelope struct {
	Success bool       `json:"success"`
	Data    *Namespace `json:"data"`
}

type namespaceListEnvelope struct {
	Success bool        `json:"success"`
	Data    []Namespace `json:"data"`
}

// ListNamespaces returns every namespace.
func (c *Client) ListNamespaces(ctx context.Context) ([]Namespace, error) {
	var out namespaceListEnvelope
	if err := c.callRaw(ctx, "namespaces.list", nil, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return []Namespace{}, nil
	}
	return out.Data, nil
}

// GetNamespace fetches one namespace by UUID.
func (c *Client) GetNamespace(ctx context.Context, uuid string) (*Namespace, error) {
	var out namespaceEnvelope
	if err := c.callRaw(ctx, "namespaces.get", map[string]string{"uuid": uuid}, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, ErrNotFound
	}
	return out.Data, nil
}

// CreateNamespace creates a namespace and returns it.
func (c *Client) CreateNamespace(ctx context.Context, in NamespaceInput) (*Namespace, error) {
	var out namespaceEnvelope
	if err := c.callRaw(ctx, "namespaces.create", in, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		// The API reports failures as success:false rather than an error, so an
		// absent payload must not be reported as a successful create.
		return nil, ErrNotFound
	}
	return out.Data, nil
}

// UpdateNamespace updates a namespace in place.
func (c *Client) UpdateNamespace(ctx context.Context, uuid string, in NamespaceInput) (*Namespace, error) {
	body := struct {
		NamespaceInput
		UUID string `json:"uuid"`
	}{NamespaceInput: in, UUID: uuid}

	var out namespaceEnvelope
	if err := c.callRaw(ctx, "namespaces.update", body, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, ErrNotFound
	}
	return out.Data, nil
}

// DeleteNamespace removes a namespace.
func (c *Client) DeleteNamespace(ctx context.Context, uuid string) error {
	var out struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := c.callRaw(ctx, "namespaces.delete", map[string]string{"uuid": uuid}, &out); err != nil {
		return err
	}
	if !out.Success {
		return &tRPCError{Message: out.Message, Data: struct {
			Code       string `json:"code"`
			HTTPStatus int    `json:"httpStatus"`
			Path       string `json:"path"`
		}{Code: "DELETE_FAILED"}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// MCP servers
// ---------------------------------------------------------------------------

// McpServer is McpServerSchema.
type McpServer struct {
	UUID        string            `json:"uuid"`
	Name        string            `json:"name"`
	Description *string           `json:"description"`
	Type        ServerType        `json:"type"`
	Command     *string           `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	URL         *string           `json:"url"`
	CreatedAt   string            `json:"created_at"`
	BearerToken *string           `json:"bearerToken"`
	Headers     map[string]string `json:"headers"`
	UserID      *string           `json:"user_id"`
	ErrorStatus string            `json:"error_status,omitempty"`
}

// McpServerInput is CreateMcpServerRequestSchema / UpdateMcpServerRequestSchema.
//
// Server-side validation is worth mirroring in the provider: the name must
// match ^[a-zA-Z0-9_-]+$ with no consecutive underscores, STDIO requires
// `command`, and every other type requires a parseable `url`.
type McpServerInput struct {
	Name        string            `json:"name"`
	Description *string           `json:"description,omitempty"`
	Type        ServerType        `json:"type"`
	Command     *string           `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	URL         *string           `json:"url,omitempty"`
	BearerToken *string           `json:"bearerToken,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	UserID      *string           `json:"user_id,omitempty"`
}

type serverEnvelope struct {
	Success bool       `json:"success"`
	Data    *McpServer `json:"data"`
}

type serverListEnvelope struct {
	Success bool        `json:"success"`
	Data    []McpServer `json:"data"`
}

// ListMcpServers returns every MCP server.
func (c *Client) ListMcpServers(ctx context.Context) ([]McpServer, error) {
	var out serverListEnvelope
	if err := c.callRaw(ctx, "mcpServers.list", nil, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return []McpServer{}, nil
	}
	return out.Data, nil
}

// GetMcpServer fetches one MCP server by UUID.
func (c *Client) GetMcpServer(ctx context.Context, uuid string) (*McpServer, error) {
	var out serverEnvelope
	if err := c.callRaw(ctx, "mcpServers.get", map[string]string{"uuid": uuid}, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, ErrNotFound
	}
	return out.Data, nil
}

// CreateMcpServer creates an MCP server.
func (c *Client) CreateMcpServer(ctx context.Context, in McpServerInput) (*McpServer, error) {
	var out serverEnvelope
	if err := c.callRaw(ctx, "mcpServers.create", in, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, ErrNotFound
	}
	return out.Data, nil
}

// UpdateMcpServer updates an MCP server in place.
func (c *Client) UpdateMcpServer(ctx context.Context, uuid string, in McpServerInput) (*McpServer, error) {
	body := struct {
		McpServerInput
		UUID string `json:"uuid"`
	}{McpServerInput: in, UUID: uuid}

	var out serverEnvelope
	if err := c.callRaw(ctx, "mcpServers.update", body, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, ErrNotFound
	}
	return out.Data, nil
}

// DeleteMcpServer removes an MCP server.
func (c *Client) DeleteMcpServer(ctx context.Context, uuid string) error {
	var out struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := c.callRaw(ctx, "mcpServers.delete", map[string]string{"uuid": uuid}, &out); err != nil {
		return err
	}
	if !out.Success {
		return &tRPCError{Message: out.Message, Data: struct {
			Code       string `json:"code"`
			HTTPStatus int    `json:"httpStatus"`
			Path       string `json:"path"`
		}{Code: "DELETE_FAILED"}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Endpoints
// ---------------------------------------------------------------------------

// Endpoint is EndpointSchema.
type Endpoint struct {
	UUID              string  `json:"uuid"`
	Name              string  `json:"name"`
	Description       *string `json:"description"`
	NamespaceUUID     string  `json:"namespace_uuid"`
	EnableAPIKeyAuth  bool    `json:"enable_api_key_auth"`
	EnableOauth       bool    `json:"enable_oauth"`
	UseQueryParamAuth bool    `json:"use_query_param_auth"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	UserID            *string `json:"user_id"`
}

// EndpointInput is CreateEndpointRequestSchema.
//
// Note the asymmetry: create takes camelCase (namespaceUuid, enableApiKeyAuth)
// while the response model is snake_case (namespace_uuid). That difference is
// in the server's own schemas, not a mistake here.
type EndpointInput struct {
	Name              string  `json:"name"`
	Description       *string `json:"description,omitempty"`
	NamespaceUUID     string  `json:"namespaceUuid"`
	EnableAPIKeyAuth  *bool   `json:"enableApiKeyAuth,omitempty"`
	EnableOauth       *bool   `json:"enableOauth,omitempty"`
	UseQueryParamAuth *bool   `json:"useQueryParamAuth,omitempty"`
	CreateMcpServer   *bool   `json:"createMcpServer,omitempty"`
	UserID            *string `json:"user_id,omitempty"`
}

// EndpointUpdateInput is UpdateEndpointRequestSchema. namespaceUuid is required
// on update even though it is not the field being changed.
type EndpointUpdateInput struct {
	Name              string  `json:"name"`
	Description       *string `json:"description,omitempty"`
	NamespaceUUID     string  `json:"namespaceUuid"`
	EnableAPIKeyAuth  *bool   `json:"enableApiKeyAuth,omitempty"`
	EnableOauth       *bool   `json:"enableOauth,omitempty"`
	UseQueryParamAuth *bool   `json:"useQueryParamAuth,omitempty"`
	UserID            *string `json:"user_id,omitempty"`
}

type endpointEnvelope struct {
	Success bool      `json:"success"`
	Data    *Endpoint `json:"data"`
}

type endpointListEnvelope struct {
	Success bool       `json:"success"`
	Data    []Endpoint `json:"data"`
}

// ListEndpoints returns every endpoint.
func (c *Client) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	var out endpointListEnvelope
	if err := c.callRaw(ctx, "endpoints.list", nil, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return []Endpoint{}, nil
	}
	return out.Data, nil
}

// GetEndpoint fetches one endpoint by UUID.
func (c *Client) GetEndpoint(ctx context.Context, uuid string) (*Endpoint, error) {
	var out endpointEnvelope
	if err := c.callRaw(ctx, "endpoints.get", map[string]string{"uuid": uuid}, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, ErrNotFound
	}
	return out.Data, nil
}

// CreateEndpoint creates an endpoint.
func (c *Client) CreateEndpoint(ctx context.Context, in EndpointInput) (*Endpoint, error) {
	var out endpointEnvelope
	if err := c.callRaw(ctx, "endpoints.create", in, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, ErrNotFound
	}
	return out.Data, nil
}

// UpdateEndpoint updates an endpoint in place.
func (c *Client) UpdateEndpoint(ctx context.Context, uuid string, in EndpointUpdateInput) (*Endpoint, error) {
	body := struct {
		EndpointUpdateInput
		UUID string `json:"uuid"`
	}{EndpointUpdateInput: in, UUID: uuid}

	var out endpointEnvelope
	if err := c.callRaw(ctx, "endpoints.update", body, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, ErrNotFound
	}
	return out.Data, nil
}

// DeleteEndpoint removes an endpoint.
func (c *Client) DeleteEndpoint(ctx context.Context, uuid string) error {
	var out struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := c.callRaw(ctx, "endpoints.delete", map[string]string{"uuid": uuid}, &out); err != nil {
		return err
	}
	if !out.Success {
		return &tRPCError{Message: out.Message, Data: struct {
			Code       string `json:"code"`
			HTTPStatus int    `json:"httpStatus"`
			Path       string `json:"path"`
		}{Code: "DELETE_FAILED"}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// API keys
// ---------------------------------------------------------------------------

// APIKey is an entry from ListApiKeysResponseSchema. Note that the list
// response includes the key's secret, which is why the resource treats it as
// sensitive; the field is named Secret here rather than Key to make that
// obvious at every use site.
type APIKey struct {
	UUID      string  `json:"uuid"`
	Name      string  `json:"name"`
	Secret    string  `json:"key"`
	CreatedAt string  `json:"created_at"`
	IsActive  bool    `json:"is_active"`
	UserID    *string `json:"user_id"`
}

// APIKeyInput is CreateApiKeyRequestSchema. The name is restricted to
// ^[a-zA-Z0-9_\s-]+$ and at most 100 characters.
type APIKeyInput struct {
	Name     string  `json:"name"`
	UserID   *string `json:"user_id,omitempty"`
	IsActive *bool   `json:"is_active,omitempty"`
}

// APIKeyUpdateInput is UpdateApiKeyRequestSchema.
type APIKeyUpdateInput struct {
	Name     *string `json:"name,omitempty"`
	IsActive *bool   `json:"is_active,omitempty"`
}

type apiKeyListEnvelope struct {
	APIKeys []APIKey `json:"apiKeys"`
}

// ListAPIKeys returns the API keys visible to the authenticated user.
//
// Note: namespaces, servers and endpoints each report "not found" as a
// success:false envelope, but this procedure returns a bare list with no
// success flag. Do not assume the same envelope shape across procedures — it
// is not consistent in the API.
func (c *Client) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	var out apiKeyListEnvelope
	if err := c.callRaw(ctx, "apiKeys.list", nil, &out); err != nil {
		return nil, err
	}
	if out.APIKeys == nil {
		return []APIKey{}, nil
	}
	return out.APIKeys, nil
}

// CreateAPIKey creates an API key and returns it, including its secret. The
// secret is returned only here and by list — never in an update.
func (c *Client) CreateAPIKey(ctx context.Context, in APIKeyInput) (*APIKey, error) {
	var out APIKey
	if err := c.callRaw(ctx, "apiKeys.create", in, &out); err != nil {
		return nil, err
	}
	if out.UUID == "" {
		return nil, ErrNotFound
	}
	return &out, nil
}

// UpdateAPIKey updates an API key's name and active flag.
func (c *Client) UpdateAPIKey(ctx context.Context, uuid string, in APIKeyUpdateInput) (*APIKey, error) {
	body := struct {
		APIKeyUpdateInput
		UUID string `json:"uuid"`
	}{APIKeyUpdateInput: in, UUID: uuid}

	var out APIKey
	if err := c.callRaw(ctx, "apiKeys.update", body, &out); err != nil {
		return nil, err
	}
	if out.UUID == "" {
		return nil, ErrNotFound
	}
	return &out, nil
}

// DeleteAPIKey removes an API key.
func (c *Client) DeleteAPIKey(ctx context.Context, uuid string) error {
	var out struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := c.callRaw(ctx, "apiKeys.delete", map[string]string{"uuid": uuid}, &out); err != nil {
		return err
	}
	if !out.Success {
		return &tRPCError{Message: out.Message, Data: struct {
			Code       string `json:"code"`
			HTTPStatus int    `json:"httpStatus"`
			Path       string `json:"path"`
		}{Code: "DELETE_FAILED"}}
	}
	return nil
}

// GetAPIKeyByUUID finds an API key by scanning the list. There is no
// apiKeys.get procedure, so this is the only way to read one back.
func (c *Client) GetAPIKeyByUUID(ctx context.Context, uuid string) (*APIKey, error) {
	keys, err := c.ListAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		if keys[i].UUID == uuid {
			return &keys[i], nil
		}
	}
	return nil, ErrNotFound
}
