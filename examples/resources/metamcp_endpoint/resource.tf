resource "metamcp_namespace" "tools" {
  name = "tools"
}

# An endpoint is the URL MCP clients actually connect to. It publishes one
# namespace, so reference that namespace's uuid.
resource "metamcp_endpoint" "tools" {
  name           = "tools"
  namespace_uuid = metamcp_namespace.tools.uuid
  description    = "Internal tooling endpoint"

  enable_api_key_auth = true
  enable_oauth        = false

  # Reachable by every user rather than only this one. A public endpoint may
  # only publish a public namespace.
  is_public = true
}

output "mcp_url" {
  value = metamcp_endpoint.tools.url
}
