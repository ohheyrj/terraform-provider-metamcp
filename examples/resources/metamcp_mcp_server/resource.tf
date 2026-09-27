# Remote server: SSE or STREAMABLE_HTTP, addressed by URL.
resource "metamcp_mcp_server" "github" {
  name = "github"
  type = "STREAMABLE_HTTP"
  url  = "https://api.githubcopilot.com/mcp/"

  # Sensitive: these routinely carry credentials, and are flagged as such so
  # Terraform treats them carefully in state and plan output.
  bearer_token = var.github_token
}

# Local server: STDIO, run as a subprocess, so it needs command and args.
resource "metamcp_mcp_server" "filesystem" {
  name    = "filesystem"
  type    = "STDIO"
  command = "npx"
  args    = ["-y", "@modelcontextprotocol/server-filesystem", "/data"]

  env = {
    LOG_LEVEL = "info"
  }
}
