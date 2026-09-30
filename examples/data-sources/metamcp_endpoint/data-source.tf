# Look up an existing endpoint by name, or by UUID.
data "metamcp_endpoint" "tools" {
  name = "tools"
}

# The URL an MCP client connects to.
output "tools_url" {
  value = data.metamcp_endpoint.tools.url
}

# Read back from the server, not from configuration: a public endpoint is
# reachable by every user, not only its owner.
output "tools_is_public" {
  value = data.metamcp_endpoint.tools.is_public
}
