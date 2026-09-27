# Look up an existing endpoint by name, or by UUID.
data "metamcp_endpoint" "tools" {
  name = "tools"
}

# The URL an MCP client connects to.
output "tools_url" {
  value = data.metamcp_endpoint.tools.url
}
