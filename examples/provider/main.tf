terraform {
  required_providers {
    metamcp = {
      source = "ohheyrj/metamcp"
    }
  }
}

provider "metamcp" {
  # Credentials come from the environment:
  #   METAMCP_ENDPOINT, METAMCP_EMAIL, METAMCP_PASSWORD
}

resource "metamcp_namespace" "example" {
  name        = "example"
  description = "Managed by Terraform"
}

resource "metamcp_mcp_server" "remote" {
  name        = "example-remote"
  description = "A remote MCP server"
  type        = "STREAMABLE_HTTP"
  url         = "https://example.com/mcp"
}

resource "metamcp_mcp_server" "local" {
  name    = "example-local"
  type    = "STDIO"
  command = "npx"
  args    = ["-y", "@modelcontextprotocol/server-everything"]

  env = {
    LOG_LEVEL = "info"
  }
}

resource "metamcp_endpoint" "example" {
  name           = "example"
  description    = "Public endpoint for the example namespace"
  namespace_uuid = metamcp_namespace.example.uuid

  enable_api_key_auth = true
  enable_oauth        = false
}

resource "metamcp_api_key" "example" {
  name = "example-client"
}

output "mcp_url" {
  description = "The URL an MCP client connects to."
  value       = metamcp_endpoint.example.url
}

output "api_key" {
  description = "Credential for the MCP gateway, not for this provider."
  value       = metamcp_api_key.example.key
  sensitive   = true
}
