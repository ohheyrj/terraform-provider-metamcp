# A namespace groups MCP servers so they can be published together.
# Servers are attached by UUID, so they may be created in any order.
resource "metamcp_mcp_server" "github" {
  name = "github"
  type = "STREAMABLE_HTTP"
  url  = "https://api.githubcopilot.com/mcp/"
}

resource "metamcp_namespace" "tools" {
  name        = "tools"
  description = "Shared internal tooling"

  mcp_server_uuids = [
    metamcp_mcp_server.github.uuid,
  ]
}
