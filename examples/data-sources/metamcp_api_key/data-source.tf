# Look up an existing API key by name, or by UUID.
data "metamcp_api_key" "ci" {
  name = "ci-pipeline"
}

output "ci_key_active" {
  value = data.metamcp_api_key.ci.is_active
}
