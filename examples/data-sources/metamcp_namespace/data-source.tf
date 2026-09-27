# Look up an existing namespace by name, or by UUID. Exactly one is required.
data "metamcp_namespace" "by_name" {
  name = "tools"
}

output "tools_uuid" {
  value = data.metamcp_namespace.by_name.uuid
}

# The associated servers come back as UUIDs, which is useful for checking what
# is published without managing it here.
output "tools_servers" {
  value = data.metamcp_namespace.by_name.mcp_server_uuids
}
