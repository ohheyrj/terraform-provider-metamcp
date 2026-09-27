# Look up an existing server by name, or by UUID.
data "metamcp_mcp_server" "github" {
  name = "github"
}

# error_status reports the last connection error MetaMCP saw, if any: a quick
# way to check a server is reachable without opening the web UI.
output "github_status" {
  value = data.metamcp_mcp_server.github.error_status
}
