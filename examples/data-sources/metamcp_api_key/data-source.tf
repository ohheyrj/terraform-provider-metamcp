# Look up an existing API key by name, or by UUID.
data "metamcp_api_key" "ci" {
  name = "ci-pipeline"
}

output "ci_key_active" {
  value = data.metamcp_api_key.ci.is_active
}

# Whether the key is usable by every user rather than only its owner. Read from
# the server, so this reflects a key changed in MetaMCP's own UI.
output "ci_key_public" {
  value = data.metamcp_api_key.ci.is_public
}
