# A namespace groups MCP servers so they can be published together.
# Servers are attached by UUID, so they may be created in any order.
resource "metamcp_mcp_server" "github" {
  name = "github"
  type = "STREAMABLE_HTTP"
  url  = "https://api.githubcopilot.com/mcp/"

  # A public namespace may only contain public servers, so a server
  # destined for one must be public too. Left unset, a server is
  # private to the authenticated user.
  is_public = true
}

resource "metamcp_namespace" "tools" {
  name        = "tools"
  description = "Shared internal tooling"

  # Left unset, the namespace is private to the authenticated user.
  # Set true to make it usable by every user on the MetaMCP instance.
  is_public = true

  mcp_server_uuids = [
    metamcp_mcp_server.github.uuid,
  ]
}
